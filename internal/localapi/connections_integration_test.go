package localapi

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"independent-panel/internal/traffic"
)

// 所有目标和客户端均为回环地址；REALITY伪装目标也是临时TLS1.3测试服务。
func TestOfficialFourExportedConnections(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证四预设导出连接")
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
	binary, err := m.Binary(version)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"reality", "hy2", "xhttp-tls", "xhttp-reality", "xhttp-tls-ip"} {
		t.Run(variant, func(t *testing.T) {
			preset, serverName := variant, "runtime-qa.test"
			if variant == "xhttp-tls-ip" {
				preset, serverName = "xhttp-tls", "127.0.0.1"
			}
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			cert, key := runtimeCertificate(t, t.TempDir(), serverName)
			var panelTLS *PanelTLS
			if preset == "hy2" || preset == "xhttp-tls" {
				settings, _ := json.Marshal(panelTLSDraft{cert, key})
				if _, err := s.SaveDocument(context.Background(), "panel-tls", 0, settings); err != nil {
					t.Fatal(err)
				}
				panelTLS, err = LoadPanelTLS(context.Background(), s)
				if err != nil {
					t.Fatal(err)
				}
			}
			runtimeRoot := t.TempDir()
			rt, err := NewRuntime(m, s, runtimeRoot, panelTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rt.Close() }()
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
					t.Fatalf("%s: status %d want %d", path, w.Code, want)
				}
				if path == "/api/runtime/connection" && (w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "privateKey") || strings.Contains(w.Body.String(), "keyFile")) {
					t.Fatal("connection privacy headers/fields")
				}
				if cookie == nil && len(w.Result().Cookies()) > 0 {
					cookie = w.Result().Cookies()[0]
				}
				return w.Body.Bytes()
			}
			request("POST", "/api/auth/setup", map[string]string{"username": "protocolqa", "password": "protocol-test-only-passphrase"}, 200)
			pair, err := tls.LoadX509KeyPair(cert, key)
			if err != nil {
				t.Fatal(err)
			}
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "tls-target") }))
			target.EnableHTTP2 = true
			target.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
			target.StartTLS()
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "four-presets-ok") }))
			defer origin.Close()
			_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
			udpTarget, udpHits := runtimeUDPEcho(t)
			var draft workspaceDraft
			_ = json.Unmarshal(validWorkspace(), &draft)
			draft.Clients[0].Subscription = true
			disabled := draft.Clients[0]
			disabled.ID, disabled.Name, disabled.Enabled = "disabled-client", "disabled-test", false
			draft.Clients = append(draft.Clients, disabled)
			serverPort, clientPort := runtimePort(t), runtimePort(t)
			if serverPort == clientPort {
				t.Fatal("ephemeral collision")
			}
			if preset == "hy2" {
				udp, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				serverPort = udp.LocalAddr().(*net.UDPAddr).Port
				_ = udp.Close()
			}
			draft.Inbounds[0].Preset, draft.Inbounds[0].Port = preset, serverPort
			draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Target: strings.TrimPrefix(target.URL, "https://"), Path: "/local-test", Mode: "auto"}
			if panelTLS != nil {
				draft.Inbounds[0].Connection.CertificateFile, draft.Inbounds[0].Connection.KeyFile = "", ""
				draft.Inbounds[0].Connection.ServerName = ""
				draft.Inbounds[0].Connection.UsePanelCertificate = true
			}
			draft.Inbounds[0].Sniffing.IPsExcluded = []string{"192.0.2.0/24", "2001:db8::/32"}
			draft.Inbounds[0].Sniffing.DomainsExcluded = []string{"domain:example.com", "full:example.org"}
			workspace, _ := json.Marshal(draft)
			request("PUT", "/api/workspace", store.Document{Data: workspace}, 200)
			settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"},{"action":"allow","network":"udp","ip":["127.0.0.1/32"],"port":"%d"}]}}]`, originPort, udpTarget.Port), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
			settingsData, _ := json.Marshal(settings)
			request("PUT", "/api/settings", store.Document{Data: settingsData}, 200)
			// 保存即生效会启动核心；停止后导出须拒绝，再经显式应用恢复。
			request("POST", "/api/runtime/stop", map[string]any{}, 200)
			operation := runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}
			request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, 422)
			request("POST", "/api/runtime/apply", operation, 200)
			metrics := waitRuntimeMetrics(t, rt)
			if *metrics.TCP != 0 || *metrics.Memory == "0" || (preset == "hy2" && *metrics.UDP < 1) || (preset != "hy2" && *metrics.UDP != 0) {
				t.Fatal("官方核心RSS/线程/TCP/UDP未正确隔离", metrics)
			}
			current, running := rt.engine.Current()
			if !running || !strings.HasPrefix(current.StatsAddress, "unix:///") {
				t.Fatal("stats not on private unix socket")
			}
			for path, mode := range map[string]os.FileMode{filepath.Dir(strings.TrimPrefix(current.StatsAddress, "unix://")): 0700, current.PolicyFile: 0600, current.File: 0600} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatal("runtime privacy permissions", err)
				}
			}
			configData, err := os.ReadFile(current.File)
			if err != nil {
				t.Fatal(err)
			}
			var full map[string]json.RawMessage
			_ = json.Unmarshal(configData, &full)
			if !strings.Contains(string(full["inbounds"]), `"ipsExcluded":["192.0.2.0/24","2001:db8::/32"]`) || !strings.Contains(string(full["inbounds"]), `"domainsExcluded":["domain:example.com","full:example.org"]`) {
				t.Fatal("sniffing exclusions lost from running config")
			}
			if strings.Contains(string(full["api"]), "HandlerService") || !strings.Contains(string(full["api"]), "StatsService") {
				t.Fatal("unexpected management service")
			}
			var exported connectionResult
			_ = json.Unmarshal(request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, 200), &exported)
			if isTLS := preset == "hy2" || preset == "xhttp-tls"; (exported.CertificateTrusted != nil) != isTLS || (isTLS && *exported.CertificateTrusted) {
				t.Fatal("TLS预设应标记测试证书不受系统信任，REALITY不应带证书信任字段")
			}
			grantRequest := subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1, Confirm: true}
			var grant subscriptionResult
			_ = json.Unmarshal(request("POST", "/api/subscription/rotate", grantRequest, 200), &grant)
			// 订阅读取不携带管理员Cookie，且只能得到本客户端的原分享链接。
			payload := subscriptionGET(t, handler, grant.Path, 200)
			decoded, err := base64.StdEncoding.DecodeString(string(payload))
			if err != nil || !sameShareLink(string(decoded), exported.Link) {
				t.Fatal("subscription differs from applied single-client export")
			}
			// 订阅节点名为“入站名-客户端名”，第三方客户端按订阅里的链接核对节点。
			subscriptionLink := strings.TrimSuffix(string(decoded), "\n")
			token := strings.TrimPrefix(grant.Path, "/sub/")
			clashPayload := subscriptionGET(t, handler, "/sub/clash/"+token, 200)
			if !strings.HasPrefix(string(clashPayload), "proxies:\n  - name: ") || strings.Contains(string(clashPayload), "skip-cert-verify") {
				t.Fatal("unexpected Clash subscription", string(clashPayload))
			}
			formats := defaultSubscriptionSettings()
			formats.JSONFragment = true
			formatData, _ := json.Marshal(formats)
			rt.mu.Lock()
			_, formatErr := s.SaveDocument(context.Background(), subscriptionSettingsDocument, 0, formatData)
			rt.mu.Unlock()
			if formatErr != nil {
				t.Fatal(formatErr)
			}
			jsonPayload := subscriptionGET(t, handler, "/sub/json/"+token, 200)
			if strings.Contains(string(jsonPayload), `"dialerProxy": "fragment"`) == (preset == "hy2") {
				t.Fatal("JSON subscription fragment must apply to TCP transports only")
			}
			mihomoReality := strings.Contains(preset, "reality") && os.Getenv("PANEL_CLIENT_QA_MIHOMO") != ""
			setCompatibility := func(enabled bool) {
				t.Helper()
				draft.Inbounds[0].Connection.MihomoCompatibility = enabled
				workspace, _ = json.Marshal(draft)
				request("PUT", "/api/workspace", store.Document{Revision: operation.WorkspaceRevision, Data: workspace}, 200)
				operation.WorkspaceRevision++
				request("POST", "/api/runtime/apply", operation, 200)
				var after connectionResult
				_ = json.Unmarshal(request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: operation.WorkspaceRevision}, 200), &after)
				if after.Link != exported.Link || string(subscriptionGET(t, handler, grant.Path, 200)) != string(payload) {
					t.Fatal("compatibility toggle changed original identity, URI or subscription")
				}
			}
			if mihomoReality {
				t.Run("official-default", func(t *testing.T) {
					verifyThirdPartyClients(t, rt, preset, subscriptionLink, payload, cert, origin.URL, false)
				})
				setCompatibility(true)
			}
			verifyThirdPartyClients(t, rt, preset, subscriptionLink, payload, cert, origin.URL, true)
			if mihomoReality {
				setCompatibility(false)
				t.Run("compatibility-disabled", func(t *testing.T) {
					verifyThirdPartyClients(t, rt, preset, subscriptionLink, payload, cert, origin.URL, false)
				})
				setCompatibility(true)
			}
			if os.Getenv("PANEL_CLIENT_QA_MIHOMO") != "" {
				t.Run("clash-subscription", func(t *testing.T) {
					verifyThirdPartyClients(t, rt, preset, subscriptionLink, clashPayload, cert, origin.URL, true)
				})
			}
			grantRequest.WorkspaceRevision = operation.WorkspaceRevision
			grantRequest.Revision = grant.Revision
			var rotated subscriptionResult
			_ = json.Unmarshal(request("POST", "/api/subscription/rotate", grantRequest, 200), &rotated)
			subscriptionGET(t, handler, grant.Path, 404)
			subscriptionGET(t, handler, rotated.Path, 200)
			// 不等后台2秒采样：已到期或已保存累计超额就不能再导出凭据。
			rt.mu.Lock()
			doc, _ := s.ReadDocument(context.Background(), "workspace")
			testDraft := draft
			testDraft.Clients = append([]clientDraft(nil), draft.Clients...)
			expired := time.Now().Add(-time.Minute).UnixMilli()
			testDraft.Clients[0].ExpiresAt = &expired
			limited, _ := json.Marshal(testDraft)
			updated, saveErr := s.SaveDocument(context.Background(), "workspace", doc.Revision, limited)
			rt.mu.Unlock()
			if saveErr != nil {
				t.Fatal(saveErr)
			}
			subscriptionGET(t, handler, rotated.Path, 404)
			rt.mu.Lock()
			testDraft.Clients[0].ExpiresAt = nil
			testDraft.Clients[0].QuotaGB = 1e-9
			limited, _ = json.Marshal(testDraft)
			updated, saveErr = s.SaveDocument(context.Background(), "workspace", updated.Revision, limited)
			ledger, ledgerRevision, ledgerErr := rt.ledger(context.Background())
			count := ledger.Totals["client-one"]
			count.Uplink++
			ledger.Totals["client-one"] = count
			ledgerData, _ := json.Marshal(ledger)
			_, countErr := s.SaveDocument(context.Background(), "traffic", ledgerRevision, ledgerData)
			rt.mu.Unlock()
			if saveErr != nil || ledgerErr != nil || countErr != nil {
				t.Fatal("subscription limit fixture", saveErr, ledgerErr, countErr)
			}
			subscriptionGET(t, handler, rotated.Path, 404)
			// 草稿修订不同，即便身份相同也不冒充已应用节点。
			rt.mu.Lock()
			updated, saveErr = s.SaveDocument(context.Background(), "workspace", updated.Revision, workspace)
			rt.mu.Unlock()
			if saveErr != nil {
				t.Fatal(saveErr)
			}
			subscriptionGET(t, handler, rotated.Path, 503)
			// 接下来的既有生命周期验收继续使用新的真实修订。
			operation.WorkspaceRevision = updated.Revision
			request("POST", "/api/runtime/apply", operation, 200)
			subscriptionGET(t, handler, rotated.Path, 200)
			grantRequest.WorkspaceRevision, grantRequest.Revision = updated.Revision, rotated.Revision
			request("POST", "/api/subscription/revoke", grantRequest, 200)
			subscriptionGET(t, handler, rotated.Path, 404)
			request("POST", "/api/runtime/connection", connectionRequest{ClientID: "disabled-client", WorkspaceRevision: operation.WorkspaceRevision}, 422)
			u, err := url.Parse(exported.Link)
			if err != nil || u.Host != fmt.Sprintf("127.0.0.1:%d", serverPort) || u.Query().Get("sni") != serverName {
				t.Fatal("link mismatch")
			}
			stream := exported.Outbound["streamSettings"].(map[string]any)
			if tlsSettings, ok := stream["tlsSettings"].(map[string]any); ok {
				tlsSettings["disableSystemRoot"] = true
				tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
			}
			clientEngine := &engine.Engine{}
			defer clientEngine.Close()
			startClient := func(out map[string]any) {
				t.Helper()
				config := map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true}}}, "outbounds": []any{out}}
				data, _ := json.Marshal(config)
				file := filepath.Join(t.TempDir(), "client.json")
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}); err != nil {
					t.Fatal(err)
				}
			}
			proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			roundTrip := func() error {
				response, err := client.Get(origin.URL)
				if err != nil {
					return err
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				if err != nil || string(data) != "four-presets-ok" {
					return fmt.Errorf("bad response")
				}
				return nil
			}
			checkUDP := func(stage string, allowed bool) {
				t.Helper()
				if !t.Run(stage, func(t *testing.T) {
					payload := make([]byte, 1200)
					if _, err := rand.Read(payload); err != nil {
						t.Fatal(err)
					}
					before := udpHits.Load()
					err := socksUDPExchange(fmt.Sprintf("127.0.0.1:%d", clientPort), udpTarget, payload)
					if allowed {
						if err != nil || udpHits.Load() != before+1 {
							t.Fatal("exported UDP client did not reach exact echo target", err)
						}
					} else if !errors.Is(err, errUDPNoResponse) || udpHits.Load() != before {
						t.Fatal("UDP rejection bypassed or local association failed instead", err)
					}
				}) {
					t.FailNow()
				}
			}
			// JSON订阅是完整客户端配置（TCP传输经fragment出站分片）；只换本地端口并信任测试CA，官方核心须直接连通。
			var subscribed map[string]any
			if err := json.Unmarshal(jsonPayload, &subscribed); err != nil {
				t.Fatal("JSON subscription is not valid JSON")
			}
			local := subscribed["inbounds"].([]any)[0].(map[string]any)
			local["port"] = clientPort
			subscribed["inbounds"], subscribed["log"] = []any{local}, map[string]string{"loglevel": "none"}
			subscribedProxy := subscribed["outbounds"].([]any)[0].(map[string]any)
			if tlsSettings, ok := subscribedProxy["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any); ok {
				tlsSettings["disableSystemRoot"] = true
				tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
			}
			subscribedData, _ := json.Marshal(subscribed)
			subscribedFile := filepath.Join(t.TempDir(), "subscription.json")
			if err := os.WriteFile(subscribedFile, subscribedData, 0600); err != nil {
				t.Fatal(err)
			}
			if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: subscribedFile, Inbounds: 1}); err != nil {
				t.Fatal("official core rejected JSON subscription", err)
			}
			if err := roundTrip(); err != nil {
				t.Fatal("JSON subscription client round trip failed", err)
			}
			startClient(exported.Outbound)
			if err := roundTrip(); err != nil {
				t.Fatal("exported client round trip failed", err)
			}
			statsDeadline := time.Now().Add(3 * time.Second)
			for {
				rt.mu.Lock()
				err = rt.checkpointCurrent(context.Background())
				ledger, _, ledgerErr := rt.ledger(context.Background())
				rt.mu.Unlock()
				if err != nil || ledgerErr != nil {
					t.Fatal("official counters failed", err, ledgerErr)
				}
				if ledger.Totals["client-one"].Uplink > 0 && ledger.Totals["client-one"].Downlink > 0 {
					break
				}
				if time.Now().After(statsDeadline) {
					t.Fatal("official bidirectional user counters stayed zero")
				}
				time.Sleep(50 * time.Millisecond)
			}
			readCount := func() traffic.Count {
				t.Helper()
				rt.mu.Lock()
				defer rt.mu.Unlock()
				if err := rt.checkpointCurrent(context.Background()); err != nil {
					t.Fatal(err)
				}
				ledger, _, err := rt.ledger(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				return ledger.Totals["client-one"]
			}
			beforeUDP := readCount()
			checkUDP("udp-connection", true)
			udpDeadline := time.Now().Add(3 * time.Second)
			for {
				count := readCount()
				if count.Uplink > beforeUDP.Uplink && count.Downlink > beforeUDP.Downlink {
					break
				}
				if time.Now().After(udpDeadline) {
					t.Fatal("UDP echo bypassed bidirectional user accounting")
				}
				time.Sleep(50 * time.Millisecond)
			}
			// 每种官方变体都验证当前实例重启后原身份继续连接，用量不回退。
			beforeRestart := rt.engine.Status()
			restartHistory := rt.historySummary(context.Background())
			rt.mu.Lock()
			beforeLedger, _, beforeErr := rt.ledger(context.Background())
			rt.mu.Unlock()
			if beforeErr != nil {
				t.Fatal(beforeErr)
			}
			restart := map[string]any{"revision": restartHistory.Revision, "startedAt": beforeRestart.StartedAt, "confirmation": "确认重启当前配置"}
			request("POST", "/api/runtime/restart", restart, 200)
			if now := rt.engine.Status(); !now.Running || now.StartedAt.Equal(*beforeRestart.StartedAt) || now.Version != beforeRestart.Version || now.WorkspaceRevision != beforeRestart.WorkspaceRevision || now.SettingsRevision != beforeRestart.SettingsRevision {
				t.Fatal("restart changed applied version/revisions or did not restart")
			}
			var afterRestart connectionResult
			if json.Unmarshal(request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: operation.WorkspaceRevision}, 200), &afterRestart) != nil || afterRestart.Link != exported.Link {
				t.Fatal("restart changed original exported identity")
			}
			if preset == "hy2" {
				startClient(exported.Outbound) // 丢弃旧QUIC，非无缝自动重连验收。
			}
			if err := roundTrip(); err != nil {
				t.Fatal("original identity failed after restart", err)
			}
			checkUDP("udp-after-restart", true)
			rt.mu.Lock()
			checkpointErr := rt.checkpointCurrent(context.Background())
			afterLedger, _, ledgerErr := rt.ledger(context.Background())
			rt.mu.Unlock()
			if checkpointErr != nil || ledgerErr != nil || afterLedger.Totals["client-one"].Total() < beforeLedger.Totals["client-one"].Total() {
				t.Fatal("restart reduced cumulative traffic", checkpointErr, ledgerErr)
			}
			goodData, _ := json.Marshal(exported.Outbound)
			// 错误认证必须失败，不只做官方语法预检。
			if preset == "hy2" {
				stream["hysteriaSettings"].(map[string]any)["auth"] = "fictional-wrong-authentication"
			} else {
				exported.Outbound["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)["id"] = "00000000-0000-4000-8000-000000000001"
			}
			startClient(exported.Outbound)
			if roundTrip() == nil {
				t.Fatal("invalid credentials accepted")
			}
			checkUDP("udp-invalid-auth", false)
			var good map[string]any
			_ = json.Unmarshal(goodData, &good)
			startClient(good)
			if roundTrip() != nil {
				t.Fatal("valid credentials no longer work")
			}
			checkUDP("udp-restored-auth", true)
			draft.Clients[0].Enabled = false
			workspace, _ = json.Marshal(draft)
			// 直接写库模拟“已保存但未生效”的较新草稿（保存即生效失败时的状态）。
			if _, err := s.SaveDocument(context.Background(), "workspace", operation.WorkspaceRevision, workspace); err != nil {
				t.Fatal(err)
			}
			operation.WorkspaceRevision++
			request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: operation.WorkspaceRevision}, 409)
			// 五个变体重建运行控制器；较新的停用草稿不能冒充上次已应用配置。
			if err := rt.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewRuntime(m, s, runtimeRoot) // 不依赖活动面板TLS，使用已应用证书快照。
			if err != nil {
				t.Fatal(err)
			}
			rt = reopened
			handler = Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
			var last appliedSummary
			if json.Unmarshal(request("GET", "/api/runtime/history", nil, 200), &last) != nil || !last.Available || last.WorkspaceRevision != operation.WorkspaceRevision-1 || rt.engine.Status().Running {
				t.Fatal("history lost or controller auto-started")
			}
			resume := map[string]any{"revision": last.Revision, "confirmation": "确认恢复上次应用"}
			request("POST", "/api/runtime/resume-preflight", resume, 200)
			if rt.engine.Status().Running {
				t.Fatal("historical preflight started core")
			}
			request("POST", "/api/runtime/resume", resume, 200)
			if mihomoReality {
				resumed, _ := rt.engine.Current()
				data, err := os.ReadFile(resumed.File)
				if err != nil || !strings.Contains(string(data), `"minClientVer":"1.8.2"`) {
					t.Fatal("compatibility policy lost from historical applied snapshot")
				}
			}
			resumedMetrics := waitRuntimeMetrics(t, rt)
			if resumedMetrics.StartedAt.Equal(*metrics.StartedAt) {
				t.Fatal("恢复后复用了旧指标实例")
			}
			if status := rt.engine.Status(); !status.Running || status.WorkspaceRevision != operation.WorkspaceRevision-1 {
				t.Fatal("resume used disabled draft")
			}
			if preset == "hy2" {
				// 官方旧QUIC连接不即时感知关闭；重启同一身份客户端，另有自然断线专项。
				startClient(good)
			}
			if err := roundTrip(); err != nil {
				t.Fatal("original identity failed after controller recreation", err)
			}
			checkUDP("udp-after-history-resume", true)
			if doc, err := s.ReadDocument(context.Background(), "workspace"); err != nil || doc.Revision != operation.WorkspaceRevision || string(doc.Data) != string(workspace) {
				t.Fatal("resume changed newer disabled draft", err)
			}
			request("POST", "/api/runtime/apply", operation, 200)
			if roundTrip() == nil {
				t.Fatal("disabled client still reaches origin")
			}
			checkUDP("udp-disabled", false)
			if preset == "hy2" {
				listener, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", serverPort))
				if err != nil {
					t.Fatal("UDP listener survived disable")
				}
				_ = listener.Close()
			} else if listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", serverPort)); err != nil {
				t.Fatal("TCP listener survived disable")
			} else {
				_ = listener.Close()
			}
			request("POST", "/api/runtime/stop", map[string]any{}, 200)
			request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: operation.WorkspaceRevision}, 422)
			if rt.engine.Status().Running {
				t.Fatal("server survived stop")
			}
			if json.Unmarshal(request("GET", "/api/runtime/history", nil, 200), &last) != nil || last.Available || last.Inbounds != 0 || last.WorkspaceRevision != operation.WorkspaceRevision {
				t.Fatal("empty applied configuration retained recoverable old users")
			}
		})
	}
}
