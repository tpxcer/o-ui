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
	"sync"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 用官方核心验证在线IP统计与IP上限判定：两个客户端进程分别绑定本机两个非回环地址
// （PANEL_IPLIMIT_QA_SOURCES="地址1,地址2"，Xray不统计127.0.0.1与::1）连到面板生成的入站，
// 读取真实statsonlineiplist输出，上限1时应对后来的IP发起封禁。本机没有iptables，封禁调用由测试记录。
func TestOfficialOnlineIPLimit(t *testing.T) {
	root, sources := os.Getenv("PANEL_RUNTIME_QA_CORE"), strings.Split(os.Getenv("PANEL_IPLIMIT_QA_SOURCES"), ",")
	if root == "" || len(sources) != 2 || net.ParseIP(sources[0]) == nil || net.ParseIP(sources[1]) == nil {
		t.Skip("设置PANEL_RUNTIME_QA_CORE与PANEL_IPLIMIT_QA_SOURCES（两个本机非回环地址）后验证在线IP上限")
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
	for _, preset := range []string{"reality", "hy2"} {
		t.Run(preset, func(t *testing.T) {
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			cert, key := runtimeCertificate(t, t.TempDir(), "runtime-qa.test")
			var panelTLS *PanelTLS
			if preset == "hy2" {
				settings, _ := json.Marshal(panelTLSDraft{cert, key})
				if _, err := s.SaveDocument(context.Background(), "panel-tls", 0, settings); err != nil {
					t.Fatal(err)
				}
				if panelTLS, err = LoadPanelTLS(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			rt, err := NewRuntime(m, s, t.TempDir(), panelTLS)
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
			request("POST", "/api/auth/setup", map[string]string{"username": "iplimitqa", "password": "iplimit-test-only-passphrase"}, 200)
			pair, err := tls.LoadX509KeyPair(cert, key)
			if err != nil {
				t.Fatal(err)
			}
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "tls-target") }))
			target.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
			target.StartTLS()
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "iplimit-ok") }))
			defer origin.Close()
			_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
			var draft workspaceDraft
			_ = json.Unmarshal(validWorkspace(), &draft)
			port := runtimePort(t)
			draft.Inbounds[0].Preset, draft.Inbounds[0].Port, draft.Inbounds[0].Listen, draft.Inbounds[0].ShareHost = preset, port, sources[0], sources[0]
			draft.Inbounds[0].Connection = &connectionDraft{ServerName: "runtime-qa.test", Target: strings.TrimPrefix(target.URL, "https://"), UsePanelCertificate: preset == "hy2"}
			draft.Clients[0].IPLimit = 1
			workspace, _ := json.Marshal(draft)
			request("PUT", "/api/workspace", store.Document{Data: workspace}, 200)
			settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]",
				"outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, originPort), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
			settingsData, _ := json.Marshal(settings)
			var saved savedDocument
			_ = json.Unmarshal(request("PUT", "/api/settings", store.Document{Data: settingsData}, 200), &saved)
			if !saved.Applied {
				t.Fatal("a client with an IP limit must still be applied:", saved.ApplyError)
			}
			var exported connectionResult
			_ = json.Unmarshal(request("POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, 200), &exported)
			out := exported.Outbound
			if tlsSettings, ok := out["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any); ok {
				tlsSettings["disableSystemRoot"] = true
				tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
			}
			// 两个客户端各自绑定一个源地址，保持空闲长连接，使Xray把两个IP都算作在线。
			clients := []*http.Client{}
			for _, source := range sources {
				out["sendThrough"] = source
				localPort := runtimePort(t)
				data, _ := json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": localPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}, "outbounds": []any{out}})
				file := filepath.Join(t.TempDir(), "client.json")
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				e := &engine.Engine{}
				defer e.Close()
				if err := e.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}); err != nil {
					t.Fatal(err)
				}
				proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", localPort))
				transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), IdleConnTimeout: time.Minute}
				defer transport.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
				deadline := time.Now().Add(10 * time.Second)
				for {
					response, err := client.Get(origin.URL)
					if err == nil {
						body, _ := io.ReadAll(response.Body)
						response.Body.Close()
						if string(body) == "iplimit-ok" {
							break
						}
					}
					if time.Now().After(deadline) {
						t.Fatal("client bound to", source, "could not connect")
					}
					time.Sleep(100 * time.Millisecond)
				}
				clients = append(clients, client)
				time.Sleep(1100 * time.Millisecond) // 让两个IP的首次出现时间有先后
				rt.ipLimitTick(context.Background(), time.Now())
			}
			online, err := sampleOnlineIPs(context.Background(), rt)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, item := range online["client-client-one"] {
				got[item.IP] = true
			}
			if len(got) != 2 || !got[sources[0]] || !got[sources[1]] {
				t.Fatal("official online IP list", online)
			}
			var mu sync.Mutex
			calls := []string{}
			previous := runIPBan
			defer func() { runIPBan = previous }()
			runIPBan = func(_ context.Context, args ...string) (string, error) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, strings.Join(args, " "))
				return "ok", nil
			}
			rt.ipLimitTick(context.Background(), time.Now())
			protocol := map[string]string{"reality": "tcp", "hy2": "udp"}[preset]
			want := fmt.Sprintf("ban %s %d %s", sources[1], port, protocol)
			mu.Lock()
			defer mu.Unlock()
			found := false
			for _, call := range calls {
				found = found || call == want
				if strings.HasPrefix(call, "ban "+sources[0]) {
					t.Fatal("the first IP must be kept", calls)
				}
			}
			if !found {
				t.Fatal("the later IP should be banned on the inbound port", calls)
			}
			_ = clients
		})
	}
}
