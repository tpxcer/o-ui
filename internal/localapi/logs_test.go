package localapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"independent-panel/internal/core"
	"independent-panel/internal/diagnostics"
	"independent-panel/internal/store"
)

func TestLogsAPIBoundariesAndFixedOperations(t *testing.T) {
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
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	h := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	request := func(method, path, body, origin, remote string, cookie *http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:5180"
		r.RemoteAddr = remote
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("日志边界或状态不正确", w.Code)
		}
		return w
	}
	const origin = "http://127.0.0.1:5180"
	const remote = "127.0.0.1:12345"
	const sentinel = "fixture-only-passphrase-47192"
	request("GET", "/api/logs", "", "", remote, nil, 401)
	w := request("POST", "/api/auth/setup", `{"username":"fixtureadmin","password":"`+sentinel+`"}`, origin, remote, nil, 200)
	cookie := w.Result().Cookies()[0]
	before := rt.logs.Snapshot()
	for _, v := range []struct{ origin, remote string }{{"http://evil.test", remote}, {origin, "192.0.2.1:1234"}} {
		request("GET", "/api/logs", "", v.origin, v.remote, cookie, 403)
		request("PUT", "/api/settings", `{}`, v.origin, v.remote, cookie, 403)
	}
	if len(rt.logs.Snapshot().Entries) != len(before.Entries) {
		t.Fatal("外部拒绝写入日志")
	}
	request("PUT", "/api/settings?private="+sentinel, `{"private":"`+sentinel+`"}`, origin, remote, cookie, 400)
	request("POST", "/api/not-found?private="+sentinel, `{}`, origin, remote, cookie, 404)
	request("POST", "/api/auth/login", `{"username":"fixtureadmin","password":"wrong-`+sentinel+`"}`, origin, remote, nil, 401)
	w = request("GET", "/api/logs?private="+sentinel, "", origin, remote, cookie, 200)
	var snapshot diagnostics.Snapshot
	if json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || len(snapshot.Entries) != 4 || snapshot.Limit != 256 {
		t.Fatal("固定日志内容不正确")
	}
	if strings.Contains(w.Body.String(), sentinel) || strings.Contains(w.Body.String(), cookie.Value) || strings.Contains(w.Body.String(), "fixtureadmin") {
		t.Fatal("身份、认证或参数泄漏")
	}
	if snapshot.Entries[1].Code != "POST /api/auth/setup.ok" || snapshot.Entries[2].Level != "warning" || !strings.Contains(snapshot.Entries[2].Message, "HTTP 400") || !strings.Contains(snapshot.Entries[3].Message, "HTTP 401") {
		t.Fatal("操作结果未准确记录")
	}
	request("GET", "/api/logs", "", origin, remote, cookie, 200)
	if len(rt.logs.Snapshot().Entries) != len(snapshot.Entries) {
		t.Fatal("GET查询自身产生记录")
	}
	h = Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"})
	request("GET", "/api/logs", "", origin, remote, cookie, 503)
}
