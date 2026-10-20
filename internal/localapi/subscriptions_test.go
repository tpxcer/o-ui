package localapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

func subscriptionFixture(t *testing.T) (*store.Store, *Runtime, http.Handler, *http.Cookie) {
	t.Helper()
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	session, err := s.Initialize(context.Background(), "subscriptionqa", "isolated-subscription-test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(strings.ReplaceAll(string(validWorkspace()), `"subscription":false`, `"subscription":true`))
	if _, err = s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
		t.Fatal(err)
	}
	return s, rt, Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt), &http.Cookie{Name: sessionCookie, Value: session.Token}
}

func subscriptionCall(t *testing.T, handler http.Handler, action string, request subscriptionRequest, cookie *http.Cookie, want int) subscriptionResult {
	t.Helper()
	data, _ := json.Marshal(request)
	r := httptest.NewRequest("POST", "/api/subscription/"+action, strings.NewReader(string(data)))
	r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:5180")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("subscription %s: %d want %d", action, w.Code, want)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable subscription management")
	}
	var result subscriptionResult
	if want == 200 && json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal("invalid result")
	}
	if action != "rotate" && result.Path != "" {
		t.Fatal("existing link recovered from digest")
	}
	return result
}

func subscriptionGET(t *testing.T, handler http.Handler, path string, want int) []byte {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("subscription GET: %d want %d", w.Code, want)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" || w.Header().Get("Location") != "" {
		t.Fatal("public subscription headers/redirect")
	}
	return w.Body.Bytes()
}

func TestSubscriptionManagement(t *testing.T) {
	s, rt, handler, cookie := subscriptionFixture(t)
	req := subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1}
	for _, action := range []string{"status", "rotate", "revoke"} {
		subscriptionCall(t, handler, action, req, nil, 401)
	}
	status := subscriptionCall(t, handler, "status", req, cookie, 200)
	if !status.Allowed || status.Active || status.Revision != 0 {
		t.Fatal("initial state")
	}
	subscriptionCall(t, handler, "rotate", req, cookie, 409)
	req.Confirm = true
	first := subscriptionCall(t, handler, "rotate", req, cookie, 200)
	secret, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(first.Path, "/sub/"))
	if err != nil || len(secret) != 32 || !first.Active || first.Revision != 1 {
		t.Fatal("invalid random token")
	}
	doc, _ := s.ReadDocument(context.Background(), subscriptionsDocument)
	token := strings.TrimPrefix(first.Path, "/sub/")
	hash := sha256.Sum256([]byte(token))
	if strings.Contains(string(doc.Data), token) || !strings.Contains(string(doc.Data), hex.EncodeToString(hash[:])) {
		t.Fatal("raw subscription secret persisted")
	}
	status = subscriptionCall(t, handler, "status", req, cookie, 200)
	if !status.Active || status.Revision != 1 || status.Path != "" {
		t.Fatal("status leak/mismatch")
	}
	subscriptionCall(t, handler, "rotate", req, cookie, 409)
	req.Revision = 1
	second := subscriptionCall(t, handler, "rotate", req, cookie, 200)
	if first.Path == second.Path {
		t.Fatal("token did not rotate")
	}
	subscriptionGET(t, handler, first.Path, 404)
	subscriptionGET(t, handler, second.Path, 503) // 无运行核心，不返回草稿中的身份。
	before, _ := s.ReadDocument(context.Background(), subscriptionsDocument)
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewRuntime(rt.manager, s, rt.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	handler = Handler(rt.manager, s, t.TempDir(), []string{"127.0.0.1:5180"}, reopened)
	after, _ := s.ReadDocument(context.Background(), subscriptionsDocument)
	if string(before.Data) != string(after.Data) || before.Revision != after.Revision {
		t.Fatal("reopen rotated authorization")
	}
	subscriptionGET(t, handler, first.Path, 404)
	subscriptionGET(t, handler, second.Path, 503)
	req.Revision = second.Revision
	revoked := subscriptionCall(t, handler, "revoke", req, cookie, 200)
	if revoked.Active || revoked.Revision != 3 {
		t.Fatal("not revoked")
	}
	subscriptionGET(t, handler, second.Path, 404)
	req.Revision = revoked.Revision
	again := subscriptionCall(t, handler, "revoke", req, cookie, 200)
	if again.Active || again.Revision != revoked.Revision {
		t.Fatal("revoke not idempotent")
	}
	logs, _ := json.Marshal(reopened.logs.Snapshot())
	if strings.Contains(string(logs), token) || strings.Contains(string(logs), "/sub/") {
		t.Fatal("subscription token in diagnostics")
	}
}

