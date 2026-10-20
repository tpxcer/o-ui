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
	"sync/atomic"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 两个仅允许精确回环目标的CONNECT服务区分实际出口，不访问外站或启动观测。
func TestOfficialBalancerRoutes(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证真实均衡分配")
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
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	var hits, aHits, bHits, deadHits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "balancer-ok")
	}))
	defer origin.Close()
	destination := strings.TrimPrefix(origin.URL, "http://")
	proxy := func(counter *atomic.Int64, broken bool) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "CONNECT" || r.Host != destination {
				http.Error(w, "blocked", http.StatusForbidden)
				return
			}
			counter.Add(1)
			if broken {
				http.Error(w, "test unavailable", http.StatusBadGateway)
				return
			}
			upstream, err := net.DialTimeout("tcp", destination, time.Second)
			if err != nil {
				http.Error(w, "test unavailable", http.StatusBadGateway)
				return
			}
			defer upstream.Close()
			connection, buffer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(6 * time.Second))
			_ = upstream.SetDeadline(time.Now().Add(6 * time.Second))
			_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			if buffer.Flush() != nil {
				return
			}
			done := make(chan struct{})
			go func() { _, _ = io.Copy(upstream, buffer); close(done) }()
			_, _ = io.Copy(connection, upstream)
			_ = connection.Close()
			_ = upstream.Close()
			<-done
		}))
		t.Cleanup(server.Close)
		return server
	}
	a, b, dead := proxy(&aHits, false), proxy(&bHits, false), proxy(&deadHits, true)
	portOf := func(server *httptest.Server) int {
		return server.Listener.Addr().(*net.TCPAddr).Port
	}
	cert, key := runtimeCertificate(t, t.TempDir())
	var workspace workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &workspace)
	workspace.Inbounds[0].Preset = "xhttp-tls"
	workspace.Inbounds[0].Port = runtimePort(t)
	workspace.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/balancer-test", Mode: "auto"}
	data, _ := json.Marshal(workspace)
	if _, err := s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	settings := settingsDraft{FreedomStrategy: "AsIs", RoutingStrategy: "AsIs", ProbeURL: "https://example.test/", LogLevel: "warning", Statistics: &on, AccessLog: &off, DNSLog: &off, DNS: `{"servers":["localhost"]}`, Advanced: "{}"}
	settings.Outbounds = fmt.Sprintf(`[{"tag":"block","protocol":"blackhole"},{"tag":"pool-a","protocol":"http","settings":{"servers":[{"address":"127.0.0.1","port":%d}]}},{"tag":"pool-b","protocol":"http","settings":{"servers":[{"address":"127.0.0.1","port":%d}]}},{"tag":"broken","protocol":"http","settings":{"servers":[{"address":"127.0.0.1","port":%d}]}}]`, portOf(a), portOf(b), portOf(dead))
	var revision int64
	for _, check := range []struct {
		name, strategy, prefix string
		used, allowed          bool
		requests               int
	}{
		{"default-random", "", "pool-", true, true, 8},
		{"explicit-random", "random", "pool-", true, true, 8},
		{"round-robin", "roundRobin", "pool-", true, true, 6},
		{"prefix-excludes-other", "roundRobin", "pool-a", true, true, 4},
		{"unused-pool-does-not-route", "roundRobin", "pool-", false, false, 1},
		{"failed-outbound-no-direct-fallback", "roundRobin", "broken", true, false, 1},
	} {
		t.Run(check.name, func(t *testing.T) {
			balancer := map[string]any{"tag": "pool", "selector": []string{check.prefix}}
			if check.strategy != "" {
				balancer["strategy"] = map[string]string{"type": check.strategy}
			}
			encoded, _ := json.Marshal([]any{balancer})
			settings.Balancers = string(encoded)
			settings.Rules = "[]"
			if check.used {
				settings.Rules = `[{"type":"field","network":"tcp","balancerTag":"pool"}]`
			}
			encoded, _ = json.Marshal(settings)
			doc, err := s.SaveDocument(context.Background(), "xray-settings", revision, encoded)
			if err != nil {
				t.Fatal(err)
			}
			revision = doc.Revision
			rt.mu.Lock()
			candidate, applyErr := rt.candidate(context.Background(), runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: revision})
			if applyErr == nil {
				applyErr = rt.apply(context.Background(), candidate)
			}
			exported, exportErr := rt.connection(context.Background(), connectionRequest{ClientID: "client-one", WorkspaceRevision: 1})
			rt.mu.Unlock()
			if applyErr != nil || exportErr != nil {
				t.Fatal("apply/export failed", applyErr, exportErr)
			}
			tlsSettings := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
			tlsSettings["disableSystemRoot"] = true
			tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
			port := runtimePort(t)
			clientConfig := map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}}
			file := filepath.Join(t.TempDir(), "client.json")
			encoded, _ = json.Marshal(clientConfig)
			if err := os.WriteFile(file, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			clientEngine := &engine.Engine{}
			defer clientEngine.Close()
			if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}); err != nil {
				t.Fatal(err)
			}
			proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			last := ""
			for index := 0; index < check.requests; index++ {
				before, beforeA, beforeB, beforeDead := hits.Load(), aHits.Load(), bHits.Load(), deadHits.Load()
				response, requestErr := client.Get(origin.URL)
				var body []byte
				if response != nil {
					body, _ = io.ReadAll(response.Body)
					_ = response.Body.Close()
				}
				if !check.allowed {
					if requestErr == nil || hits.Load() != before {
						t.Fatal("blocked/failed route reached origin")
					}
					if !check.used && (aHits.Load() != beforeA || bHits.Load() != beforeB || deadHits.Load() != beforeDead) {
						t.Fatal("unused pool received connection")
					}
					if check.used && (deadHits.Load() <= beforeDead || aHits.Load() != beforeA || bHits.Load() != beforeB) {
						t.Fatal("failed candidate was not tried exclusively")
					}
					continue
				}
				if requestErr != nil || string(body) != "balancer-ok" || hits.Load() != before+1 {
					t.Fatal("expected real origin response")
				}
				selected := ""
				if aHits.Load() == beforeA+1 && bHits.Load() == beforeB {
					selected = "a"
				}
				if bHits.Load() == beforeB+1 && aHits.Load() == beforeA {
					selected = "b"
				}
				if selected == "" || deadHits.Load() != beforeDead {
					t.Fatal("request escaped selected candidates")
				}
				if check.strategy == "roundRobin" && check.prefix == "pool-" && selected == last {
					t.Fatal("round robin did not alternate actual upstreams")
				}
				if check.prefix == "pool-a" && selected != "a" {
					t.Fatal("selector used nonmatching outbound")
				}
				last = selected
			}
		})
	}
}
