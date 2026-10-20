package localapi

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

func TestPasswordChangeAPI(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name, origin := "http", "http://127.0.0.1:5180"
		if secure {
			name, origin = "https", "https://127.0.0.1:5180"
		}
		t.Run(name, func(t *testing.T) {
			m, err := core.NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"})
			request := func(path, body, requestOrigin string, cookie *http.Cookie) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Host = "127.0.0.1:5180"
				r.RemoteAddr = "127.0.0.1:1234"
				if secure {
					r.TLS = &tls.ConnectionState{}
				}
				r.Header.Set("Origin", requestOrigin)
				r.Header.Set("Content-Type", "application/json")
				if cookie != nil {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			old := "test-only-passphrase-924"
			next := "different-test-only-passphrase-825"
			body, _ := json.Marshal(map[string]string{"currentPassword": old, "newPassword": next})
			if w := request("/api/auth/password", string(body), origin, nil); w.Code != 401 {
				t.Fatal("unauthenticated change", w.Code)
			}
			w := request("/api/auth/setup", `{"username":"tester","password":"test-only-passphrase-924"}`, origin, nil)
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			cookie := w.Result().Cookies()[0]
			w = request("/api/auth/login", `{"username":"tester","password":"test-only-passphrase-924"}`, origin, nil)
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			other := w.Result().Cookies()[0]
			for _, input := range []struct {
				body, origin string
				code         int
			}{
				{string(body), "https://evil.test", 403}, {string(body), "", 403},
				{`{"currentPassword":"test-only-passphrase-924","newPassword":"short"}`, origin, 400},
				{`{"currentPassword":"wrong-test-password","newPassword":"different-test-only-passphrase-825"}`, origin, 403},
				{`{"currentPassword":"test-only-passphrase-924","newPassword":"test-only-passphrase-924"}`, origin, 400},
				{string(body) + `{}`, origin, 400}, {`{"unexpected":"value"}`, origin, 400},
				{`{"newPassword":"` + strings.Repeat("a", 2100) + `"}`, origin, 400},
			} {
				w = request("/api/auth/password", input.body, input.origin, cookie)
				if w.Code != input.code {
					t.Fatalf("rejected change status=%d want=%d", w.Code, input.code)
				}
				if strings.Contains(w.Body.String(), old) || strings.Contains(w.Body.String(), next) {
					t.Fatal("error disclosed credential")
				}
				if len(w.Result().Cookies()) != 0 {
					t.Fatal("failed change revoked cookie")
				}
			}
			w = request("/api/auth/password", string(body), origin, cookie)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("change failed", w.Code)
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || cookies[0].MaxAge != -1 || cookies[0].Value != "" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Secure != secure {
				t.Fatal("cookie not cleared securely")
			}
			for _, session := range []*http.Cookie{cookie, other} {
				if w = request("/api/auth/password", string(body), origin, session); w.Code != 401 {
					t.Fatal("old login survived", w.Code)
				}
			}
			if w = request("/api/auth/login", `{"username":"tester","password":"test-only-passphrase-924"}`, origin, nil); w.Code != 401 {
				t.Fatal("old password accepted", w.Code)
			}
			login, _ := json.Marshal(map[string]string{"username": "tester", "password": next})
			if w = request("/api/auth/login", string(login), origin, nil); w.Code != 200 {
				t.Fatal("new login failed", w.Code)
			}
		})
	}
}

func TestPasswordChangeAPIRateLimit(t *testing.T) {
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	session, err := s.Initialize(t.Context(), "tester", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"})
	for i := 0; i < 6; i++ {
		r := httptest.NewRequest("POST", "/api/auth/password", strings.NewReader(`{"currentPassword":"wrong-test-password","newPassword":"different-test-only-passphrase-825"}`))
		r.Host = "127.0.0.1:5180"
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if i < 5 && w.Code != 403 {
			t.Fatal(w.Code)
		}
		if i == 5 && (w.Code != 429 || w.Header().Get("Retry-After") != "900") {
			t.Fatal("missing rate limit", w.Code)
		}
	}
}
