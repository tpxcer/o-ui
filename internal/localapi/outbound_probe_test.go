package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

func probeDraft(t *testing.T, outbounds, target string) json.RawMessage {
	t.Helper()
	on, off := true, false
	data, err := json.Marshal(settingsDraft{FreedomStrategy: "AsIs", RoutingStrategy: "AsIs", ProbeURL: target, Statistics: &on, AccessLog: &off, DNSLog: &off, LogLevel: "warning", Rules: "[]", Outbounds: outbounds, Balancers: "[]", DNS: `{"servers":["localhost"]}`, Advanced: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOutboundProbeConfig(t *testing.T) {
	const direct = `{"tag":"direct","protocol":"freedom","settings":{}}`
	data := probeDraft(t, `[{"tag":"proxy","protocol":"http","proxySettings":{"tag":"direct"}},`+direct+`,{"tag":"unused","protocol":"vless","settings":{"reverse":{"tag":"bridge"}}}]`, "https://example.test:8443/check")
	var draft settingsDraft
	_ = json.Unmarshal(data, &draft)
	draft.Rules = `[{"type":"field","network":"tcp","outboundTag":"missing"}]`
	draft.Advanced = `{"observatory":{"subjectSelector":["unused"]},"transport":{}}`
	data, _ = json.Marshal(draft)
	before := string(data)
	config, target, err := outboundProbeConfig(data, "proxy", "/tmp/fixture.sock")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(config)
	if len(config["outbounds"].([]map[string]any)) != 2 || strings.Contains(string(encoded), "unused") || strings.Contains(string(encoded), "observatory") || config["stats"] != nil || config["policy"] != nil || target != draft.ProbeURL || string(data) != before {
		t.Fatal("unexpected probe isolation")
	}
	for _, check := range []struct{ name, outbounds, tag, target string }{
		{"missing", `[` + direct + `]`, "absent", "https://example.test/"},
		{"cycle", `[{"tag":"a","protocol":"http","proxySettings":{"tag":"b"}},{"tag":"b","protocol":"socks","streamSettings":{"sockopt":{"dialerProxy":"a"}}}]`, "a", "https://example.test/"},
		{"dependency missing", `[{"tag":"a","protocol":"http","proxySettings":{"tag":"missing"}}]`, "a", "https://example.test/"},
		{"reverse", `[{"tag":"a","protocol":"vless","settings":{"reverse":{"tag":"bridge"}}}]`, "a", "https://example.test/"},
		{"block", `[{"tag":"a","protocol":"blackhole"}]`, "a", "https://example.test/"},
		{"kernel interface", `[{"tag":"a","protocol":"wireguard","settings":{}}]`, "a", "https://example.test/"},
		{"userinfo", `[` + direct + `]`, "direct", "https://fixture:secret@example.test/"},
		{"port", `[` + direct + `]`, "direct", "http://example.test:65536/"},
	} {
		t.Run(check.name, func(t *testing.T) {
			if _, _, err := outboundProbeConfig(probeDraft(t, check.outbounds, check.target), check.tag, "/tmp/fixture.sock"); err == nil {
				t.Fatal("invalid probe accepted")
			}
		})
	}
}

func TestProbeHTTPRequest(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "oup-http-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "p.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/redirect":
			w.Header().Set("Location", "http://should-not-follow.test/")
			w.WriteHeader(302)
		case "/fail":
			w.WriteHeader(503)
		case "/slow":
			<-r.Context().Done()
		default:
			w.WriteHeader(204)
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	for _, path := range []string{"/ok", "/redirect", "/fail", "/slow"} {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		before := hits.Load()
		_, err := probeHTTPRequest(ctx, "http://fixture.test"+path, socket)
		cancel()
		if (err != nil) != (path == "/fail" || path == "/slow") || hits.Load() != before+1 {
			t.Fatal(path, "unexpected request outcome", err)
		}
	}
	tlsListener, err := net.Listen("unix", filepath.Join(directory, "tls.sock"))
	if err != nil {
		t.Fatal(err)
	}
	secure := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	secure.Listener.Close()
	secure.Listener = tlsListener
	secure.StartTLS()
	defer secure.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := probeHTTPRequest(ctx, "https://fixture.test/", filepath.Join(directory, "tls.sock")); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatal("untrusted target certificate accepted")
	}
}

func TestOutboundProbeBoundary(t *testing.T) {
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
	session, err := s.Initialize(context.Background(), "probe-tester", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{manager: m, done: make(chan struct{})}
	h := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	for _, check := range []struct {
		name, origin, body string
		auth               bool
		want               int
	}{
		{"unauthenticated", "http://127.0.0.1:5180", "{}", false, 401},
		{"cross origin", "https://evil.test", "{}", true, 403},
		{"bad payload", "http://127.0.0.1:5180", `{"data":null,"tag":"direct","unexpected":1}`, true, 400},
		{"missing core", "http://127.0.0.1:5180", `{"data":` + string(probeDraft(t, `[{"tag":"direct","protocol":"freedom"}]`, "https://example.test/")) + `,"tag":"direct"}`, true, 503},
	} {
		t.Run(check.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/xray/outbound-test", strings.NewReader(check.body))
			r.Host = "127.0.0.1:5180"
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Origin", check.origin)
			r.Header.Set("Content-Type", "application/json")
			if check.auth {
				r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != check.want {
				t.Fatalf("unexpected status %d", w.Code)
			}
		})
	}
	rt.probeMu.Lock()
	_, status, err := rt.probeOutbound(context.Background(), nil, "")
	rt.probeMu.Unlock()
	if status != 429 || err == nil {
		t.Fatal("concurrent test accepted")
	}
}

func TestOfficialOutboundProbe(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("requires explicit isolated official core")
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	version := runtimeQAVersion(t)
	if m.Selected() != version {
		t.Fatal("QA core selection mismatch")
	}
	binary, err := m.Binary(version)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt := &Runtime{manager: m, state: s, done: make(chan struct{})}
	defer rt.engine.Close()
	var hits, attempts atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(204) }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Method != "CONNECT" || r.Host != strings.TrimPrefix(origin.URL, "http://") || r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("fixture-user:fixture-pass")) {
			http.Error(w, "rejected", 407)
			return
		}
		upstream, e := net.DialTimeout("tcp", r.Host, time.Second)
		if e != nil {
			return
		}
		defer upstream.Close()
		conn, buffer, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_ = upstream.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if buffer.Flush() != nil {
			return
		}
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, buffer); close(done) }()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
		_ = upstream.Close()
		<-done
	}))
	defer proxy.Close()
	// 独立业务核心持续存活，测试不得替换它或写设置文档。
	file := filepath.Join(t.TempDir(), "business.json")
	if err = os.WriteFile(file, []byte(`{"outbounds":[{"protocol":"freedom"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = rt.engine.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}); err != nil {
		t.Fatal(err)
	}
	beforeEngine := rt.engine.Status()
	beforeDocuments, err := s.ReadDocuments(context.Background(), []string{"workspace", "xray-settings"})
	if err != nil {
		t.Fatal(err)
	}
	port := proxy.Listener.Addr().(*net.TCPAddr).Port
	for _, bad := range []bool{false, true} {
		password := "fixture-pass"
		if bad {
			password = "fixture-wrong"
		}
		outbounds, _ := json.Marshal([]any{map[string]any{"tag": "probe", "protocol": "http", "settings": map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{map[string]string{"user": "fixture-user", "pass": password}}}}}}, map[string]string{"tag": "direct", "protocol": "freedom"}})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		beforeHits, beforeAttempts := hits.Load(), attempts.Load()
		_, status, err := rt.probeOutbound(ctx, probeDraft(t, string(outbounds), origin.URL), "probe")
		cancel()
		if !bad && (err != nil || status != 200 || hits.Load() != beforeHits+1) {
			t.Fatal("real HTTP proxy probe failed", status, err)
		}
		if bad && (err == nil || hits.Load() != beforeHits) {
			t.Fatal("bad proxy authentication fell back to direct")
		}
		if attempts.Load() <= beforeAttempts {
			t.Fatal("probe did not try selected proxy")
		}
	}
	// 请求取消必须结束自有核心并清理临时配置，业务核心继续运行。
	directoriesBefore, _ := filepath.Glob("/tmp/oup-*")
	requested := make(chan struct{}, 1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requested <- struct{}{}; <-r.Context().Done() }))
	defer slow.Close()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_, _, _ = rt.probeOutbound(ctx, probeDraft(t, `[{"tag":"direct","protocol":"freedom"}]`, slow.URL), "direct")
	}()
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("cancel fixture did not reach target")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled probe did not stop")
	}
	directoriesAfter, _ := filepath.Glob("/tmp/oup-*")
	if !reflect.DeepEqual(directoriesBefore, directoriesAfter) {
		t.Fatal("probe left temporary configuration or socket behind")
	}
	afterDocuments, err := s.ReadDocuments(context.Background(), []string{"workspace", "xray-settings"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeEngine, rt.engine.Status()) {
		t.Fatalf("business engine changed: before=%+v after=%+v", beforeEngine, rt.engine.Status())
	}
	if !reflect.DeepEqual(beforeDocuments, afterDocuments) {
		t.Fatal("saved documents changed")
	}
	if m.Selected() != version {
		t.Fatal("selected core changed")
	}
}
