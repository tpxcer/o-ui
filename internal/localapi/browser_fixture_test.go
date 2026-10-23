package localapi

import (
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
	"strings"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 显式启用的隔离浏览器验收夹具，不参与普通测试、不写.runtime或用户账号。
func TestBrowserFixture(t *testing.T) {
	if os.Getenv("PANEL_BROWSER_QA") != "1" {
		t.Skip("仅手动浏览器验收时启用")
	}
	coreRoot := os.Getenv("PANEL_BROWSER_CORE")
	if coreRoot == "" {
		coreRoot = t.TempDir()
	}
	m, err := core.NewManager(coreRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	certFile, keyFile := runtimeCertificate(t, t.TempDir(), "runtime-qa.test", "localhost", "127.0.0.1", "::1")
	var panelTLS *PanelTLS
	if os.Getenv("PANEL_BROWSER_HTTPS_QA") == "1" {
		data, _ := json.Marshal(panelTLSDraft{certFile, keyFile})
		if _, err = s.SaveDocument(context.Background(), "panel-tls", 0, data); err != nil {
			t.Fatal(err)
		}
		panelTLS, err = LoadPanelTLS(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
	}
	rt, err := NewRuntime(m, s, t.TempDir(), panelTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	// 浏览器夹具不访问外部公网信息服务，只返回文档保留地址。
	rt.public.fetch = func(_ context.Context, network string) (*publicAddress, error) {
		if network == "tcp6" {
			return &publicAddress{IP: "2001:db8::10", Location: "测试城市, 测试地区, US", Country: "US", Org: "AS64500 文档示例网络"}, nil
		}
		return &publicAddress{IP: "192.0.2.10", Hostname: "vps.example.test", Location: "测试城市, 测试地区, US", Country: "US", Org: "AS64500 文档示例网络"}, nil
	}
	t.Logf("隔离测试证书路径：%s；私钥文件路径：%s；建议测试端口：%d", certFile, keyFile, runtimePort(t))
	if _, err = s.Initialize(context.Background(), "browserqa", "test-only-passphrase-924"); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PANEL_BROWSER_LOGS_QA") == "1" {
		for i := 0; i < 270; i++ {
			rt.logs.Operation("POST /api/runtime/preflight", []int{200, 400, 503}[i%3])
		}
		rt.logs.Add("core.port_busy")
		rt.logs.Add("core.hidden")
	}
	if os.Getenv("PANEL_BROWSER_SUBSCRIPTION_QA") == "1" {
		var draft workspaceDraft
		_ = json.Unmarshal(validWorkspace(), &draft)
		draft.Clients[0].Name, draft.Clients[0].Subscription = "订阅测试用户", true
		other := draft.Clients[0]
		other.ID, other.Name, other.Subscription = "subscription-off", "订阅未开启用户", false
		draft.Clients = append(draft.Clients, other)
		data, _ := json.Marshal(draft)
		if _, err = s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("PANEL_BROWSER_FIREWALL_QA") == "1" {
		previous := runFirewall
		defer func() { runFirewall = previous }()
		doc, err := s.ReadDocument(context.Background(), "workspace")
		if err != nil {
			t.Fatal(err)
		}
		runFirewall = func(context.Context, string, string, string) (string, error) {
			return "隔离测试：不支持的INPUT拦截层", fmt.Errorf("test-only firewall failure")
		}
		if _, err = saveFirewallReport(context.Background(), s, nil, doc.Data, doc.Revision); err != nil {
			t.Fatal(err)
		}
		runFirewall = func(context.Context, string, string, string) (string, error) {
			return "隔离测试：防火墙已放行（未调用宿主防火墙）", nil
		}
	}
	if os.Getenv("PANEL_BROWSER_TRAFFIC_QA") == "1" {
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", 8192)) }))
		defer origin.Close()
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
		var draft workspaceDraft
		_ = json.Unmarshal(validWorkspace(), &draft)
		draft.Inbounds[0].Preset = "xhttp-tls"
		draft.Inbounds[0].Name = "本机统计验收"
		draft.Inbounds[0].Port = runtimePort(t)
		draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: certFile, KeyFile: keyFile, ServerName: "runtime-qa.test", Path: "/local-test", Mode: "auto"}
		draft.Clients[0].Name = "统计测试用户"
		draft.Clients[0].QuotaGB = 1
		data, _ := json.Marshal(draft)
		if _, err = s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
			t.Fatal(err)
		}
		settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, port), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
		data, _ = json.Marshal(settings)
		if _, err = s.SaveDocument(context.Background(), "xray-settings", 0, data); err != nil {
			t.Fatal(err)
		}
		rt.mu.Lock()
		c, candidateErr := rt.candidate(context.Background(), runtimeRequest{Version: "26.7.28", WorkspaceRevision: 1, SettingsRevision: 1})
		if candidateErr == nil {
			candidateErr = rt.apply(context.Background(), c)
		}
		out, exportErr := rt.connection(context.Background(), connectionRequest{ClientID: "client-one", WorkspaceRevision: 1})
		rt.mu.Unlock()
		if candidateErr != nil || exportErr != nil {
			t.Fatal("browser traffic fixture start", candidateErr, exportErr)
		}
		tlsSettings := out.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
		tlsSettings["disableSystemRoot"] = true
		tlsSettings["certificates"] = []any{map[string]string{"certificateFile": certFile, "usage": "verify"}}
		clientPort := runtimePort(t)
		data, _ = json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{out.Outbound}})
		file := filepath.Join(t.TempDir(), "client.json")
		if err = os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		clientEngine := &engine.Engine{}
		defer clientEngine.Close()
		if err = clientEngine.Apply(context.Background(), engine.Candidate{Binary: c.Binary, File: file, Inbounds: 1}); err != nil {
			t.Fatal(err)
		}
		proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		rt.mu.Lock()
		err = rt.checkpointCurrent(context.Background())
		ledger, _, ledgerErr := rt.ledger(context.Background())
		rt.mu.Unlock()
		if err != nil || ledgerErr != nil || ledger.Totals["client-one"].Total() == 0 {
			t.Fatal("browser fixture counters", err, ledgerErr)
		}
		_ = clientEngine.Close()
	}
	if os.Getenv("PANEL_BROWSER_BACKUP_QA") == "1" {
		var draft workspaceDraft
		_ = json.Unmarshal(validWorkspace(), &draft)
		draft.Inbounds[0].Name = "恢复后的测试入站"
		draft.Inbounds[0].Connection = &connectionDraft{Target: "127.0.0.1:8443", ServerName: "runtime-qa.test", Path: "/", Mode: "auto"}
		data, _ := json.Marshal(draft)
		if _, err = s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
			t.Fatal(err)
		}
		if _, err = rt.credentials(context.Background(), draft); err != nil {
			t.Fatal(err)
		}
		b, _, err := rt.capture(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		file, err := sealBackup(b, "browser-backup-test-passphrase")
		if err != nil {
			t.Fatal(err)
		}
		backupPath := filepath.Join(t.TempDir(), "browser-test.ixpb")
		if err = writePrivate(backupPath, file); err != nil {
			t.Fatal(err)
		}
		draft.Inbounds[0].Name = "恢复前的测试入站"
		draft.Clients[0].Name = "恢复前的测试用户"
		data, _ = json.Marshal(draft)
		if _, err = s.SaveDocument(context.Background(), "workspace", 1, data); err != nil {
			t.Fatal(err)
		}
		t.Logf("隔离加密测试包：%s", backupPath)
	}
	// Cookie不按端口隔离；IPv6回环与用户的127.0.0.1隔离，不新增系统地址别名。
	address := os.Getenv("PANEL_BROWSER_LISTEN")
	if address == "" {
		address = "[::1]:5182"
	}
	if address != "[::1]:5182" && address != "localhost:5184" {
		t.Fatal("仅允许隔离回环测试地址")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs("../../dist")
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{}, 1)
	handler := Handler(m, s, dist, []string{address}, rt)
	// 手动验收访问路径：PANEL_BROWSER_BASE_PATH=/abc/ 时只在该路径下提供面板。
	if path := os.Getenv("PANEL_BROWSER_BASE_PATH"); path != "" {
		if !validPanelBasePath(path) {
			t.Fatal("invalid PANEL_BROWSER_BASE_PATH")
		}
		handler = PanelHandler(m, s, dist, &PanelAccess{Data: PanelAccessDraft{Listen: address, BasePath: path}}, rt)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/__finish_browser_qa" && r.Host == address {
			select {
			case finished <- struct{}{}:
			default:
			}
			w.WriteHeader(204)
			return
		}
		handler.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	scheme := "http"
	if panelTLS != nil {
		scheme = "https"
		server.TLSConfig = panelTLS.Config()
		go func() { _ = server.ServeTLS(listener, "", "") }()
	} else {
		go func() { _ = server.Serve(listener) }()
	}
	t.Logf("隔离浏览器验收：%s://%s；最多10分钟，不使用用户数据库或会话域；启动测试需-timeout=12m以上", scheme, address)
	duration := 10 * time.Minute
	if os.Getenv("PANEL_BROWSER_EXTENDED_QA") == "1" {
		duration = 30 * time.Minute
		t.Log("本轮显式延长为30分钟，需-timeout=35m以上；不用于恢复用户旧页")
	}
	select {
	case <-finished:
	case <-time.After(duration):
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
