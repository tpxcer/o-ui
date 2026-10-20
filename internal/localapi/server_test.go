package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

func TestLocalBoundaryAndRequests(t *testing.T) {
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	session, err := s.Initialize(context.Background(), "tester", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180", "127.0.0.1:5178"})
	tests := []struct {
		name, method, path, remote, host, origin, contentType, body string
		want                                                        int
	}{
		{"status", "GET", "/api/core/status", "127.0.0.1:1234", "127.0.0.1:5180", "", "", "", 200},
		{"remote", "GET", "/api/core/status", "192.0.2.1:1234", "127.0.0.1:5180", "", "", "", 403},
		{"rebind", "GET", "/api/core/status", "127.0.0.1:1234", "evil.test", "", "", "", 403},
		{"foreign-origin", "POST", "/api/core/install", "127.0.0.1:1234", "127.0.0.1:5180", "https://evil.test", "application/json", `{}`, 403},
		{"missing-origin", "POST", "/api/core/install", "127.0.0.1:1234", "127.0.0.1:5180", "", "application/json", `{}`, 403},
		{"form-post", "POST", "/api/core/install", "127.0.0.1:1234", "127.0.0.1:5180", "http://127.0.0.1:5180", "text/plain", `{}`, 415},
		{"bad-version", "POST", "/api/core/install", "127.0.0.1:1234", "127.0.0.1:5180", "http://127.0.0.1:5180", "application/json", `{"version":"latest"}`, 400},
		{"unknown-field", "POST", "/api/core/install", "127.0.0.1:1234", "127.0.0.1:5180", "http://127.0.0.1:5180", "application/json", `{"version":"26.7.28","url":"https://evil.test"}`, 400},
		{"trailing-json", "POST", "/api/core/install", "127.0.0.1:1234", "127.0.0.1:5180", "http://127.0.0.1:5180", "application/json", `{"version":"26.7.28"}{}`, 400},
		{"vite-origin", "GET", "/api/core/status", "127.0.0.1:1234", "127.0.0.1:5178", "http://127.0.0.1:5178", "", "", 200},
		{"missing", "GET", "/api/not-found", "127.0.0.1:1234", "127.0.0.1:5180", "", "", "", 404},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			req.RemoteAddr, req.Host = test.remote, test.host
			req.Header.Set("Origin", test.origin)
			req.Header.Set("Content-Type", test.contentType)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != test.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("CORS should not be enabled")
			}
		})
	}
}

func TestAuthenticationAndSettingsAPI(t *testing.T) {
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"})
	request := func(method, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:5180"
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	origin := "http://127.0.0.1:5180"
	for _, path := range []string{"/api/core/status", "/api/core/releases", "/api/settings", "/api/workspace", "/api/runtime/status", "/api/runtime/traffic", "/api/runtime/metrics", "/api/logs", "/api/system/status", "/api/not-found"} {
		if w := request("GET", path, "", "", nil); w.Code != 401 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if w := request("POST", "/api/core/install", `{"version":"26.7.28"}`, origin, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/auth/setup", `{"username":"tester","password":"short"}`, origin, nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/auth/setup", `{}`, "https://evil.test", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/auth/setup", `{}`, "", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := request("POST", "/api/auth/setup", `{"username":"tester","password":"test-only-passphrase-924"}`, origin, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	for _, origin := range []string{"", "https://evil.test"} {
		if w := request("POST", "/api/runtime/connection", `{"clientId":"client-one","workspaceRevision":1}`, origin, cookie); w.Code != 403 {
			t.Fatal("connection export accepted wrong origin", w.Code)
		}
	}
	if w := request("POST", "/api/runtime/connection", `{}`, origin, nil); w.Code != 401 {
		t.Fatal("unauthenticated connection export", w.Code)
	}
	if cookie.Name != sessionCookie || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge != 43200 {
		t.Fatal("cookie protections missing")
	}
	if strings.Contains(w.Body.String(), cookie.Value) {
		t.Fatal("token in JSON")
	}
	if w = request("GET", "/api/auth/status", "", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("GET", "/api/core/status", "", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request("POST", "/api/auth/setup", `{"username":"tester","password":"test-only-passphrase-924"}`, origin, cookie); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w = request("GET", "/api/settings", "", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":0`) {
		t.Fatal(w.Code, w.Body.String())
	}
	workspaceBody, _ := json.Marshal(store.Document{Data: validWorkspace()})
	if w = request("PUT", "/api/workspace", string(workspaceBody), origin, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("GET", "/api/workspace", "", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":1`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("PUT", "/api/workspace", string(workspaceBody), origin, cookie); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("PUT", "/api/workspace", `{"revision":1,"data":{"inbounds":[],"clients":[]}}`, origin, cookie); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	draft := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/204", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": `[{"tag":"direct","protocol":"freedom"}]`, "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
	body, _ := json.Marshal(map[string]any{"revision": 0, "data": draft})
	if w = request("PUT", "/api/settings", string(body), origin, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("PUT", "/api/settings", string(body), origin, cookie); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("PUT", "/api/settings", `{"revision":1,"data":{}}`, origin, cookie); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("PUT", "/api/settings", `{"revision":1,"data":{}}{}`, origin, cookie); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w = request("POST", "/api/auth/logout", `{}`, origin, cookie); w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal(w.Code)
	}
	if w = request("GET", "/api/core/status", "", "", cookie); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w = request("POST", "/api/auth/login", `{"username":"tester","password":"incorrect"}`, origin, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w = request("POST", "/api/auth/login", `{"username":"tester","password":"test-only-passphrase-924"}`, origin, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
