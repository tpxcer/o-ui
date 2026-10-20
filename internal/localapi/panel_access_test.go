package localapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

func TestPanelAccessValidation(t *testing.T) {
	cert, key := runtimeCertificate(t, t.TempDir(), "panel-qa.test", "127.0.0.1", "::1")
	pair, err := loadPanelPair(panelTLSDraft{cert, key})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, listen, origin string
		public, tls, valid   bool
	}{
		{"default", "127.0.0.1:5180", "", false, false, true},
		{"local-ipv6", "[::1]:5180", "", false, true, true},
		{"wildcard-off", "0.0.0.0:5180", "", false, true, false},
		{"local-origin", "127.0.0.1:5180", "https://panel-qa.test:5180", false, true, false},
		{"public-domain", "0.0.0.0:5180", "https://panel-qa.test:5180", true, true, true},
		{"public-ip", "127.0.0.1:5180", "https://127.0.0.1:5180", true, true, true},
		{"public-ipv6", "[::]:5180", "https://[::1]:5180", true, true, true},
		{"public-no-tls", "0.0.0.0:5180", "https://panel-qa.test:5180", true, false, false},
		{"http", "0.0.0.0:5180", "http://panel-qa.test:5180", true, true, false},
		{"wrong-name", "0.0.0.0:5180", "https://other.test:5180", true, true, false},
		{"port-mismatch", "0.0.0.0:5180", "https://panel-qa.test", true, true, false},
		{"low-port", "0.0.0.0:443", "https://panel-qa.test", true, true, false},
		{"hostname-listen", "localhost:5180", "", false, true, false},
		{"mapped-ip", "[::ffff:127.0.0.1]:5180", "", false, true, false},
		{"multicast", "224.0.0.1:5180", "https://panel-qa.test:5180", true, true, false},
		{"userinfo", "0.0.0.0:5180", "https://user@panel-qa.test:5180", true, true, false},
		{"path", "0.0.0.0:5180", "https://panel-qa.test:5180/path", true, true, false},
		{"query", "0.0.0.0:5180", "https://panel-qa.test:5180?x=1", true, true, false},
		{"fragment", "0.0.0.0:5180", "https://panel-qa.test:5180#x", true, true, false},
		{"capital", "0.0.0.0:5180", "https://PANEL-QA.test:5180", true, true, false},
		{"zone", "[fe80::1%en0]:5180", "https://panel-qa.test:5180", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certificate := pair
			if !tc.tls {
				certificate = nil
			}
			if err := validatePanelAccess(PanelAccessDraft{Public: tc.public, Listen: tc.listen, Origin: tc.origin}, certificate); (err == nil) != tc.valid {
				t.Fatal("unexpected access validation", err)
			}
		})
	}
}

func TestPanelAccessStoredDocument(t *testing.T) {
	for _, data := range []string{`null`, `{}`, `{"public":false,"listen":"127.0.0.1:5190","origin":"","unknown":true}`} {
		t.Run(data, func(t *testing.T) {
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.SaveDocument(context.Background(), "panel-access", 0, json.RawMessage(data)); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadPanelAccess(context.Background(), s, "127.0.0.1:5180", nil); err == nil {
				t.Fatal("damaged persisted access fell back to default")
			}
		})
	}
}

