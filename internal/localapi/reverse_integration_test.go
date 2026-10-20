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
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 门户端为面板生成的配置：bridge客户端带反向代理标签r-home，user客户端的流量按路由送入r-home；默认出站为阻断，
// 只有经桥接端（独立Xray进程，VLESS简化写法出站带reverse）回到本机才能取到origin内容。桥接端停止后应失败。
func TestOfficialVLESSReverseProxy(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证VLESS反向代理")
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	version := runtimeQAVersion(t)
	binary, err := m.Binary(version)
	if err != nil {
		t.Fatal(err)
	}
	for _, preset := range []string{"reality", "xhttp-reality"} {
		t.Run(preset, func(t *testing.T) {
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			rt, err := NewRuntime(m, s, t.TempDir(), nil)
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
					t.Fatalf("%s: status %d want %d: %s", path, w.Code, want, w.Body.String())
				}
				if cookie == nil && len(w.Result().Cookies()) > 0 {
					cookie = w.Result().Cookies()[0]
				}
				return w.Body.Bytes()
			}
			request("POST", "/api/auth/setup", map[string]string{"username": "reverseqa", "password": "reverse-test-only-passphrase"}, 200)
			cert, key := runtimeCertificate(t, t.TempDir(), "runtime-qa.test")
			pair, err := tls.LoadX509KeyPair(cert, key)
			if err != nil {
				t.Fatal(err)
			}
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "tls-target") }))
			target.EnableHTTP2 = true
			target.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
			target.StartTLS()
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "reverse-ok") }))
			defer origin.Close()
			_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))

			var draft workspaceDraft
			_ = json.Unmarshal(validWorkspace(), &draft)
			draft.Inbounds[0].Preset, draft.Inbounds[0].Port = preset, runtimePort(t)
			draft.Inbounds[0].Connection = &connectionDraft{ServerName: "runtime-qa.test", Target: strings.TrimPrefix(target.URL, "https://"), Path: "/reverse-test", Mode: "auto"}
			bridge, user := draft.Clients[0], draft.Clients[0]
			bridge.ID, bridge.Name, bridge.ReverseTag = "bridge", "bridge", "r-home"
			user.ID, user.Name = "user", "user"
			draft.Clients = []clientDraft{bridge, user}
			workspace, _ := json.Marshal(draft)
			request("PUT", "/api/workspace", store.Document{Data: workspace}, 200)
			settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false,
				"rules":     `[{"type":"field","user":["client-user"],"outboundTag":"r-home"}]`,
				"outbounds": `[{"tag":"block","protocol":"blackhole","settings":{}},{"tag":"direct","protocol":"freedom","settings":{}}]`, "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
			settingsData, _ := json.Marshal(settings)
			var saved savedDocument
			_ = json.Unmarshal(request("PUT", "/api/settings", store.Document{Data: settingsData}, 200), &saved)
			if !saved.Applied {
				t.Fatal("portal config with a reverse route was not applied:", saved.ApplyError)
			}
			export := func(id string) map[string]any {
				var result connectionResult
				_ = json.Unmarshal(request("POST", "/api/runtime/connection", connectionRequest{ClientID: id, WorkspaceRevision: 1}, 200), &result)
				return result.Outbound
			}
			userOut, bridgeOut := export("user"), export("bridge")
			vnext := bridgeOut["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)
			account := vnext["users"].([]any)[0].(map[string]any)
			flat := map[string]any{"address": vnext["address"], "port": vnext["port"], "id": account["id"], "encryption": "none", "reverse": map[string]any{"tag": "r-bridge"}}
			if flow, ok := account["flow"]; ok {
				flat["flow"] = flow
			}
			bridgeOut["tag"], bridgeOut["settings"] = "portal", flat
			start := func(e *engine.Engine, config map[string]any) {
				t.Helper()
				data, _ := json.Marshal(config)
				file := filepath.Join(t.TempDir(), "client.json")
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := e.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}); err != nil {
					t.Fatal(err)
				}
			}
			bridgeEngine, userEngine := &engine.Engine{}, &engine.Engine{}
			defer bridgeEngine.Close()
			defer userEngine.Close()
			start(bridgeEngine, map[string]any{"log": map[string]string{"loglevel": "none"},
				"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": runtimePort(t), "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}},
				"outbounds": []any{bridgeOut, map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "network": "tcp", "ip": []string{"127.0.0.1/32"}, "port": originPort}}}}},
				"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"r-bridge"}, "outboundTag": "direct"}}}})
			userPort := runtimePort(t)
			start(userEngine, map[string]any{"log": map[string]string{"loglevel": "none"},
				"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": userPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}},
				"outbounds": []any{userOut}})
			proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", userPort))
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			fetch := func() bool {
				response, err := client.Get(origin.URL)
				if err != nil {
					return false
				}
				defer response.Body.Close()
				body, err := io.ReadAll(io.LimitReader(response.Body, 64))
				return err == nil && string(body) == "reverse-ok"
			}
			deadline := time.Now().Add(10 * time.Second)
			for !fetch() {
				if time.Now().After(deadline) {
					t.Fatal("traffic routed to the reverse tag did not come back through the bridge")
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err := bridgeEngine.Stop(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(500 * time.Millisecond)
			if fetch() {
				t.Fatal("reverse route still worked without the bridge; traffic bypassed the reverse tunnel")
			}
		})
	}
}
