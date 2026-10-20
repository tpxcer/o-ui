package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"independent-panel/internal/core"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
)

func TestAPIMessagesFollowRequestLanguage(t *testing.T) {
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
	session, err := s.Initialize(context.Background(), "langqa", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	h := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	call := func(path, lang string, auth bool) (int, map[string]any) {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:1234", "127.0.0.1:5180"
		if lang != "" {
			r.Header.Set(i18n.Header, lang)
		}
		if auth {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	for _, v := range []struct {
		path, lang string
		auth       bool
		code       int
		want       string
	}{
		{"/api/workspace", "", false, 401, "请先登录，或登录已过期"},
		{"/api/workspace", "en", false, 401, "Please sign in, or your session has expired"},
		{"/api/missing", "zh", true, 404, "接口不存在"},
		{"/api/missing", "en", true, 404, "API not found"},
	} {
		code, body := call(v.path, v.lang, v.auth)
		if code != v.code || body["error"] != v.want {
			t.Fatal(v, code, body)
		}
	}
	_, logs := call("/api/logs", "en", true)
	data, _ := json.Marshal(logs)
	if !strings.Contains(string(data), "o-ui started") || strings.Contains(string(data), "本轮已启动") {
		t.Fatal("诊断日志应按请求语言返回英文", string(data))
	}
	_, logs = call("/api/logs", "", true)
	data, _ = json.Marshal(logs)
	if !strings.Contains(string(data), "o-ui 本轮已启动") {
		t.Fatal("缺省语言应保持中文", string(data))
	}
}
