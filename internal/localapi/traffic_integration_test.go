package localapi

import (
	"context"
	"crypto/tls"
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
	"sync/atomic"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

// 仅在临时库/回环端口执行，验证限制断开已有连接而不只移除导出按钮。
func TestOfficialTrafficLimits(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证真实统计/到期/额度")
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
	for _, preset := range []string{"xhttp-tls", "reality", "hy2", "xhttp-reality"} {
		t.Run(preset, func(t *testing.T) {
			kinds := []string{"expiry", "quota"}
			if preset == "xhttp-tls" {
				kinds = append(kinds, "quota-db-failure", "expiry-during-download", "quota-during-download")
			}
			for _, kind := range kinds {
				t.Run(kind, func(t *testing.T) {
					quota := strings.HasPrefix(kind, "quota")
					dbRoot := t.TempDir()
					s, err := store.Open(dbRoot)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
					runtimeRoot := t.TempDir()
					rt, err := NewRuntime(m, s, runtimeRoot)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = rt.Close() }()
					cert, key := runtimeCertificate(t, t.TempDir())
					targetAddress := ""
					if preset == "reality" || preset == "xhttp-reality" {
						pair, err := tls.LoadX509KeyPair(cert, key)
						if err != nil {
							t.Fatal(err)
						}
						target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "local-reality-target") }))
						target.EnableHTTP2 = true
						target.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
						target.StartTLS()
						defer target.Close()
						targetAddress = strings.TrimPrefix(target.URL, "https://")
					}
					originClosed := make(chan struct{})
					origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/stream" {
							_, _ = io.WriteString(w, strings.Repeat("x", 32<<10))
							return
						}
						defer close(originClosed)
						flusher := w.(http.Flusher)
						w.WriteHeader(200)
						_, _ = io.WriteString(w, "x")
						flusher.Flush()
						ticker := time.NewTicker(50 * time.Millisecond)
						defer ticker.Stop()
						for {
							select {
							case <-r.Context().Done():
								return
							case <-ticker.C:
								if _, err := io.WriteString(w, "x"); err != nil {
									return
								}
								flusher.Flush()
							}
						}
					}))
					defer origin.Close()
					_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
					var draft workspaceDraft
					_ = json.Unmarshal(validWorkspace(), &draft)
					draft.Inbounds[0].Preset = preset
					draft.Inbounds[0].Port = runtimePort(t)
					if preset == "hy2" {
						listener, err := net.ListenPacket("udp", "127.0.0.1:0")
						if err != nil {
							t.Fatal(err)
						}
						draft.Inbounds[0].Port = listener.LocalAddr().(*net.UDPAddr).Port
						listener.Close()
					}
					draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Target: targetAddress, Path: "/local-test", Mode: "auto"}
					other := draft.Clients[0]
					other.ID = "other"
					other.Name = "other"
					draft.Clients = append(draft.Clients, other)
					if quota {
						draft.Clients[0].QuotaGB = 0.00001
					}
					data, _ := json.Marshal(draft)
					if _, err = s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
						t.Fatal(err)
					}
					settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, originPort), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
					data, _ = json.Marshal(settings)
					if _, err = s.SaveDocument(context.Background(), "xray-settings", 0, data); err != nil {
						t.Fatal(err)
					}
					op := runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}
					apply := func() {
						t.Helper()
						rt.mu.Lock()
						defer rt.mu.Unlock()
						if err := rt.checkpointCurrent(context.Background()); err != nil {
							t.Fatal(err)
						}
						c, err := rt.candidate(context.Background(), op)
						if err != nil {
							t.Fatal(err)
						}
						if err = rt.apply(context.Background(), c); err != nil {
							t.Fatal(err)
						}
					}
					apply()
					clients := map[string]*http.Client{}
					for _, id := range []string{"client-one", "other"} {
						rt.mu.Lock()
						out, err := rt.connection(context.Background(), connectionRequest{ClientID: id, WorkspaceRevision: 1})
						rt.mu.Unlock()
						if err != nil {
							t.Fatal(err)
						}
						if tlsSettings, ok := out.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any); ok {
							tlsSettings["disableSystemRoot"] = true
							tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
						}
						port := runtimePort(t)
						data, _ = json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{out.Outbound}})
						file := filepath.Join(t.TempDir(), "client.json")
						if err = os.WriteFile(file, data, 0600); err != nil {
							t.Fatal(err)
						}
						clientEngine := &engine.Engine{}
						defer clientEngine.Close()
						if err = clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, File: file, Inbounds: 1}); err != nil {
							t.Fatal(err)
						}
						proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
						transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
						defer transport.CloseIdleConnections()
						clients[id] = &http.Client{Transport: transport, Timeout: 2 * time.Second}
					}
					// 核心重启时已采集用量保留，空闲新实例不重复累计。
					response, err := clients["other"].Get(origin.URL)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					deadline := time.Now().Add(3 * time.Second)
					var before traffic.Ledger
					var ledgerErr error
					for {
						rt.mu.Lock()
						err = rt.checkpointCurrent(context.Background())
						before, _, ledgerErr = rt.ledger(context.Background())
						rt.mu.Unlock()
						if err != nil || ledgerErr != nil {
							t.Fatal("counter read failed", err, ledgerErr)
						}
						if before.Totals["other"].Downlink > 0 {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("counter stayed zero")
						}
						time.Sleep(50 * time.Millisecond)
					}
					apply()
					rt.mu.Lock()
					err = rt.checkpointCurrent(context.Background())
					after, _, ledgerErr := rt.ledger(context.Background())
					rt.mu.Unlock()
					if err != nil || ledgerErr != nil || before.Totals["other"] != after.Totals["other"] {
						t.Fatal("restart duplicated/lost checkpoint", err, ledgerErr)
					}
					if !quota {
						settings["statistics"] = false
						data, _ = json.Marshal(settings)
						if _, err = s.SaveDocument(context.Background(), "xray-settings", 1, data); err != nil {
							t.Fatal(err)
						}
						op.SettingsRevision = 2
						expiry := time.Now().Add(4 * time.Second).UnixMilli()
						draft.Clients[0].ExpiresAt = &expiry
						data, _ = json.Marshal(draft)
						if _, err = s.SaveDocument(context.Background(), "workspace", 1, data); err != nil {
							t.Fatal(err)
						}
						op.WorkspaceRevision = 2
						apply()
					}
					// 未应用草稿不能改变已应用额度/时限或出站。
					draft.Clients[0].QuotaGB = 0
					draft.Clients[0].ExpiresAt = nil
					data, _ = json.Marshal(draft)
					if _, err = s.SaveDocument(context.Background(), "workspace", op.WorkspaceRevision, data); err != nil {
						t.Fatal(err)
					}
					streamClient := *clients["client-one"]
					streamClient.Timeout = 0
					streamTimeout := 12 * time.Second
					if preset == "hy2" {
						streamTimeout = 50 * time.Second
					}
					streamCtx, cancel := context.WithTimeout(context.Background(), streamTimeout)
					defer cancel()
					req, _ := http.NewRequestWithContext(streamCtx, "GET", origin.URL+"/stream", nil)
					stream, err := streamClient.Do(req)
					if err != nil {
						t.Fatal("initial stream", err)
					}
					defer stream.Body.Close()
					closed := make(chan struct{})
					var received atomic.Int64
					go func() {
						defer close(closed)
						buffer := make([]byte, 4096)
						for {
							n, err := stream.Body.Read(buffer)
							received.Add(int64(n))
							if err != nil {
								return
							}
						}
					}()
					if strings.HasSuffix(kind, "-during-download") {
						session, err := s.Initialize(context.Background(), "downloadqa", "download-test-only-passphrase")
						if err != nil {
							t.Fatal(err)
						}
						handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
						started := blockCoreDownload(t)
						downloadCtx, cancelDownload := context.WithTimeout(context.Background(), time.Minute)
						downloadDone := make(chan int, 1)
						go func() {
							r := httptest.NewRequest("POST", "http://127.0.0.1:5180/api/core/install", strings.NewReader(`{"version":"99.1.1"}`)).WithContext(downloadCtx)
							r.RemoteAddr = "127.0.0.1:1234"
							r.Header.Set("Origin", "http://127.0.0.1:5180")
							r.Header.Set("Content-Type", "application/json")
							r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
							w := httptest.NewRecorder()
							handler.ServeHTTP(w, r)
							downloadDone <- w.Code
						}()
						defer func() {
							cancelDownload()
							if code := <-downloadDone; code != http.StatusBadGateway || m.Status().Busy || m.Selected() != version {
								t.Error("cancelled test download changed selection or kept busy")
							}
						}()
						select {
						case <-started:
						case <-time.After(3 * time.Second):
							t.Fatal("slow download did not start")
						}
					}
					if quota {
						response, err = clients["client-one"].Get(origin.URL)
						if err != nil {
							t.Fatal(err)
						}
						_, _ = io.Copy(io.Discard, response.Body)
						_ = response.Body.Close()
					}
					select {
					case <-originClosed:
					case <-time.After(10 * time.Second):
						t.Fatal("policy left upstream forwarding alive")
					}
					// 先验实际转发停止，不能把UDP对端尚未发现断线误判为继续传输。
					time.Sleep(250 * time.Millisecond)
					lastReceived := received.Load()
					time.Sleep(time.Second)
					if lastReceived == 0 || received.Load() != lastReceived || streamCtx.Err() != nil {
						t.Fatal("limited stream continued transferring or ended by test timeout")
					}
					rt.mu.Lock()
					current, running := rt.engine.Current()
					issuePresent := rt.issue != ""
					rt.mu.Unlock()
					var applied struct {
						Inbounds []config.CompiledInbound `json:"inbounds"`
					}
					data, readErr := os.ReadFile(current.File)
					if !running || issuePresent || readErr != nil || json.Unmarshal(data, &applied) != nil {
						t.Fatal("restriction reload did not preserve unlimited user")
					}
					for _, in := range applied.Inbounds {
						for _, user := range in.Settings.Clients {
							if user.Email == "client-client-one" {
								t.Fatal("limited user remained in running snapshot")
							}
						}
					}
					peerTimeout := 10 * time.Second
					if preset == "hy2" {
						// 官方26.7.28 Transport.Close不通知QUIC对端，默认空闲超时30秒。
						peerTimeout = 35 * time.Second
					}
					select {
					case <-closed:
						if streamCtx.Err() != nil {
							t.Fatal("stream ended by test timeout, not policy")
						}
					case <-time.After(peerTimeout):
						t.Fatal("client did not observe closed transport within protocol timeout")
					}
					if response, err = clients["client-one"].Get(origin.URL); err == nil {
						_ = response.Body.Close()
						t.Fatal("blocked user reconnected")
					}
					// 旧连接断开时新核心尚在启动，允许已说明的短暂重连窗口。
					until := time.Now().Add(5 * time.Second)
					for {
						response, err = clients["other"].Get(origin.URL)
						if err == nil || time.Now().After(until) {
							break
						}
						time.Sleep(100 * time.Millisecond)
					}
					if err != nil {
						t.Fatal("unlimited user cannot reconnect after reload", err)
					}
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					rt.mu.Lock()
					ledger, _, err := rt.ledger(context.Background())
					issue := rt.issue
					status := rt.engine.Status()
					rt.mu.Unlock()
					if err != nil || issue != "" || !status.Running {
						t.Fatal("guard failed rather than removed limited user", err, issue)
					}
					if quota && ledger.Totals["client-one"].Total() < 10_000 {
						t.Fatal("quota stopped before actual use")
					}
					if strings.HasSuffix(kind, "-during-download") && (!m.Status().Busy || m.Selected() != version) {
						t.Fatal("test download ended before live limit verification")
					}
					// 正常退出/数据库重开不丢累计；保存的未应用草稿不在执行过程中生效。
					if err = rt.Close(); err != nil {
						t.Fatal(err)
					}
					persisted, _, err := rt.ledger(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = store.Open(dbRoot)
					if err != nil {
						t.Fatal(err)
					}
					rt, err = NewRuntime(m, s, runtimeRoot)
					if err != nil {
						t.Fatal(err)
					}
					reopened, _, err := rt.ledger(context.Background())
					if err != nil || reopened.Totals["client-one"] != persisted.Totals["client-one"] || reopened.Totals["other"] != persisted.Totals["other"] {
						t.Fatal("database reopen lost totals", err)
					}
					// 重新应用原额度/到期策略，不得因进程与数据库重开让受限用户复活。
					if quota {
						draft.Clients[0].QuotaGB = 0.00001
					} else {
						expired := time.Now().Add(-time.Minute).UnixMilli()
						draft.Clients[0].ExpiresAt = &expired
					}
					data, _ = json.Marshal(draft)
					if _, err = s.SaveDocument(context.Background(), "workspace", op.WorkspaceRevision+1, data); err != nil {
						t.Fatal(err)
					}
					op.WorkspaceRevision += 2
					apply()
					rt.mu.Lock()
					_, exportErr := rt.connection(context.Background(), connectionRequest{ClientID: "client-one", WorkspaceRevision: op.WorkspaceRevision})
					rt.mu.Unlock()
					if exportErr == nil {
						t.Fatal("blocked user revived after reopen")
					}
					if quota {
						settings["statistics"] = false
						data, _ = json.Marshal(settings)
						if _, err = s.SaveDocument(context.Background(), "xray-settings", op.SettingsRevision, data); err != nil {
							t.Fatal(err)
						}
						op.SettingsRevision++
						rt.mu.Lock()
						_, candidateErr := rt.candidate(context.Background(), op)
						rt.mu.Unlock()
						// 统计常开：旧草稿的statistics=false被忽略，额度仍可执行。
						if candidateErr != nil {
							t.Fatal("statistics=false must not disable quota enforcement", candidateErr)
						}
						// 故障注入只作用于当前临时夹具，既验证CLI失败，也验证账本不可用。
						rt.mu.Lock()
						c, running := rt.engine.Current()
						if !running {
							t.Fatal("missing fixture core")
						}
						if kind == "quota-db-failure" {
							err = s.Close()
						} else {
							socket := strings.TrimPrefix(c.StatsAddress, "unix://")
							err = os.Rename(socket, socket+".unavailable")
						}
						rt.mu.Unlock()
						if err != nil {
							t.Fatal(err)
						}
						until := time.Now().Add(7 * time.Second)
						for rt.engine.Status().Running && time.Now().Before(until) {
							time.Sleep(100 * time.Millisecond)
						}
						rt.mu.Lock()
						failed, _, ledgerErr := rt.ledger(context.Background())
						issue := rt.issue
						rt.mu.Unlock()
						if kind == "quota-db-failure" {
							if rt.engine.Status().Running || ledgerErr == nil || issue == "" {
								t.Fatal("database failure did not fail closed", issue)
							}
							// 未能保存停止标志时，下次打开根据active检查点标记历史缺口。
							_ = rt.Close()
							s, err = store.Open(dbRoot)
							if err != nil {
								t.Fatal(err)
							}
							rt, err = NewRuntime(m, s, runtimeRoot)
							if err != nil {
								t.Fatal(err)
							}
							failed, _, err = rt.ledger(context.Background())
							if err != nil || !failed.Incomplete || failed.Totals["client-one"] != persisted.Totals["client-one"] {
								t.Fatal("database failure lost checkpoint", err)
							}
						} else if rt.engine.Status().Running || ledgerErr != nil || !failed.Incomplete || issue == "" {
							t.Fatal("collector failure did not fail closed", ledgerErr, issue)
						}
					}
				})
			}
		})
	}
}
