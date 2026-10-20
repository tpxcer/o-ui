package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

// 非回环候选只执行-test；唯一实际启动的代理监听固定127.0.0.1。
func TestOfficialProxyEndpointBoundaries(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后执行官方预检与回环导出边界验证")
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	version := runtimeQAVersion(t)
	if m.Selected() != version {
		t.Fatal("selected core does not match explicit QA version")
	}
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
	session, err := s.Initialize(context.Background(), "endpointqa", "isolated-endpoint-test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	call := func(method, path string, value any, want int) []byte {
		t.Helper()
		data, _ := json.Marshal(value)
		r := httptest.NewRequest(method, "http://127.0.0.1:5180/api/"+path, strings.NewReader(string(data)))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status %d want %d", path, w.Code, want)
		}
		return w.Body.Bytes()
	}
	draft := endpointDraft(t, "::", "2001:db8::10")
	draft.Clients[0].Subscription = true
	draft.Inbounds[0].Port = runtimePort(t)
	draft.Inbounds[0].Preset = "xhttp-tls"
	cert, key := runtimeCertificate(t, t.TempDir())
	draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/endpoint-test", Mode: "auto"}
	data, _ := json.Marshal(draft)
	call("PUT", "workspace", store.Document{Data: data}, 200)
	// 保存即生效（参照OUI）不再要求非回环确认；显式应用接口仍要求确认，停止后单独验证。
	call("POST", "runtime/stop", map[string]any{}, 200)
	op := runtimeRequest{Version: version, WorkspaceRevision: 1}
	call("POST", "runtime/preflight", op, 200)
	call("POST", "runtime/apply", op, 422)
	op.ProxyConfirmation = "确认重启当前配置"
	call("POST", "runtime/apply", op, 422)
	if rt.engine.Status().Running {
		t.Fatal("unconfirmed non-loopback started")
	}
	// 记录从未启动的候选，以验证历史入口同样要求额外确认。
	c, err := rt.candidate(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	record, err := rt.snapshot(c)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(record)
	current, err := s.ReadDocument(context.Background(), appliedDocument)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.SaveDocument(context.Background(), appliedDocument, current.Revision, data)
	if err != nil {
		t.Fatal(err)
	}
	if !rt.historySummary(context.Background()).NonLoopbackProxy {
		t.Fatal("history hides non-loopback scope")
	}
	call("POST", "runtime/resume-preflight", map[string]any{"revision": doc.Revision}, 200)
	call("POST", "runtime/resume", map[string]any{"revision": doc.Revision, "confirmation": "确认恢复上次应用"}, 422)
	if rt.engine.Status().Running {
		t.Fatal("history bypassed proxy confirmation")
	}
	// 同一分享主机保留，只有监听退回127.0.0.1后才允许实际启动。
	draft.Inbounds[0].Listen = "127.0.0.1"
	data, _ = json.Marshal(draft)
	call("PUT", "workspace", store.Document{Revision: 1, Data: data}, 200)
	op.WorkspaceRevision, op.ProxyConfirmation = 2, ""
	call("POST", "runtime/apply", op, 200)
	var exported connectionResult
	if json.Unmarshal(call("POST", "runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 2}, 200), &exported) != nil {
		t.Fatal("invalid export")
	}
	u, err := url.Parse(exported.Link)
	if err != nil || u.Hostname() != "2001:db8::10" || u.Query().Get("sni") != "runtime-qa.test" {
		t.Fatal("export conflated share address/SNI")
	}
	grant := subscriptionCall(t, handler, "rotate", subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 2, Confirm: true}, &http.Cookie{Name: sessionCookie, Value: session.Token}, 200)
	decoded, err := base64.StdEncoding.DecodeString(string(subscriptionGET(t, handler, grant.Path, 200)))
	if err != nil || !sameShareLink(string(decoded), exported.Link) {
		t.Fatal("subscription did not use same share address")
	}
	for _, path := range []string{"/api/runtime/status", "/api/workspace", grant.Path} {
		for _, remote := range []bool{false, true} {
			r := httptest.NewRequest("GET", "http://127.0.0.1:5180"+path, nil)
			r.RemoteAddr = "127.0.0.1:1234"
			if remote {
				r.RemoteAddr = "192.0.2.1:1234"
			} else {
				r.Host = "proxy.example.test"
			}
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal("proxy settings widened management/subscription boundary")
			}
		}
	}
	call("POST", "runtime/stop", struct{}{}, 200)
	if rt.engine.Status().Running {
		t.Fatal("test core not stopped")
	}
}
