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

func TestListOrderPersistenceIsolationAndBoundary(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	session, err := s.Initialize(ctx, "tester", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace", "xray-settings", "runtime-applied", "firewall"} {
		if _, err = s.SaveDocument(ctx, name, 0, json.RawMessage(`{"unchanged":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"})
	request := func(method, kind, body string, authenticated bool, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/panel/list-order/"+kind, strings.NewReader(body))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, kind := range []string{"inbounds", "clients"} {
		w := request("GET", kind, "", true, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) {
			t.Fatal(w.Code, w.Body.String())
		}
		w = request("PUT", kind, `{"revision":0,"data":["b","a"]}`, true, "http://127.0.0.1:5180")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		w = request("PUT", kind, `{"revision":0,"data":["a","b"]}`, true, "http://127.0.0.1:5180")
		if w.Code != 409 {
			t.Fatal("stale update", w.Code)
		}
		for _, raw := range []string{`{"revision":1,"data":["a","a"]}`, `{"revision":1,"data":null}`, `{"revision":1,"data":[""]}`, `{"revision":-1,"data":[]}`, `{"revision":1,"data":[],"extra":true}`} {
			if w = request("PUT", kind, raw, true, "http://127.0.0.1:5180"); w.Code != 400 {
				t.Fatal("invalid accepted", w.Code, raw)
			}
		}
		if w = request("GET", kind, "", false, ""); w.Code != 401 {
			t.Fatal("auth", w.Code)
		}
		if w = request("PUT", kind, `{"revision":1,"data":[]}`, true, "https://evil.test"); w.Code != 403 {
			t.Fatal("origin", w.Code)
		}
		doc, err := s.ReadDocument(ctx, "list-order-"+kind)
		if err != nil || doc.Revision != 1 || string(doc.Data) != `["b","a"]` {
			t.Fatal("not persisted", doc, err)
		}
	}
	for _, name := range []string{"workspace", "xray-settings", "runtime-applied", "firewall"} {
		doc, err := s.ReadDocument(ctx, name)
		if err != nil || doc.Revision != 1 || string(doc.Data) != `{"unchanged":true}` {
			t.Fatal("business state changed", name, doc, err)
		}
	}
}
