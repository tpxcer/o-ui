package localapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

// 实际重启测试面板；只使用已验证官方包的临时副本与临时数据库。
func TestOfficialHistoryProcessRecovery(t *testing.T) {
	source := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if source == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证实际面板进程运行历史恢复")
	}
	version := runtimeQAVersion(t)
	coreRoot := t.TempDir()
	manager, err := core.NewManager(source)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if manager.Selected() != version {
		t.Fatal("selected core does not match explicit QA version")
	}
	for _, version := range []string{version, "26.3.27"} {
		if _, err := manager.Binary(version); err != nil {
			t.Fatal("requires both verified official test versions", version, err)
		}
		from, to := filepath.Join(source, "v"+version), filepath.Join(coreRoot, "v"+version)
		if err := os.CopyFS(to, os.DirFS(from)); err != nil {
			t.Fatal(err)
		}
		if err := filepath.WalkDir(to, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			mode := fs.FileMode(0600)
			if entry.IsDir() || entry.Name() == "xray" {
				mode = 0700
			}
			return os.Chmod(path, mode)
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = manager.Close()
	fixtureManager, err := core.NewManager(coreRoot)
	if err != nil {
		t.Fatal(err)
	}
	err = fixtureManager.Install(context.Background(), version)
	_ = fixtureManager.Close()
	if err != nil {
		t.Fatal("select verified temporary test core", err)
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := processQABinary(t, project)
	stateRoot := t.TempDir()
	address := fmt.Sprintf("127.0.0.1:%d", runtimePort(t))
	base := "http://" + address
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 25 * time.Second}
	var command *exec.Cmd
	var done chan struct{}
	var processErr error
	stop := func(kill bool) {
		t.Helper()
		if command == nil {
			return
		}
		select {
		case <-done:
		default:
			if kill {
				_ = command.Process.Kill()
			} else {
				_ = command.Process.Signal(os.Interrupt)
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Error("panel shutdown timeout")
			}
			if !kill && processErr != nil {
				t.Error("panel normal shutdown failed", processErr)
			}
		}
		command = nil
	}
	defer stop(false)
	launch := func() {
		t.Helper()
		command = exec.Command(binary, "-listen", address, "-state", stateRoot, "-data", coreRoot, "-dist", filepath.Join(project, "dist"))
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done = make(chan struct{})
		current, finished := command, done
		go func() { processErr = current.Wait(); close(finished) }()
		deadline := time.Now().Add(8 * time.Second)
		for {
			select {
			case <-done:
				t.Fatal("panel exited before ready", processErr)
			default:
			}
			response, err := client.Get(base + "/api/auth/status")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("panel readiness timeout")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	call := func(method, path string, value any, want int) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest(method, base+"/api/"+path, bytes.NewReader(data))
		request.Header.Set("Origin", base)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("panel request failed", method, path, err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != want || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("panel API status/boundary mismatch", method, path, response.StatusCode, "want", want, err)
		}
		return body
	}
	status := func() engine.Status {
		t.Helper()
		var result engine.Status
		if json.Unmarshal(call("GET", "runtime/status", nil, 200), &result) != nil {
			t.Fatal("invalid status")
		}
		return result
	}
	history := func() appliedSummary {
		t.Helper()
		var result appliedSummary
		body := call("GET", "runtime/history", nil, 200)
		if json.Unmarshal(body, &result) != nil || strings.Contains(string(body), "Candidate") || strings.Contains(string(body), stateRoot) {
			t.Fatal("invalid or private history summary")
		}
		return result
	}
	launch()
	call("POST", "auth/setup", map[string]string{"username": "historyqa", "password": "history-process-test-passphrase"}, 200)
	if history().Available || status().Running {
		t.Fatal("empty installation has history or running core")
	}
	db, err := sql.Open("sqlite", filepath.Join(stateRoot, "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	readDoc := func(name string) store.Document {
		t.Helper()
		var doc store.Document
		if err := db.QueryRow("SELECT revision,data FROM documents WHERE name=?", name).Scan(&doc.Revision, &doc.Data); err != nil {
			t.Fatal("test document unavailable", name, err)
		}
		return doc
	}
	ledger := func() traffic.Ledger {
		t.Helper()
		var result traffic.Ledger
		if json.Unmarshal(readDoc("traffic").Data, &result) != nil {
			t.Fatal("invalid test ledger")
		}
		return result
	}
	cert, key := runtimeCertificate(t, t.TempDir())
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	serverPort := runtimePort(t)
	draft.Inbounds[0].Preset, draft.Inbounds[0].Port = "xhttp-tls", serverPort
	draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/history-test", Mode: "auto"}
	expires := time.Now().Add(20 * time.Second).UnixMilli()
	expiring, quota := draft.Clients[0], draft.Clients[0]
	expiring.ID, expiring.Name, expiring.ExpiresAt = "expiring", "expiring", &expires
	quota.ID, quota.Name, quota.QuotaGB = "quota", "quota", 0.00001
	draft.Clients = append(draft.Clients, expiring, quota)
	data, _ := json.Marshal(draft)
	call("PUT", "workspace", store.Document{Data: data}, 200)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "history-process-ok") }))
	defer origin.Close()
	_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
	settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, originPort), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
	data, _ = json.Marshal(settings)
	call("PUT", "settings", store.Document{Data: data}, 200)
	call("POST", "runtime/apply", runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}, 200)
	first := history()
	resume := map[string]any{"revision": first.Revision, "confirmation": "确认恢复上次应用"}
	call("POST", "runtime/resume", resume, 422)
	clients := map[string]*http.Client{}
	for _, id := range []string{"client-one", "expiring", "quota"} {
		var out connectionResult
		if json.Unmarshal(call("POST", "runtime/connection", connectionRequest{ClientID: id, WorkspaceRevision: 1}, 200), &out) != nil {
			t.Fatal("connection export failed")
		}
		tlsSettings := out.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
		tlsSettings["disableSystemRoot"] = true
		tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
		port := runtimePort(t)
		data, _ := json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{out.Outbound}})
		file := filepath.Join(t.TempDir(), "client.json")
		if err := writePrivate(file, data); err != nil {
			t.Fatal(err)
		}
		clientEngine := &engine.Engine{}
		defer clientEngine.Close()
		if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: filepath.Join(coreRoot, "v"+version, "xray"), File: file, Inbounds: 1}); err != nil {
			t.Fatal(err)
		}
		proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		clients[id] = &http.Client{Transport: transport, Timeout: 2 * time.Second}
	}
	roundTrip := func(id string, allowed bool) {
		t.Helper()
		response, err := clients[id].Get(origin.URL)
		if !allowed {
			if err == nil {
				response.Body.Close()
				t.Fatal("blocked historical identity forwarded traffic", id)
			}
			return
		}
		if err != nil {
			t.Fatal("original historical identity failed", id, err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || string(body) != "history-process-ok" {
			t.Fatal("invalid proxy response", id, err)
		}
	}
	for id := range clients {
		roundTrip(id, true)
	}
	// 较新草稿与所选版本均不同；恢复不得夹带这些尚未应用的内容。
	for index := range draft.Clients {
		draft.Clients[index].Enabled = false
	}
	data, _ = json.Marshal(draft)
	call("PUT", "workspace", store.Document{Revision: 1, Data: data}, 200)
	settings["outbounds"] = `[{"tag":"direct","protocol":"vless","settings":{"vnext":[]}}]`
	data, _ = json.Marshal(settings)
	call("PUT", "settings", store.Document{Revision: 1, Data: data}, 200)
	call("POST", "core/install", map[string]string{"version": "26.3.27"}, 200)
	workspaceBefore, settingsBefore := readDoc("workspace"), readDoc("xray-settings")
	stop(false)
	if time.Now().UnixMilli() >= expires {
		t.Fatal("fixture expiry reached before shutdown; cannot prove recovery filtering")
	}
	persisted := ledger()
	if persisted.Totals["client-one"].Total() == 0 || !first.Available || historyDataCount(readDoc(appliedDocument).Data) != 3 {
		t.Fatal("missing checkpoint/history before real process restart")
	}
	// 在停止的临时库注入已耗尽累计；不改变运行历史或用户数据库。
	persisted.Totals["quota"] = traffic.Count{Uplink: 10000}
	data, _ = json.Marshal(persisted)
	if _, err := db.Exec("UPDATE documents SET revision=revision+1,data=? WHERE name='traffic'", data); err != nil {
		t.Fatal(err)
	}
	var record appliedSnapshot
	_ = json.Unmarshal(readDoc(appliedDocument).Data, &record)
	oldSocketDir := filepath.Dir(strings.TrimPrefix(record.Candidate.StatsAddress, "unix://"))
	// 正常退出应已删除本进程统计套接字目录；恢复不得依赖旧路径。
	if _, err := os.Stat(oldSocketDir); !os.IsNotExist(err) {
		t.Fatal("normal panel shutdown kept its stats socket directory", err)
	}
	for _, path := range []string{cert, key} {
		if err := os.Rename(path, path+".unavailable"); err != nil {
			t.Fatal("isolate old source paths", err)
		}
	}
	if wait := time.Until(time.UnixMilli(expires)); wait > 0 {
		time.Sleep(wait + 25*time.Millisecond)
	}
	assertDrafts := func() {
		t.Helper()
		if now := readDoc("workspace"); now.Revision != workspaceBefore.Revision || !bytes.Equal(now.Data, workspaceBefore.Data) {
			t.Fatal("recovery changed workspace draft")
		}
		if now := readDoc("xray-settings"); now.Revision != settingsBefore.Revision || !bytes.Equal(now.Data, settingsBefore.Data) {
			t.Fatal("recovery changed settings draft")
		}
	}
	waitRunning := func() engine.Status {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if current := status(); current.Running {
				return current
			}
			if time.Now().After(deadline) {
				t.Fatal("core running at last exit was not resumed on start")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	assertStaysStopped := func() {
		t.Helper()
		for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(100 * time.Millisecond) {
			if status().Running {
				t.Fatal("core not running at last exit was resumed on start")
			}
		}
	}
	var selected core.Status
	// 正常退出时核心在运行：启动后按原历史自动恢复，不使用较新草稿或另选版本，仍剔除已受限用户。
	launch()
	if current := waitRunning(); current.Version != version || current.WorkspaceRevision != 1 || current.SettingsRevision != 1 || !history().Available || ledger().Totals["client-one"].Total() < persisted.Totals["client-one"].Total() {
		t.Fatal("auto resume used selected version/newer drafts or lost history/checkpoint")
	}
	assertDrafts()
	_ = json.Unmarshal(call("GET", "core/status", nil, 200), &selected)
	if selected.Selected != "26.3.27" || historyDataCount(readDoc(appliedDocument).Data) != 1 {
		t.Fatal("auto resume changed selection or revived blocked users")
	}
	roundTrip("client-one", true)
	roundTrip("expiring", false)
	roundTrip("quota", false)
	// 手动停止后退出，再启动不得自动恢复；显式恢复仍走原确认流程。
	call("POST", "runtime/stop", map[string]any{}, 200)
	stop(false)
	launch()
	assertStaysStopped()
	stopped := history()
	if !stopped.Available {
		t.Fatal("manual stop lost history")
	}
	resume["revision"] = stopped.Revision
	call("POST", "runtime/resume", map[string]any{"revision": stopped.Revision}, 409)
	call("POST", "runtime/resume", map[string]any{"revision": stopped.Revision + 1, "confirmation": "确认恢复上次应用"}, 409)
	call("POST", "runtime/resume-preflight", map[string]any{"revision": stopped.Revision}, 200)
	if status().Running || history().Revision != stopped.Revision {
		t.Fatal("history preflight applied/started core")
	}
	call("POST", "runtime/resume", resume, 200)
	if current := status(); !current.Running || current.Version != version || current.WorkspaceRevision != 1 || current.SettingsRevision != 1 {
		t.Fatal("restore used selected version or newer drafts")
	}
	assertDrafts()
	_ = json.Unmarshal(call("GET", "core/status", nil, 200), &selected)
	if selected.Selected != "26.3.27" || historyDataCount(readDoc(appliedDocument).Data) != 1 {
		t.Fatal("recovery changed selection or revived blocked users")
	}
	roundTrip("client-one", true)
	roundTrip("expiring", false)
	roundTrip("quota", false)
	if current := ledger(); current.Totals["quota"] != persisted.Totals["quota"] || current.Totals["client-one"].Total() < persisted.Totals["client-one"].Total() {
		t.Fatal("recovery reduced cumulative usage")
	}
	// 首页重启必须保持当前已应用身份/版本，不读取较新停用草稿或另选核心。
	beforeRestart, restartHistory := status(), history()
	restart := map[string]any{"revision": restartHistory.Revision, "startedAt": beforeRestart.StartedAt, "confirmation": "确认重启当前配置"}
	call("POST", "runtime/restart", map[string]any{"revision": restartHistory.Revision, "startedAt": beforeRestart.StartedAt}, 409)
	call("POST", "runtime/restart", map[string]any{"revision": restartHistory.Revision - 1, "startedAt": beforeRestart.StartedAt, "confirmation": "确认重启当前配置"}, 409)
	call("POST", "runtime/restart", map[string]any{"revision": restartHistory.Revision, "startedAt": beforeRestart.StartedAt.Add(-time.Second), "confirmation": "确认重启当前配置"}, 409)
	call("POST", "runtime/restart-preflight", map[string]any{"revision": restartHistory.Revision, "startedAt": beforeRestart.StartedAt}, 200)
	if now := status(); !now.Running || !now.StartedAt.Equal(*beforeRestart.StartedAt) || history().Revision != restartHistory.Revision {
		t.Fatal("restart preflight or rejected request changed running instance")
	}
	beforeCount := ledger().Totals["client-one"]
	call("POST", "runtime/restart", restart, 200)
	if now := status(); !now.Running || now.StartedAt.Equal(*beforeRestart.StartedAt) || now.Version != version || now.WorkspaceRevision != 1 || now.SettingsRevision != 1 {
		t.Fatal("restart used newer draft/selection or did not replace own process")
	}
	call("POST", "runtime/restart", restart, 409)
	assertDrafts()
	_ = json.Unmarshal(call("GET", "core/status", nil, 200), &selected)
	if selected.Selected != "26.3.27" || ledger().Totals["client-one"].Total() < beforeCount.Total() || ledger().Totals["quota"] != persisted.Totals["quota"] || historyDataCount(readDoc(appliedDocument).Data) != 1 {
		t.Fatal("restart changed selection/usage or revived blocked identities")
	}
	roundTrip("client-one", true)
	roundTrip("expiring", false)
	roundTrip("quota", false)
	// 运行中写pending失败，不能停止仍正常的旧核心或改变成功历史。
	restartHistory, beforeRestart = history(), status()
	restart = map[string]any{"revision": restartHistory.Revision, "startedAt": beforeRestart.StartedAt, "confirmation": "确认重启当前配置"}
	if _, err := db.Exec(`CREATE TRIGGER restart_failure BEFORE UPDATE ON documents WHEN NEW.name='runtime-applied' AND instr(CAST(NEW.data AS TEXT), '"pending":true')>0 BEGIN SELECT RAISE(ABORT,'test restart write failure'); END`); err != nil {
		t.Fatal(err)
	}
	call("POST", "runtime/restart", restart, 422)
	if _, err := db.Exec("DROP TRIGGER restart_failure"); err != nil {
		t.Fatal(err)
	}
	if now := status(); !now.Running || !now.StartedAt.Equal(*beforeRestart.StartedAt) || history().Revision != restartHistory.Revision {
		t.Fatal("failed restart checkpoint interrupted old core")
	}
	roundTrip("client-one", true)
	// 强制终止实际面板后，监督停止自有核心；被强杀时核心在运行，重启后自动恢复并保留尾流缺口。
	stop(true)
	until := time.Now().Add(8 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", serverPort), 100*time.Millisecond)
		if err != nil {
			break
		}
		connection.Close()
		if time.Now().After(until) {
			t.Fatal("core listener survived parent SIGKILL")
		}
		time.Sleep(25 * time.Millisecond)
	}
	launch()
	waitRunning()
	if !ledger().Incomplete || !history().Available {
		t.Fatal("crash auto resume lost history or concealed usage gap")
	}
	roundTrip("client-one", true)
	assertDrafts()
	// 核心自行退出不算在运行：清除标记后，面板正常退出再启动不得自动恢复。
	output, err := exec.Command("pgrep", "-f", "run -config "+filepath.Join(stateRoot, "runtime")).Output()
	if pids := strings.Fields(string(output)); err != nil || len(pids) != 1 {
		t.Fatal("cannot locate only the panel's own core process", err, len(pids))
	} else if err = exec.Command("kill", "-9", pids[0]).Run(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); string(readDoc(autoResumeDocument).Data) != `{"resume":false}`; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("unexpected core exit kept auto resume intent")
		}
	}
	stop(false)
	launch()
	assertStaysStopped()
	call("POST", "runtime/stop", map[string]any{}, 200)
	call("POST", "runtime/restart", restart, 409)
	resume["revision"] = history().Revision
	// 切换标记写失败不能启动；最终成功记录写失败须停止新核心并留下不可恢复的pending。
	for _, pending := range []bool{true, false} {
		before := readDoc(appliedDocument)
		query := fmt.Sprintf(`CREATE TRIGGER history_failure BEFORE UPDATE ON documents WHEN NEW.name='runtime-applied' AND instr(CAST(NEW.data AS TEXT), '"pending":%t')>0 BEGIN SELECT RAISE(ABORT,'test history write failure'); END`, pending)
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
		call("POST", "runtime/resume", resume, 422)
		if status().Running {
			t.Fatal("history persistence failure left core running")
		}
		if _, err := db.Exec("DROP TRIGGER history_failure"); err != nil {
			t.Fatal(err)
		}
		if pending && !bytes.Equal(before.Data, readDoc(appliedDocument).Data) {
			t.Fatal("failed pending write changed successful history")
		}
		if !pending && (history().Available || history().Issue == "") {
			t.Fatal("uncommitted application remained recoverable")
		}
	}
	stop(false)
	launch()
	if history().Available || history().Issue == "" || status().Running {
		t.Fatal("uncertain pending history accepted after actual restart")
	}
	call("POST", "runtime/resume", map[string]any{"revision": history().Revision, "confirmation": "确认恢复上次应用"}, 422)
}

func historyDataCount(data []byte) int {
	var record appliedSnapshot
	if json.Unmarshal(data, &record) != nil {
		return -1
	}
	data, err := os.ReadFile(record.Candidate.File)
	var full struct {
		Inbounds []config.CompiledInbound `json:"inbounds"`
	}
	if err != nil || json.Unmarshal(data, &full) != nil {
		return -1
	}
	count := 0
	for _, in := range full.Inbounds {
		count += len(in.Settings.Clients)
	}
	return count
}
