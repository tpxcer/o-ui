package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/monitor"
	"independent-panel/internal/store"
)

func TestSystemMonitorAPI(t *testing.T) {
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
	session, err := s.Initialize(context.Background(), "monitorqa", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	h := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	for _, v := range []struct {
		name, remote, host, origin string
		auth                       bool
		want                       int
	}{
		{"未登录", "127.0.0.1:1234", "127.0.0.1:5180", "", false, 401},
		{"外部", "192.0.2.1:1234", "127.0.0.1:5180", "", true, 403},
		{"跨源", "127.0.0.1:1234", "127.0.0.1:5180", "http://evil.test", true, 403},
		{"正常", "127.0.0.1:1234", "127.0.0.1:5180", "", true, 200},
	} {
		t.Run(v.name, func(t *testing.T) {
			for _, path := range []string{"/api/system/status", "/api/runtime/metrics"} {
				r := httptest.NewRequest("GET", path, nil)
				r.RemoteAddr = v.remote
				r.Host = v.host
				r.Header.Set("Origin", v.origin)
				if v.auth {
					r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != v.want {
					t.Fatal(w.Code)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("缺少no-store")
				}
				if path == "/api/runtime/metrics" && w.Code == 200 {
					var metrics engine.Metrics
					if json.Unmarshal(w.Body.Bytes(), &metrics) != nil || metrics.Running || metrics.Memory != nil || metrics.TCP != nil || len(metrics.Issues) == 0 {
						t.Fatal("停止状态不明确或保留假指标")
					}
				}
			}
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var v monitor.Snapshot
		r := httptest.NewRequest("GET", "/api/system/status", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Host = "127.0.0.1:5180"
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if !v.At.IsZero() {
			if v.Host == nil || v.History == nil {
				t.Fatal("无真实样本")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("采集未就绪")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitRuntimeMetrics(t *testing.T, rt *Runtime) engine.Metrics {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m := rt.metrics.Snapshot()
		status := rt.engine.Status()
		if m.Running && m.Memory != nil && m.Threads != nil && *m.Threads > 0 && m.TCP != nil && m.UDP != nil && m.StartedAt != nil && status.StartedAt != nil && m.StartedAt.Equal(*status.StartedAt) {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatal("自有官方核心指标不可用", m.Issues)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
