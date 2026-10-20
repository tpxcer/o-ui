package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

// 仅显式新临时目录：seed保持核心运行等待外部中断，verify在同一挂载路径重开。
// 夹具含临时凭据，不打印内容，不属于产品备份格式。
type durableCheckpoint struct {
	Token        string
	TargetPort   int
	Link         string
	Subscription string
	Payload      []byte
	Documents    map[string]store.Document
	Count        traffic.Count
	History      int64
}

func TestOfficialDurableRecovery(t *testing.T) {
	root, stage := os.Getenv("PANEL_DURABLE_QA_DIR"), os.Getenv("PANEL_DURABLE_QA_PHASE")
	if root == "" && stage == "" {
		t.Skip("显式指定全新持久测试目录及seed/verify阶段后验证异常停止恢复")
	}
	if !filepath.IsAbs(root) || (stage != "seed" && stage != "verify") || os.Getenv("PANEL_RUNTIME_QA_CORE") == "" {
		t.Fatal("requires absolute isolated QA directory, explicit phase and official core")
	}
	if stage == "seed" {
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal("refuses an existing seed directory", err)
		}
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		t.Fatal("requires private ordinary QA directory")
	}
	ctx := context.Background()
	m, err := core.NewManager(os.Getenv("PANEL_RUNTIME_QA_CORE"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Selected() != core.DefaultVersion {
		t.Fatal("durable QA only supports the fixed default core")
	}
	binary, err := m.Binary(core.DefaultVersion)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(filepath.Join(root, "panel"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	checkpointFile := filepath.Join(root, "qa-checkpoint.json")
	var saved durableCheckpoint
	if stage == "seed" {
		session, err := state.Initialize(ctx, "durableqa", "isolated-durable-qa-passphrase")
		if err != nil {
			t.Fatal(err)
		}
		saved.Token = session.Token
	} else {
		data, err := os.ReadFile(checkpointFile)
		if err != nil || json.Unmarshal(data, &saved) != nil || saved.TargetPort < 1024 || saved.History <= 0 || len(saved.Documents) != 5 {
			t.Fatal("missing/invalid private QA checkpoint")
		}
		if session, err := state.Authenticate(ctx, saved.Token); err != nil || session.Username != "durableqa" {
			t.Fatal("committed administrator/session lost")
		}
		if _, err := state.Login(ctx, "durableqa", "isolated-durable-qa-passphrase"); err != nil {
			t.Fatal("committed password no longer authenticates")
		}
	}
	rt, err := NewRuntime(m, state, filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	handler := Handler(m, state, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	cookie := &http.Cookie{Name: sessionCookie, Value: saved.Token}
	call := func(method, path string, value any, want int) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "http://127.0.0.1:5180/api/"+path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("durable API status/boundary mismatch", method, path, w.Code, want)
		}
		return w.Body.Bytes()
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(saved.TargetPort)))
	if err != nil {
		t.Fatal(err)
	}
	saved.TargetPort = listener.Addr().(*net.TCPAddr).Port
	target := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "durable-original-identity-ok")
	})}
	go func() { _ = target.Serve(listener) }()
	defer target.Close()
	names := []string{"workspace", "xray-settings", "proxy-identities", subscriptionsDocument, appliedDocument}
	if stage == "seed" {
		cert, key := runtimeCertificate(t, t.TempDir()) // 不在持久盘；重启后源文件必须不存在。
		certificate, err := os.ReadFile(cert)
		if err != nil || writePrivate(filepath.Join(root, "client-trust.pem"), certificate) != nil {
			t.Fatal("cannot persist private QA trust copy")
		}
		var draft workspaceDraft
		_ = json.Unmarshal(validWorkspace(), &draft)
		draft.Clients[0].Subscription = true
		draft.Inbounds[0].Preset, draft.Inbounds[0].Port = "xhttp-tls", runtimePort(t)
		draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/durable-qa", Mode: "auto"}
		data, _ := json.Marshal(draft)
		call("PUT", "workspace", store.Document{Data: data}, 200)
		settings := json.RawMessage(fmt.Sprintf(`{"freedomStrategy":"AsIs","routingStrategy":"AsIs","probeURL":"https://example.test/","statistics":true,"accessLog":false,"dnsLog":false,"logLevel":"warning","rules":"[]","outbounds":%q,"balancers":"[]","dns":"{\"servers\":[\"localhost\"]}","advanced":"{}"}`, fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%d"}]}}]`, saved.TargetPort)))
		call("PUT", "settings", store.Document{Data: settings}, 200)
		call("POST", "runtime/apply", runtimeRequest{Version: core.DefaultVersion, WorkspaceRevision: 1, SettingsRevision: 1}, 200)
		var exported connectionResult
		if json.Unmarshal(call("POST", "runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, 200), &exported) != nil {
			t.Fatal("connection export failed")
		}
		saved.Link = exported.Link
		grant := subscriptionCall(t, handler, "rotate", subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1, Confirm: true}, cookie, 200)
		saved.Subscription, saved.Payload = grant.Path, subscriptionGET(t, handler, grant.Path, 200)
		tlsSettings := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
		tlsSettings["disableSystemRoot"], tlsSettings["certificates"] = true, []any{map[string]string{"certificateFile": filepath.Join(root, "client-trust.pem"), "usage": "verify"}}
		data, _ = json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": runtimePort(t), "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}})
		if err := writePrivate(filepath.Join(root, "client.json"), data); err != nil {
			t.Fatal(err)
		}
		for _, source := range []string{cert, key} {
			if err := os.Rename(source, source+".unavailable"); err != nil {
				t.Fatal("cannot isolate temporary certificate sources", err)
			}
		}
	} else {
		docs, err := state.ReadDocuments(ctx, names)
		if err != nil || !reflect.DeepEqual(docs, saved.Documents) {
			t.Fatal("committed workspace/settings/identity/subscription/history changed")
		}
		var draft workspaceDraft
		if json.Unmarshal(docs["workspace"].Data, &draft) != nil || len(draft.Inbounds) != 1 || draft.Inbounds[0].Connection == nil {
			t.Fatal("invalid original workspace")
		}
		for _, source := range []string{draft.Inbounds[0].Connection.CertificateFile, draft.Inbounds[0].Connection.KeyFile} {
			if _, err := os.Stat(source); !os.IsNotExist(err) {
				t.Fatal("old source certificate still available; cannot prove snapshot recovery")
			}
		}
		ledger, _, err := rt.ledger(ctx)
		if err != nil || ledger.Active || !ledger.Incomplete || ledger.Totals["client-one"].Uplink < saved.Count.Uplink || ledger.Totals["client-one"].Downlink < saved.Count.Downlink || rt.engine.Status().Running {
			t.Fatal("cold startup lost checkpoint, hid gap or automatically started core")
		}
		saved.Count = ledger.Totals["client-one"]
		subscriptionGET(t, handler, saved.Subscription, 503)
		history := rt.historySummary(ctx)
		if !history.Available || history.Revision != saved.History || history.Version != core.DefaultVersion {
			t.Fatal("committed history unavailable after cold startup")
		}
		call("POST", "runtime/resume", map[string]any{"revision": history.Revision, "confirmation": "确认恢复上次应用"}, 200)
		var exported connectionResult
		if json.Unmarshal(call("POST", "runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, 200), &exported) != nil || exported.Link != saved.Link || !bytes.Equal(subscriptionGET(t, handler, saved.Subscription, 200), saved.Payload) {
			t.Fatal("recovery changed original proxy identity or subscription")
		}
	}
	clientFile := filepath.Join(root, "client.json")
	data, err := os.ReadFile(clientFile)
	var clientConfig struct {
		Inbounds []struct{ Port int }
	}
	if err != nil || json.Unmarshal(data, &clientConfig) != nil || len(clientConfig.Inbounds) != 1 {
		t.Fatal("invalid persisted test client")
	}
	clientEngine := &engine.Engine{}
	defer clientEngine.Close()
	if err := clientEngine.Apply(ctx, engine.Candidate{Binary: binary, Version: core.DefaultVersion, File: clientFile, Inbounds: 1}); err != nil {
		t.Fatal("original client failed to start", err)
	}
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientConfig.Inbounds[0].Port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", saved.TargetPort))
	if err != nil {
		t.Fatal("original identity cannot forward after startup", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "durable-original-identity-ok" {
		t.Fatal("invalid actual proxy response")
	}
	rt.mu.Lock()
	err = rt.checkpointCurrent(ctx)
	rt.mu.Unlock()
	if err != nil {
		t.Fatal("actual traffic checkpoint failed", err)
	}
	ledger, _, err := rt.ledger(ctx)
	if err != nil || !ledger.Active || ledger.Totals["client-one"].Uplink == 0 || ledger.Totals["client-one"].Downlink == 0 {
		t.Fatal("actual committed bidirectional traffic missing")
	}
	if stage == "seed" {
		saved.Count = ledger.Totals["client-one"]
		saved.Documents, err = state.ReadDocuments(ctx, names)
		if err != nil {
			t.Fatal(err)
		}
		saved.History = rt.historySummary(ctx).Revision
		data, _ = json.Marshal(saved)
		if err := writePrivate(checkpointFile, data); err != nil {
			t.Fatal(err)
		}
		directory, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			t.Fatal("QA checkpoint directory sync failed", err)
		}
		fmt.Println("PANEL_DURABLE_READY_FOR_FORCE_STOP")
		<-time.After(3 * time.Minute)
		t.Fatal("external test interruption did not arrive")
	}
	if ledger.Totals["client-one"].Uplink <= saved.Count.Uplink || ledger.Totals["client-one"].Downlink <= saved.Count.Downlink || !ledger.Incomplete {
		t.Fatal("restored traffic did not increase or concealed pre-crash gap")
	}
	call("POST", "runtime/stop", map[string]any{}, 200)
	if rt.engine.Status().Running {
		t.Fatal("explicit stop left core running")
	}
	fmt.Println("PANEL_DURABLE_RECOVERY_PASSED")
}
