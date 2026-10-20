package localapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/store"
)

func TestRestartAPIBoundaries(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt, err := NewRuntime(nil, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	_, doc := historyFixture(t, rt)
	session, err := s.Initialize(context.Background(), "restartqa", "restart-test-only-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(nil, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	body := fmt.Sprintf(`{"revision":%d,"startedAt":"2026-10-19T00:00:00Z","confirmation":"确认重启当前配置"}`, doc.Revision)
	for _, action := range []string{"restart", "restart-preflight"} {
		for _, item := range []struct {
			name, origin, contentType, body string
			auth                            bool
			want                            int
		}{
			{"anonymous", "http://127.0.0.1:5180", "application/json", body, false, 401},
			{"cross-origin", "https://evil.test", "application/json", body, true, 403},
			{"missing-origin", "", "application/json", body, true, 403},
			{"not-json", "http://127.0.0.1:5180", "text/plain", body, true, 415},
			{"unknown-field", "http://127.0.0.1:5180", "application/json", `{"extra":true}`, true, 400},
			{"trailing", "http://127.0.0.1:5180", "application/json", body + `{}`, true, 400},
			{"oversize", "http://127.0.0.1:5180", "application/json", `{"confirmation":"` + strings.Repeat("x", 600) + `"}`, true, 400},
			{"invalid-time", "http://127.0.0.1:5180", "application/json", `{"startedAt":"invalid"}`, true, 400},
			{"missing-instance", "http://127.0.0.1:5180", "application/json", `{"confirmation":"确认重启当前配置"}`, true, 409},
			{"stopped", "http://127.0.0.1:5180", "application/json", body, true, 409},
		} {
			t.Run(action+"/"+item.name, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5180/api/runtime/"+action, strings.NewReader(item.body))
				r.RemoteAddr = "127.0.0.1:1234"
				r.Header.Set("Origin", item.origin)
				r.Header.Set("Content-Type", item.contentType)
				if item.auth {
					r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != item.want || w.Header().Get("Cache-Control") != "no-store" || rt.engine.Status().Running {
					t.Fatal("restart boundary or unexpected start", w.Code, item.want)
				}
				if strings.Contains(w.Body.String(), rt.root) || strings.Contains(w.Body.String(), session.Token) {
					t.Fatal("restart leaked private state")
				}
			})
		}
	}
	if current, err := s.ReadDocument(context.Background(), appliedDocument); err != nil || current.Revision != doc.Revision || string(current.Data) != string(doc.Data) {
		t.Fatal("rejected restart modified history", err)
	}
	if _, err := rt.historyCandidate(context.Background(), doc.Revision, new(time.Time)); err != store.ErrConflict {
		t.Fatal("stopped runtime supplied restart candidate", err)
	}
}