func TestSubscriptionBoundary(t *testing.T) {
	_, _, handler, cookie := subscriptionFixture(t)
	created := subscriptionCall(t, handler, "rotate", subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1, Confirm: true}, cookie, 200)
	for _, test := range []struct {
		name, method, path, remote, host, origin, site string
		want                                           int
	}{
		{"remote", "GET", created.Path, "192.0.2.1:1234", "127.0.0.1:5180", "", "", 403},
		{"rebind", "GET", created.Path, "127.0.0.1:1234", "evil.test", "", "", 403},
		{"origin", "GET", created.Path, "127.0.0.1:1234", "127.0.0.1:5180", "https://evil.test", "", 403},
		{"fetch-site", "GET", created.Path, "127.0.0.1:1234", "127.0.0.1:5180", "", "cross-site", 403},
		{"head", "HEAD", created.Path, "127.0.0.1:1234", "127.0.0.1:5180", "", "", 404},
		{"post", "POST", created.Path, "127.0.0.1:1234", "127.0.0.1:5180", "http://127.0.0.1:5180", "", 404},
		{"query", "GET", created.Path + "?format=json", "127.0.0.1:1234", "127.0.0.1:5180", "", "", 404},
		{"trailing", "GET", created.Path + "/", "127.0.0.1:1234", "127.0.0.1:5180", "", "", 404},
		{"double-slash", "GET", "/sub//" + strings.TrimPrefix(created.Path, "/sub/"), "127.0.0.1:1234", "127.0.0.1:5180", "", "", 404},
		{"short", "GET", "/sub/short", "127.0.0.1:1234", "127.0.0.1:5180", "", "", 404},
		{"unknown-format", "GET", "/sub/yaml/" + strings.TrimPrefix(created.Path, "/sub/"), "127.0.0.1:1234", "127.0.0.1:5180", "", "", 404},
		{"nested-format", "GET", "/sub/clash/json/" + strings.TrimPrefix(created.Path, "/sub/"), "127.0.0.1:1234", "127.0.0.1:5180", "", "", 404},
		{"format-remote", "GET", "/sub/clash/" + strings.TrimPrefix(created.Path, "/sub/"), "192.0.2.1:1234", "127.0.0.1:5180", "", "", 403},
		{"root", "GET", "/sub", "127.0.0.1:1234", "127.0.0.1:5180", "", "", 404},
		{"management-no-origin", "POST", "/api/subscription/rotate", "127.0.0.1:1234", "127.0.0.1:5180", "", "", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader("{}"))
			r.RemoteAddr, r.Host = test.remote, test.host
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Sec-Fetch-Site", test.site)
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), created.Path) {
				t.Fatalf("boundary status %d want %d", w.Code, test.want)
			}
		})
	}
}

