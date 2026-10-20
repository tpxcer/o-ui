package localapi

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 同一UDP关联跨过限制点；不能以TCP断开或HY2监听释放代替停止UDP转发。
func TestOfficialUDPTrafficLimits(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证真实UDP到期/额度")
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
			for _, kind := range []string{"expiry", "quota"} {
				t.Run(kind, func(t *testing.T) {
					ctx := context.Background()
					dbRoot, runtimeRoot := t.TempDir(), t.TempDir()
					s, err := store.Open(dbRoot)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
					rt, err := NewRuntime(m, s, runtimeRoot)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = rt.Close() }()
					cert, key := runtimeCertificate(t, t.TempDir())
					targetAddress := ""
					if strings.Contains(preset, "reality") {
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
					target, hits := runtimeUDPEcho(t)
					var draft workspaceDraft
					_ = json.Unmarshal(validWorkspace(), &draft)
					draft.Inbounds[0].Preset, draft.Inbounds[0].Port = preset, runtimePort(t)
					if preset == "hy2" {
						listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
						if err != nil {
							t.Fatal(err)
						}
						draft.Inbounds[0].Port = listener.LocalAddr().(*net.UDPAddr).Port
						_ = listener.Close()
					}
					draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Target: targetAddress, Path: "/local-test", Mode: "auto"}
					other := draft.Clients[0]
					other.ID, other.Name = "other", "other"
					draft.Clients = append(draft.Clients, other)
					data, _ := json.Marshal(draft)
					if _, err = s.SaveDocument(ctx, "workspace", 0, data); err != nil {
						t.Fatal(err)
					}
					settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"udp","ip":["127.0.0.1/32"],"port":"%d"}]}}]`, target.Port), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
					data, _ = json.Marshal(settings)
					if _, err = s.SaveDocument(ctx, "xray-settings", 0, data); err != nil {
						t.Fatal(err)
					}
					op := runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}
					apply := func() {
						t.Helper()
						rt.mu.Lock()
						defer rt.mu.Unlock()
						if err := rt.checkpointCurrent(ctx); err != nil {
							t.Fatal(err)
						}
						candidate, err := rt.candidate(ctx, op)
						if err == nil {
							err = rt.apply(ctx, candidate)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					apply()
					type clientFixture struct {
						process   *engine.Engine
						candidate engine.Candidate
						proxy     string
					}
					clients := map[string]clientFixture{}
					for _, id := range []string{"client-one", "other"} {
						rt.mu.Lock()
						exported, err := rt.connection(ctx, connectionRequest{ClientID: id, WorkspaceRevision: 1})
						rt.mu.Unlock()
						if err != nil {
							t.Fatal(err)
						}
						if tlsSettings, ok := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any); ok {
							tlsSettings["disableSystemRoot"] = true
							tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
						}
						port := runtimePort(t)
						data, _ = json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true}}}, "outbounds": []any{exported.Outbound}})
						file := filepath.Join(t.TempDir(), "client.json")
						if err := os.WriteFile(file, data, 0600); err != nil {
							t.Fatal(err)
						}
						process := &engine.Engine{}
						defer process.Close()
						candidate := engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}
						if err := process.Apply(ctx, candidate); err != nil {
							t.Fatal(err)
						}
						clients[id] = clientFixture{process, candidate, fmt.Sprintf("127.0.0.1:%d", port)}
					}
					if kind == "quota" {
						draft.Clients[0].QuotaGB = 0.00001
					} else {
						expiry := time.Now().Add(6 * time.Second).UnixMilli()
						draft.Clients[0].ExpiresAt = &expiry
						settings["statistics"] = false // 到期不能依赖开启流量统计。
						data, _ = json.Marshal(settings)
						if _, err := s.SaveDocument(ctx, "xray-settings", 1, data); err != nil {
							t.Fatal(err)
						}
						op.SettingsRevision = 2
					}
					appliedData, _ := json.Marshal(draft)
					if _, err := s.SaveDocument(ctx, "workspace", 1, appliedData); err != nil {
						t.Fatal(err)
					}
					op.WorkspaceRevision = 2
					apply()
					// 去掉草稿限制但不应用，不能影响正在执行的策略。
					draft.Clients[0].QuotaGB, draft.Clients[0].ExpiresAt = 0, nil
					data, _ = json.Marshal(draft)
					if _, err := s.SaveDocument(ctx, "workspace", 2, data); err != nil {
						t.Fatal(err)
					}
					active, err := openSOCKSUDP(clients["client-one"].proxy)
					if err != nil {
						t.Fatal(err)
					}
					defer active.Close()
					exchange := func(run func([]byte) error, allowed bool) {
						t.Helper()
						payload := make([]byte, 1200)
						if _, err := rand.Read(payload); err != nil {
							t.Fatal(err)
						}
						before := hits.Load()
						err := run(payload)
						if allowed {
							if err != nil || hits.Load() != before+1 {
								t.Fatal("UDP did not reach exact echo target", err)
							}
						} else if !errors.Is(err, errUDPNoResponse) || hits.Load() != before {
							t.Fatal("restricted UDP forwarded or association failed instead", err)
						}
					}
					activeExchange := func(payload []byte) error { return active.Exchange(target, payload) }
					freshExchange := func(id string) func([]byte) error {
						return func(payload []byte) error { return socksUDPExchange(clients[id].proxy, target, payload) }
					}
					exchange(activeExchange, true)
					exchange(freshExchange("other"), true)
					if kind == "quota" {
						for i := 0; i < 10; i++ {
							exchange(activeExchange, true)
						}
					}
					deadline := time.Now().Add(10 * time.Second)
					for {
						rt.mu.Lock()
						current, running := rt.engine.Current()
						issue := rt.issue
						rt.mu.Unlock()
						data, err := os.ReadFile(current.File)
						var applied struct {
							Inbounds []config.CompiledInbound `json:"inbounds"`
						}
						if !running || issue != "" || err != nil || json.Unmarshal(data, &applied) != nil {
							t.Fatal("guard failed rather than preserving unlimited user", err, issue)
						}
						limitedPresent, otherPresent := false, false
						for _, in := range applied.Inbounds {
							for _, user := range in.Settings.Clients {
								limitedPresent = limitedPresent || user.Email == "client-client-one"
								otherPresent = otherPresent || user.Email == "client-other"
							}
						}
						if !otherPresent {
							t.Fatal("restriction removed unlimited user")
						}
						if !limitedPresent {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("UDP user remained in applied configuration after limit")
						}
						time.Sleep(100 * time.Millisecond)
					}
					exchange(activeExchange, false)
					restartClient := func(id string) {
						t.Helper()
						client := clients[id]
						if err := client.process.Apply(ctx, client.candidate); err != nil {
							t.Fatal(err)
						}
					}
					// 丢弃旧QUIC再验原身份，不能把旧连接未感知关闭当成认证拒绝。
					restartClient("client-one")
					exchange(freshExchange("client-one"), false)
					restartClient("other")
					exchange(freshExchange("other"), true)
					rt.mu.Lock()
					err = rt.checkpointCurrent(ctx)
					ledger, _, ledgerErr := rt.ledger(ctx)
					rt.mu.Unlock()
					if err != nil || ledgerErr != nil || (kind == "quota" && (ledger.Totals["client-one"].Total() < 10_000 || ledger.Totals["other"].Uplink == 0 || ledger.Totals["other"].Downlink == 0)) {
						t.Fatal("UDP accounting missing or limit fired before actual use", err, ledgerErr)
					}
					if err := rt.Close(); err != nil {
						t.Fatal(err)
					}
					persisted, _, err := rt.ledger(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.Close(); err != nil {
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
					reopened, _, err := rt.ledger(ctx)
					if err != nil || rt.engine.Status().Running || reopened.Totals["client-one"] != persisted.Totals["client-one"] || reopened.Totals["other"] != persisted.Totals["other"] {
						t.Fatal("reopen auto-started or lost UDP totals", err)
					}
					if _, err := s.SaveDocument(ctx, "workspace", 3, appliedData); err != nil {
						t.Fatal(err)
					}
					op.WorkspaceRevision = 4
					apply()
					rt.mu.Lock()
					_, exportErr := rt.connection(ctx, connectionRequest{ClientID: "client-one", WorkspaceRevision: 4})
					rt.mu.Unlock()
					if exportErr == nil {
						t.Fatal("UDP limited user revived after database reopen")
					}
					restartClient("client-one")
					exchange(freshExchange("client-one"), false)
					restartClient("other")
					exchange(freshExchange("other"), true)
				})
			}
		})
	}
}
