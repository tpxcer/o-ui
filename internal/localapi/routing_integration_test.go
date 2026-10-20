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

// 只使用已校验官方核心和回环目标，验证表单同构JSON的真实匹配/顺序。
func TestOfficialRoutingRules(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证真实路由行为")
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
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "routing-ok")
	}))
	defer origin.Close()
	_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
	cert, key := runtimeCertificate(t, t.TempDir())
	var workspace workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &workspace)
	workspace.Inbounds[0].Preset = "xhttp-tls"
	workspace.Inbounds[0].Port = runtimePort(t)
	workspace.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/routing-test", Mode: "auto"}
	data, _ := json.Marshal(workspace)
	if _, err := s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
		t.Fatal(err)
	}
	settings := settingsDraft{FreedomStrategy: "AsIs", RoutingStrategy: "AsIs", ProbeURL: "https://example.test/", LogLevel: "warning", Balancers: "[]", DNS: `{"servers":["localhost"]}`, Advanced: "{}"}
	on, off := true, false
	settings.Statistics, settings.AccessLog, settings.DNSLog = &on, &off, &off
	// 默认阻断，只精确放行临时目标IP/端口；不关闭核心私网保护。
	settings.Outbounds = fmt.Sprintf(`[{"tag":"block","protocol":"blackhole"},{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, originPort)
	ipAllow := fmt.Sprintf(`{"type":"field","ip":["127.0.0.0/8"],"port":"%s","network":"tcp","outboundTag":"direct"}`, originPort)
	block := `{"type":"field","network":"tcp","outboundTag":"block"}`
	domainAllow := fmt.Sprintf(`{"type":"field","domain":["full:route-qa.test"],"port":"%s","network":"tcp","protocol":["http"],"outboundTag":"direct"}`, originPort)
	var revision int64
	for _, check := range []struct {
		name, rules, host string
		allowed           bool
	}{
		{"empty-default-block", "[]", "route-qa.test", false},
		{"ip-port-tcp-allow", "[" + ipAllow + "," + block + "]", "route-qa.test", true},
		{"first-match-block", "[" + block + "," + ipAllow + "]", "route-qa.test", false},
		{"domain-sniff-http-and-port", "[" + domainAllow + "]", "route-qa.test", true},
		{"different-domain-no-match", "[" + domainAllow + "]", "other-qa.test", false},
	} {
		t.Run(check.name, func(t *testing.T) {
			settings.Rules = check.rules
			data, _ := json.Marshal(settings)
			doc, err := s.SaveDocument(context.Background(), "xray-settings", revision, data)
			if err != nil {
				t.Fatal(err)
			}
			revision = doc.Revision
			rt.mu.Lock()
			candidate, err := rt.candidate(context.Background(), runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: revision})
			if err == nil {
				err = rt.apply(context.Background(), candidate)
			}
			exported, exportErr := rt.connection(context.Background(), connectionRequest{ClientID: "client-one", WorkspaceRevision: 1})
			rt.mu.Unlock()
			if err != nil || exportErr != nil {
				t.Fatal("apply/export failed", err, exportErr)
			}
			tlsSettings := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
			tlsSettings["disableSystemRoot"] = true
			tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
			port := runtimePort(t)
			clientConfig := map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}}
			file := filepath.Join(t.TempDir(), "client.json")
			data, _ = json.Marshal(clientConfig)
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			clientEngine := &engine.Engine{}
			defer clientEngine.Close()
			if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}); err != nil {
				t.Fatal(err)
			}
			proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			before := hits.Load()
			request, _ := http.NewRequest("GET", origin.URL, nil)
			request.Host = check.host
			response, requestErr := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Do(request)
			var body []byte
			if response != nil {
				body, _ = io.ReadAll(response.Body)
				_ = response.Body.Close()
			}
			if check.allowed {
				if requestErr != nil || string(body) != "routing-ok" || hits.Load() != before+1 {
					t.Fatal("expected allow did not reach exact loopback origin")
				}
			} else if requestErr == nil || hits.Load() != before {
				t.Fatal("blocked request reached origin or returned success")
			}
		})
	}
}
