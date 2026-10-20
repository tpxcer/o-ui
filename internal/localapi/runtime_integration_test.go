package localapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

func runtimeCertificate(t *testing.T, dir string, hosts ...string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "runtime-qa.test"}, DNSNames: []string{"runtime-qa.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	if len(hosts) > 0 {
		template.DNSNames = nil
		for _, host := range hosts {
			if ip := net.ParseIP(host); ip != nil {
				template.IPAddresses = append(template.IPAddresses, ip)
			} else {
				template.DNSNames = append(template.DNSNames, host)
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func runtimePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// 通过与网页完全相同的登录/保存/预检/应用/停止API验证；不写用户数据库或版本选择。
func TestOfficialRuntimeAPI(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE为已校验官方核心目录后运行")
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	version := runtimeQAVersion(t)
	if m.Selected() != version {
		t.Fatal("selected core does not match explicit QA version")
	}
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cert, key := runtimeCertificate(t, t.TempDir())
	panelSettings, _ := json.Marshal(panelTLSDraft{cert, key})
	if _, err := s.SaveDocument(context.Background(), "panel-tls", 0, panelSettings); err != nil {
		t.Fatal(err)
	}
	panelTLS, err := LoadPanelTLS(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(m, s, t.TempDir(), panelTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	var cookie *http.Cookie
	request := func(method, path string, value any, want int) []byte {
		t.Helper()
		data, _ := json.Marshal(value)
		r := httptest.NewRequest(method, "http://127.0.0.1:5180"+path, strings.NewReader(string(data)))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: status %d want %d", method, path, w.Code, want)
		}
		if cookie == nil && len(w.Result().Cookies()) > 0 {
			cookie = w.Result().Cookies()[0]
		}
		return w.Body.Bytes()
	}
	request("POST", "/api/runtime/apply", map[string]any{}, 401)
	request("POST", "/api/auth/setup", map[string]string{"username": "runtimeqa", "password": "runtime-test-only-passphrase"}, 200)
	serverPort, clientPort := runtimePort(t), runtimePort(t)
	if serverPort == clientPort {
		t.Fatal("ephemeral port collision")
	}
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	draft.Inbounds[0].Preset = "xhttp-tls"
	draft.Inbounds[0].Port = serverPort
	draft.Inbounds[0].Connection = &connectionDraft{UsePanelCertificate: true, Path: "/runtime-qa", Mode: "auto"}
	workspace, _ := json.Marshal(draft)
	request("PUT", "/api/workspace", store.Document{Data: workspace}, 200)
	zeroEntered, zeroRelease := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseZero := func() { releaseOnce.Do(func() { close(zeroRelease) }) }
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zero-downlink" {
			close(zeroEntered)
			select {
			case <-zeroRelease:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, "runtime-api-ok")
		if r.URL.Path == "/metrics-stream" {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	defer origin.Close()
	defer releaseZero()
	_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
	settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, originPort), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
	settingsData, _ := json.Marshal(settings)
	request("PUT", "/api/settings", store.Document{Data: settingsData}, 200)
	operation := runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}
	request("POST", "/api/runtime/preflight", operation, 200)
	if rt.engine.Status().Running {
		t.Fatal("preflight started proxy")
	}
	request("POST", "/api/runtime/apply", operation, 200)
	first := rt.engine.Status()
	if !first.Running {
		t.Fatal("API apply did not start own core")
	}
	metrics := waitRuntimeMetrics(t, rt)
	if *metrics.TCP != 0 || *metrics.UDP != 0 || *metrics.Memory == "0" {
		t.Fatal("空闲核心指标混入其他进程或监听")
	}
	var visibleMetrics engine.Metrics
	metricData := request("GET", "/api/runtime/metrics", nil, 200)
	if json.Unmarshal(metricData, &visibleMetrics) != nil || !visibleMetrics.Running || strings.Contains(string(metricData), rt.root) || strings.Contains(string(metricData), `"pid"`) {
		t.Fatal("指标接口不真实或泄露进程私有信息")
	}
	firstHistory, err := s.ReadDocument(context.Background(), appliedDocument)
	if err != nil {
		t.Fatal(err)
	}
	var exported connectionResult
	if json.Unmarshal(request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, 200), &exported) != nil || exported.Link == "" {
		t.Fatal("connection export failed")
	}
	request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 0}, 409)
	request("POST", "/api/runtime/connection", connectionRequest{ClientID: "missing", WorkspaceRevision: 1}, 422)
	entries, err := os.ReadDir(rt.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("runtime data accessible by other users")
		}
		if entry.IsDir() {
			files, err := os.ReadDir(filepath.Join(rt.root, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				info, err := file.Info()
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("snapshot permission mismatch")
				}
			}
		}
	}
	ids, err := rt.credentials(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	visible := request("GET", "/api/workspace", nil, 200)
	if strings.Contains(string(visible), ids.Clients["client-one"].UUID) {
		t.Fatal("API leaked connection identity")
	}
	binary, err := m.Binary(version)
	if err != nil {
		t.Fatal(err)
	}
	// 使用网页同一API导出的出站，不再手工重拼；隔离测试显式信任临时CA。
	tlsSettings := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
	tlsSettings["disableSystemRoot"] = true
	tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
	clientConfig := map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}}
	clientFile := filepath.Join(t.TempDir(), "client.json")
	data, _ := json.Marshal(clientConfig)
	if err = os.WriteFile(clientFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	clientEngine := &engine.Engine{}
	defer clientEngine.Close()
	if err = clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: clientFile, Inbounds: 1}); err != nil {
		t.Fatal(err)
	}
	proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	zeroResult := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Transport: transport, Timeout: 12 * time.Second}).Get(origin.URL + "/zero-downlink")
		if err == nil {
			defer response.Body.Close()
			var body []byte
			body, err = io.ReadAll(response.Body)
			if err == nil && string(body) != "runtime-api-ok" {
				err = fmt.Errorf("unexpected delayed body")
			}
		}
		zeroResult <- err
	}()
	select {
	case <-zeroEntered:
	case <-time.After(4 * time.Second):
		t.Fatal("zero-downlink request did not reach origin")
	}
	current, running := rt.engine.Current()
	if !running {
		t.Fatal("core stopped before zero-downlink sample")
	}
	statsContext, cancelStats := context.WithTimeout(context.Background(), 3*time.Second)
	statsCommand := exec.CommandContext(statsContext, current.Binary, "api", "statsquery", "--server="+current.StatsAddress, "-timeout=2", "-pattern=user>>>")
	statsCommand.Env = []string{"PATH=/usr/bin:/bin"}
	var statsOutput boundedOutput
	statsCommand.Stdout = &statsOutput
	statsErr := statsCommand.Run()
	cancelStats()
	var rawStats struct {
		Stat []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	if statsErr != nil || json.Unmarshal(statsOutput.Bytes(), &rawStats) != nil {
		t.Fatal("official zero-value raw sample unavailable")
	}
	omittedZero := false
	for _, stat := range rawStats.Stat {
		if stat.Name == "user>>>client-client-one>>>traffic>>>downlink" && len(stat.Value) == 0 {
			omittedZero = true
		}
	}
	if !omittedZero {
		t.Fatal("official sample did not exercise omitted protobuf zero")
	}
	counts, err := queryTraffic(context.Background(), current)
	if err != nil || counts["client-one"].Uplink == 0 || counts["client-one"].Downlink != 0 {
		t.Fatal("official omitted zero statistics rejected", err)
	}
	var beforeLedger time.Time
	rt.mu.Lock()
	ledger, _, ledgerErr := rt.ledger(context.Background())
	if ledger.SampledAt != nil {
		beforeLedger = *ledger.SampledAt
	}
	rt.mu.Unlock()
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		rt.mu.Lock()
		ledger, _, ledgerErr = rt.ledger(context.Background())
		rt.mu.Unlock()
		if ledgerErr != nil || !rt.engine.Status().Running {
			t.Fatal("zero-downlink sample triggered protective stop", ledgerErr)
		}
		if ledger.SampledAt != nil && ledger.SampledAt.After(beforeLedger) && ledger.Totals["client-one"].Uplink > 0 && ledger.Totals["client-one"].Downlink == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("zero-downlink checkpoint was not persisted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	releaseZero()
	if err = <-zeroResult; err != nil {
		t.Fatal("delayed response did not recover", err)
	}
	roundTrip := func() {
		t.Helper()
		response, err := client.Get(origin.URL)
		if err != nil {
			t.Fatal("API-generated proxy round trip failed", err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || string(body) != "runtime-api-ok" {
			t.Fatal("bad proxy response")
		}
	}
	roundTrip()
	streamClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	streamResponse, err := streamClient.Get(origin.URL + "/metrics-stream")
	if err != nil {
		t.Fatal("指标测试真实流连接失败", err)
	}
	defer streamResponse.Body.Close()
	metricDeadline := time.Now().Add(5 * time.Second)
	for {
		m := waitRuntimeMetrics(t, rt)
		if *m.TCP >= 2 {
			break
		} // 服务端入站与到目标的出站，不是客户端数。
		if time.Now().After(metricDeadline) {
			t.Fatal("官方真实TCP连接未出现在自有核心指标")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = streamResponse.Body.Close()
	operation.WorkspaceRevision = 0
	request("POST", "/api/runtime/apply", operation, 409)
	operation.WorkspaceRevision = 1
	if !rt.engine.Status().StartedAt.Equal(*first.StartedAt) {
		t.Fatal("stale revision restarted core")
	}
	previousOutbounds := settings["outbounds"]
	settings["outbounds"] = `[{"tag":"direct","protocol":"vless","settings":{"vnext":[]}}]`
	settingsData, _ = json.Marshal(settings)
	request("PUT", "/api/settings", store.Document{Revision: 1, Data: settingsData}, 200)
	operation.SettingsRevision = 2
	request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, 409)
	request("POST", "/api/runtime/apply", operation, 422)
	if data := request("GET", "/api/logs", nil, 200); !strings.Contains(string(data), `"code":"xray.preflight.failed"`) {
		t.Fatal("官方预检失败未记录")
	}
	if !rt.engine.Status().StartedAt.Equal(*first.StartedAt) {
		t.Fatal("official preflight failure interrupted core")
	}
	if current, err := s.ReadDocument(context.Background(), appliedDocument); err != nil || string(current.Data) != string(firstHistory.Data) || current.Revision != firstHistory.Revision {
		t.Fatal("failed preflight changed successful history", err)
	}
	settings["outbounds"] = previousOutbounds
	settingsData, _ = json.Marshal(settings)
	request("PUT", "/api/settings", store.Document{Revision: 2, Data: settingsData}, 200)
	operation.SettingsRevision = 3
	// 原路径不再存在：若旧运行配置未保存证书快照，接下来的回退就无法启动。
	if err = os.Rename(cert, cert+".moved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(key, key+".moved"); err != nil {
		t.Fatal(err)
	}
	// 通过官方预检但端口被占用，真实启动失败后恢复旧配置/身份。
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	draft.Inbounds[0].Port = occupied.Addr().(*net.TCPAddr).Port
	workspace, _ = json.Marshal(draft)
	request("PUT", "/api/workspace", store.Document{Revision: 1, Data: workspace}, 200)
	operation.WorkspaceRevision = 2
	request("POST", "/api/runtime/apply", operation, 422)
	logData := request("GET", "/api/logs", nil, 200)
	if !strings.Contains(string(logData), `"code":"core.port_busy"`) || !strings.Contains(string(logData), `"code":"xray.rollback.ok"`) || strings.Contains(string(logData), key) || strings.Contains(string(logData), cert) {
		t.Fatal("官方核心占用/回退脱敏诊断缺失或泄漏路径")
	}
	if status := rt.engine.Status(); !status.Running || status.WorkspaceRevision != 1 {
		t.Fatal("official runtime rollback failed")
	}
	if current, err := s.ReadDocument(context.Background(), appliedDocument); err != nil || string(current.Data) != string(firstHistory.Data) || current.Revision <= firstHistory.Revision {
		t.Fatal("failed launch did not restore successful history or invalidate stale confirmation", err)
	}
	request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 2}, 409)
	roundTrip()
	// 停用最后一个客户端必须真正关闭旧监听，而不是因空配置拒绝应用后留下旧访问。
	draft.Inbounds[0].Port = serverPort
	draft.Clients[0].Enabled = false
	workspace, _ = json.Marshal(draft)
	request("PUT", "/api/workspace", store.Document{Revision: 2, Data: workspace}, 200)
	operation.WorkspaceRevision = 3
	request("POST", "/api/runtime/apply", operation, 200)
	if status := rt.engine.Status(); status.Running || status.Inbounds != 0 || status.WorkspaceRevision != 3 {
		t.Fatal("disabled last client kept old core")
	}
	if connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", serverPort), time.Second); err == nil {
		connection.Close()
		t.Fatal("disabled client listener remains")
	}
	draft.Clients[0].Enabled = true
	workspace, _ = json.Marshal(draft)
	request("PUT", "/api/workspace", store.Document{Revision: 3, Data: workspace}, 200)
	operation.WorkspaceRevision = 4
	request("POST", "/api/runtime/apply", operation, 200)
	roundTrip()
	// 在线导出会先采样，但不能靠恢复入口中断正在运行的核心。
	var saved struct {
		Archive string `json:"archive"`
	}
	backupPassword := "runtime-backup-only-passphrase"
	if json.Unmarshal(request("POST", "/api/backup/export", map[string]string{"password": backupPassword}, 200), &saved) != nil || saved.Archive == "" {
		t.Fatal("backup export")
	}
	restoreBody := restoreRequest{Archive: saved.Archive, Password: backupPassword, Confirmation: "确认覆盖并恢复"}
	request("POST", "/api/backup/preview", restoreBody, 409)
	request("POST", "/api/backup/restore", restoreBody, 409)
	roundTrip()
	request("POST", "/api/runtime/stop", map[string]any{}, 200)
	if rt.engine.Status().Running {
		t.Fatal("API stop failed")
	}
	metricData = request("GET", "/api/runtime/metrics", nil, 200)
	visibleMetrics = engine.Metrics{}
	if json.Unmarshal(metricData, &visibleMetrics) != nil || visibleMetrics.Running || visibleMetrics.Memory != nil || visibleMetrics.TCP != nil || visibleMetrics.StartedAt != nil {
		t.Fatal("停止API后旧指标仍有效")
	}
	request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 4}, 422)
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", serverPort), time.Second)
	if err == nil {
		connection.Close()
		t.Fatal("owned listener survived API stop")
	}
	if connection, err := net.Dial("tcp", occupied.Addr().String()); err != nil {
		t.Fatal("foreign listener stopped")
	} else {
		connection.Close()
	}
	// 新安装只恢复业务包，管理员由该安装独立初始化；旧证书路径不再存在。
	if err = os.Rename(cert+".moved", cert+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(key+".moved", key+".unavailable"); err != nil {
		t.Fatal(err)
	}
	freshState, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer freshState.Close()
	freshRuntime, err := NewRuntime(m, freshState, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer freshRuntime.Close()
	handler = Handler(m, freshState, t.TempDir(), []string{"127.0.0.1:5180"}, freshRuntime)
	cookie = nil
	request("POST", "/api/auth/setup", map[string]string{"username": "restoreqa", "password": "fresh-install-test-only-passphrase"}, 200)
	var preview backupPreview
	if json.Unmarshal(request("POST", "/api/backup/preview", restoreBody, 200), &preview) != nil {
		t.Fatal("restore preview")
	}
	restoreBody.Fingerprint = preview.Fingerprint
	request("POST", "/api/backup/restore", restoreBody, 200)
	if freshRuntime.engine.Status().Running {
		t.Fatal("restore auto-started")
	}
	if panel, err := LoadPanelTLS(context.Background(), freshState); err != nil || panel != nil {
		t.Fatal("business restore imported panel HTTPS settings", err)
	}
	freshOperation := runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}
	request("POST", "/api/runtime/preflight", freshOperation, 200)
	request("POST", "/api/runtime/apply", freshOperation, 200)
	roundTrip() // 原客户端身份与受信任证书必须仍能连接，不重新生成客户端。
	request("POST", "/api/runtime/stop", map[string]any{}, 200)
	if session, err := freshState.Authenticate(context.Background(), cookie.Value); err != nil || session.Username != "restoreqa" {
		t.Fatal("restored old administrator", err)
	}
}