func TestSubscriptionWorkspaceRevocationAtomic(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "option-off", true: "client-removed"}[remove], func(t *testing.T) {
			s, _, handler, cookie := subscriptionFixture(t)
			req := subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1, Confirm: true}
			created := subscriptionCall(t, handler, "rotate", req, cookie, 200)
			old, _ := s.ReadDocument(context.Background(), "workspace")
			var draft workspaceDraft
			_ = json.Unmarshal(old.Data, &draft)
			if remove {
				draft.Clients = []clientDraft{}
			} else {
				draft.Clients[0].Subscription = false
			}
			data, _ := json.Marshal(draft)
			before, _ := s.ReadDocument(context.Background(), subscriptionsDocument)
			stale := old
			stale.Revision = 0
			if _, err := saveSubscriptionWorkspace(context.Background(), s, stale, data); err != store.ErrConflict {
				t.Fatal("stale workspace accepted", err)
			}
			after, _ := s.ReadDocument(context.Background(), subscriptionsDocument)
			if string(before.Data) != string(after.Data) || before.Revision != after.Revision {
				t.Fatal("failed workspace save revoked links")
			}
			next, err := saveSubscriptionWorkspace(context.Background(), s, old, data)
			if err != nil {
				t.Fatal(err)
			}
			subscriptionGET(t, handler, created.Path, 404)
			if _, err = saveSubscriptionWorkspace(context.Background(), s, next, old.Data); err != nil {
				t.Fatal(err)
			}
			req.WorkspaceRevision = 3
			status := subscriptionCall(t, handler, "status", req, cookie, 200)
			if status.Active {
				t.Fatal("off/on or recreated ID resurrected old grant")
			}
			subscriptionGET(t, handler, created.Path, 404)
		})
	}
}

func TestSubscriptionStateValidationAndGeneration(t *testing.T) {
	valid := subscriptionState{Version: 1, Generation: 1, Clients: map[string]subscriptionToken{"client-one": {Hash: strings.Repeat("a", 64), Generation: 1}}}
	data, _ := json.Marshal(valid)
	for _, raw := range []string{"null", "{}", `{"version":1,"generation":1,"clients":null}`, strings.ReplaceAll(string(data), `"generation":1`, `"generation":2`) + "x", strings.ReplaceAll(string(data), strings.Repeat("a", 64), "short")} {
		if _, err := readSubscriptions(store.Document{Revision: 1, Data: json.RawMessage(raw)}); err == nil {
			t.Fatal("invalid persisted authorization accepted")
		}
	}
	next, err := invalidateSubscriptions(store.Document{Revision: 1, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	state, err := readSubscriptions(store.Document{Revision: 2, Data: next})
	if err != nil || state.Generation != 2 || state.Clients["client-one"].Generation != 1 {
		t.Fatal("generation not invalidated")
	}
	valid.Generation = math.MaxUint64
	data, _ = json.Marshal(valid)
	if _, err = invalidateSubscriptions(store.Document{Revision: 1, Data: data}); err == nil {
		t.Fatal("generation overflow")
	}
}

func TestSubscriptionConcurrentRotation(t *testing.T) {
	_, _, handler, cookie := subscriptionFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[int]int{}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, _ := json.Marshal(subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1, Confirm: true})
			r := httptest.NewRequest("POST", "/api/subscription/rotate", strings.NewReader(string(data)))
			r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
			r.Header.Set("Origin", "http://127.0.0.1:5180")
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			mu.Lock()
			counts[w.Code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("old confirmation rotated twice", counts)
	}
}

// Clash/JSON与Base64共用同一密钥；关闭某格式后该地址与无效链接一样返回404，开启时与Base64同样要求核心已应用运行。
func TestSubscriptionFormatsToggle(t *testing.T) {
	s, _, handler, cookie := subscriptionFixture(t)
	created := subscriptionCall(t, handler, "rotate", subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1, Confirm: true}, cookie, 200)
	if !created.Clash || !created.JSON {
		t.Fatal("formats should be enabled by default", created)
	}
	token := strings.TrimPrefix(created.Path, "/sub/")
	for _, path := range []string{created.Path, "/sub/clash/" + token, "/sub/json/" + token} {
		subscriptionGET(t, handler, path, 503)
	}
	settings := defaultSubscriptionSettings()
	settings.ClashEnabled, settings.JSONEnabled = false, false
	data, _ := json.Marshal(settings)
	if _, err := s.SaveDocument(context.Background(), subscriptionSettingsDocument, 0, data); err != nil {
		t.Fatal(err)
	}
	subscriptionGET(t, handler, created.Path, 503)
	subscriptionGET(t, handler, "/sub/clash/"+token, 404)
	subscriptionGET(t, handler, "/sub/json/"+token, 404)
	status := subscriptionCall(t, handler, "status", subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1}, cookie, 200)
	if status.Clash || status.JSON || status.Path != "" {
		t.Fatal("status should report disabled formats without a path", status)
	}
}
