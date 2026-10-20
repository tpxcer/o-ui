package localapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

type installTransport func(*http.Request) (*http.Response, error)

func (f installTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type pendingDownload struct {
	ctx     context.Context
	started chan struct{}
}

func (b *pendingDownload) Read([]byte) (int, error) {
	close(b.started)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (*pendingDownload) Close() error { return nil }

// 仅拦截测试进程的官方请求；不会连接GitHub或返回可执行文件。
func blockCoreDownload(t *testing.T) <-chan struct{} {
	t.Helper()
	started := make(chan struct{})
	asset := map[string]string{"darwin/arm64": "Xray-macos-arm64-v8a.zip", "darwin/amd64": "Xray-macos-64.zip", "linux/arm64": "Xray-linux-arm64-v8a.zip", "linux/amd64": "Xray-linux-64.zip"}[runtime.GOOS+"/"+runtime.GOARCH]
	if asset == "" {
		t.Fatal("unsupported test platform")
	}
	const version = "99.1.1"
	digest := sha256.Sum256([]byte("not-an-archive"))
	url := "https://github.com/XTLS/Xray-core/releases/download/v" + version + "/" + asset
	metadata, err := json.Marshal(map[string]any{"tag_name": "v" + version, "html_url": "https://github.com/XTLS/Xray-core/releases/tag/v" + version,
		"assets": []any{map[string]any{"name": asset, "size": 14, "digest": "sha256:" + hex.EncodeToString(digest[:]), "browser_download_url": url}}})
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	http.DefaultTransport = installTransport(func(r *http.Request) (*http.Response, error) {
		var body io.ReadCloser
		switch r.URL.String() {
		case "https://api.github.com/repos/XTLS/Xray-core/releases/tags/v" + version:
			body = io.NopCloser(strings.NewReader(string(metadata)))
		case url:
			body = &pendingDownload{ctx: r.Context(), started: started}
		default:
			return nil, errors.New("test refuses unexpected network request")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	return started
}

func TestCoreDownloadDoesNotHoldRuntimeLock(t *testing.T) {
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
	session, err := s.Initialize(context.Background(), "installqa", "install-test-only-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	request := func(ctx context.Context, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:5180"+path, strings.NewReader(body)).WithContext(ctx)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	started := blockCoreDownload(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(ctx, "/api/core/install", `{"version":"99.1.1"}`) }()
	defer func() {
		cancel()
		if w := <-done; w.Code != http.StatusBadGateway || m.Status().Busy || m.Selected() != core.DefaultVersion {
			t.Error("cancelled download changed selection or left install busy")
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("download did not start")
	}
	if !rt.mu.TryLock() {
		t.Fatal("slow core download holds runtime lock, blocking traffic guard and stop")
	}
	rt.mu.Unlock()
	if w := request(context.Background(), "/api/core/install", `{"version":"99.1.1"}`); w.Code != http.StatusConflict {
		t.Fatal("concurrent install not rejected", w.Code)
	}
	if w := request(context.Background(), "/api/runtime/stop", `{}`); w.Code != http.StatusOK {
		t.Fatal("stop unavailable during download", w.Code)
	}
	if !m.Status().Busy || m.Selected() != core.DefaultVersion {
		t.Fatal("pending install changed selection or lost busy state")
	}
}