func TestPanelAccessSettingsAndNetworkBoundary(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	active, err := LoadPanelAccess(ctx, s, "127.0.0.1:5180", nil)
	if err != nil || active.Data.Public || active.Revision != 0 {
		t.Fatal("default changed", err)
	}
	handler := PanelHandler(m, s, t.TempDir(), active, rt)
	call := func(method, path string, value any, host, remote, origin string, secure bool, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(value)
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		r.Host, r.RemoteAddr = host, remote
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		if secure {
			r.TLS = &tls.ConnectionState{}
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if strings.HasPrefix(path, "/api/") && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("API response cacheable")
		}
		return w
	}
	local := func(method, path string, value any, cookie *http.Cookie) *httptest.ResponseRecorder {
		return call(method, path, value, "127.0.0.1:5180", "127.0.0.1:1234", "http://127.0.0.1:5180", false, cookie)
	}
	if w := local("GET", "/api/panel/access", nil, nil); w.Code != 401 {
		t.Fatal("access settings did not require auth", w.Code)
	}
	cert, key := runtimeCertificate(t, t.TempDir(), "panel-qa.test")
	pair, err := loadPanelPair(panelTLSDraft{cert, key})
	if err != nil {
		t.Fatal(err)
	}
	draft := PanelAccessDraft{Public: true, Listen: "127.0.0.1:5180", Origin: "https://panel-qa.test:5180"}
	data, _ := json.Marshal(draft)
	if _, err := s.SaveDocument(ctx, "panel-access", 0, data); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPanelAccess(ctx, s, "127.0.0.1:5180", pair); err == nil {
		t.Fatal("uninitialized administrator exposed")
	}
	credentials := map[string]string{"username": "accessqa", "password": "test-only-access-passphrase"}
	w := local("POST", "/api/auth/setup", credentials, nil)
	if w.Code != 200 {
		t.Fatal("local administrator setup", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	request := map[string]any{"revision": 1, "data": draft, "confirmation": "确认启用HTTPS非本机访问"}
	if w := local("PUT", "/api/panel/access", request, cookie); w.Code != 400 {
		t.Fatal("public without saved certificate accepted", w.Code)
	}
	if w := local("PUT", "/api/panel/tls", map[string]any{"revision": 0, "data": panelTLSDraft{cert, key}}, cookie); w.Code != 200 {
		t.Fatal("save valid certificate", w.Code)
	}
	request["confirmation"] = ""
	if w := local("PUT", "/api/panel/access", request, cookie); w.Code != 409 {
		t.Fatal("public save without confirmation", w.Code)
	}
	request["confirmation"] = "确认启用HTTPS非本机访问"
	w = local("PUT", "/api/panel/access", request, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"activeRevision":0`) || !strings.Contains(w.Body.String(), `"revision":2`) || strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Fatal("save changed active boundary or leaked key", w.Code)
	}
	if w := local("PUT", "/api/panel/access", request, cookie); w.Code != 409 {
		t.Fatal("stale access overwrite accepted", w.Code)
	}
	if w := call("GET", "/api/auth/status", nil, "panel-qa.test:5180", "198.51.100.1:1234", "", true, nil); w.Code != 403 {
		t.Fatal("save changed live network boundary", w.Code)
	}
	loaded, err := LoadPanelAccess(ctx, s, "127.0.0.1:5180", pair)
	if err != nil || loaded.Revision != 2 || !loaded.Data.Public {
		t.Fatal("restart access snapshot", err)
	}
	handler = PanelHandler(m, s, t.TempDir(), loaded, rt)
	for _, tc := range []struct {
		name, method, path, host, remote, origin string
		secure                                   bool
		cookie                                   *http.Cookie
		code                                     int
	}{
		{"plain-forwarded", "GET", "/api/auth/status", "panel-qa.test:5180", "198.51.100.1:1234", "", false, nil, 403},
		{"wrong-host", "GET", "/api/auth/status", "other.test:5180", "198.51.100.1:1234", "", true, nil, 403},
		{"old-dev-host", "GET", "/api/auth/status", "127.0.0.1:5178", "127.0.0.1:1234", "", true, nil, 403},
		{"invalid-peer", "GET", "/api/auth/status", "panel-qa.test:5180", "invalid", "", true, nil, 403},
		{"public-status", "GET", "/api/auth/status", "panel-qa.test:5180", "198.51.100.1:1234", "", true, nil, 200},
		{"anonymous", "GET", "/api/workspace", "panel-qa.test:5180", "198.51.100.1:1234", "", true, nil, 401},
		{"authenticated", "GET", "/api/workspace", "panel-qa.test:5180", "198.51.100.1:1234", "", true, cookie, 200},
		{"cross-origin-read", "GET", "/api/workspace", "panel-qa.test:5180", "198.51.100.1:1234", "https://other.test:5180", true, cookie, 403},
		{"missing-write-origin", "POST", "/api/auth/logout", "panel-qa.test:5180", "198.51.100.1:1234", "", true, cookie, 403},
		{"wrong-write-origin", "POST", "/api/auth/logout", "panel-qa.test:5180", "198.51.100.1:1234", "http://panel-qa.test:5180", true, cookie, 403},
		{"public-setup", "POST", "/api/auth/setup", "panel-qa.test:5180", "198.51.100.1:1234", "https://panel-qa.test:5180", true, nil, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := call(tc.method, tc.path, nil, tc.host, tc.remote, tc.origin, tc.secure, tc.cookie); w.Code != tc.code {
				t.Fatal("network boundary mismatch", w.Code, tc.code)
			}
		})
	}
	w = call("POST", "/api/auth/login", credentials, "panel-qa.test:5180", "198.51.100.1:1234", "https://panel-qa.test:5180", true, nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure || w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("public secure login protection", w.Code)
	}
	wrongCert, wrongKey := runtimeCertificate(t, t.TempDir(), "other.test")
	if w := call("PUT", "/api/panel/tls", map[string]any{"revision": 1, "data": panelTLSDraft{wrongCert, wrongKey}}, "panel-qa.test:5180", "198.51.100.1:1234", "https://panel-qa.test:5180", true, cookie); w.Code != 400 {
		t.Fatal("mismatched saved access certificate accepted", w.Code)
	}
	if err := os.Rename(key, key+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPanelTLS(ctx, s); err == nil {
		t.Fatal("missing public TLS silently downgraded")
	}
}
