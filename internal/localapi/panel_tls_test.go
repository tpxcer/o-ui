package localapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

func newTestTrust(p *PanelTLS) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.cert)
	return pool
}

func TestPanelCertificateReuseAndBackup(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cert, key := runtimeCertificate(t, t.TempDir())
	p, err := loadPanelPair(panelTLSDraft{cert, key})
	if err != nil {
		t.Fatal(err)
	}
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	draft.Inbounds[0].Preset = "xhttp-tls"
	draft.Inbounds[0].Connection = &connectionDraft{UsePanelCertificate: true, Path: "/", Mode: "auto"}
	draft.Inbounds[0].Sniffing.IPsExcluded = []string{"192.0.2.0/24"}
	draft.Inbounds[0].Sniffing.DomainsExcluded = []string{"domain:example.com"}
	data, _ := json.Marshal(draft)
	if _, err := s.SaveDocument(ctx, "workspace", 0, data); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{state: s, root: t.TempDir(), panelTLS: p}
	ids, err := rt.credentials(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Runtime{state: s}).capture(ctx); err == nil {
		t.Fatal("missing panel certificate silently omitted from backup")
	}
	b, _, err := rt.capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Certificates) != 1 || b.Workspace.Inbounds[0].Connection.UsePanelCertificate || b.Workspace.Inbounds[0].Connection.CertificateFile != "" {
		t.Fatal("backup retained source dependency instead of certificate")
	}
	if string(b.Certificates["in-one"].Certificate) != string(p.cert) || string(b.Certificates["in-one"].Key) != string(p.key) {
		t.Fatal("wrong certificate snapshot")
	}
	fresh, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	restoredRuntime := &Runtime{state: fresh, root: t.TempDir()}
	empty, documents, err := restoredRuntime.capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := restoredRuntime.restore(ctx, b, empty, documents, "test-only-backup-passphrase"); err != nil {
		t.Fatal("panel certificate backup restore", err)
	}
	document, err := fresh.ReadDocument(ctx, "workspace")
	var restored workspaceDraft
	if err != nil || json.Unmarshal(document.Data, &restored) != nil {
		t.Fatal("restored workspace", err)
	}
	if restored.Inbounds[0].Connection.UsePanelCertificate {
		t.Fatal("restored inbound depends on destination panel certificate")
	}
	if compiled, err := buildInbounds(restored, ids, nil, time.Now()); err != nil || len(compiled) != 1 {
		t.Fatal("restored certificate cannot compile without panel HTTPS", err)
	}
	// 已生效证书可复用，不读取已移走的私钥源文件。
	if err := os.Rename(key, key+".moved"); err != nil {
		t.Fatal(err)
	}
	draft.Inbounds[0].Connection.CertificateFile, draft.Inbounds[0].Connection.KeyFile = "/unwritten-panel-cert.pem", "/unwritten-panel-key.pem"
	compiled, err := buildInbounds(draft, ids, nil, time.Now(), p)
	if err != nil || len(compiled) != 1 || len(compiled[0].Sniffing.IPsExcluded) != 1 || len(compiled[0].Sniffing.DomainsExcluded) != 1 {
		t.Fatal("active certificate or sniffing lost", err)
	}
	if _, err := buildInbounds(draft, ids, nil, time.Now()); err == nil {
		t.Fatal("inactive panel certificate accepted")
	}
	compiled[0].Sniffing.IPsExcluded[0] = "192.0.2.2"
	if draft.Inbounds[0].Sniffing.IPsExcluded[0] != "192.0.2.0/24" {
		t.Fatal("sniffing source slice shared")
	}
	if config.ValidateSniffing(compiled[0].Sniffing) != nil {
		t.Fatal("sniffing output invalid")
	}
	draft.Inbounds[0].Connection.ServerName = "wrong.test"
	if _, err := buildInbounds(draft, ids, nil, time.Now(), p); err == nil {
		t.Fatal("mismatched active panel certificate accepted")
	}
	draft.Inbounds[0].Enabled = false
	if compiled, err := buildInbounds(draft, ids, nil, time.Now()); err != nil || len(compiled) != 0 {
		t.Fatal("disabled inbound required panel certificate", err)
	}
	draft.Inbounds[0].Enabled = true
	draft.Clients[0].Enabled = false
	if compiled, err := buildInbounds(draft, ids, nil, time.Now()); err != nil || len(compiled) != 0 {
		t.Fatal("empty inbound required panel certificate", err)
	}
	wildCert, wildKey := runtimeCertificate(t, t.TempDir(), "*.example.test")
	wild, err := loadPanelPair(panelTLSDraft{wildCert, wildKey})
	if err != nil {
		t.Fatal(err)
	}
	draft.Clients[0].Enabled = true
	draft.Inbounds[0].Connection.ServerName = ""
	if _, err := buildInbounds(draft, ids, nil, time.Now(), wild); err == nil {
		t.Fatal("wildcard certificate silently invented service name")
	}
	draft.Inbounds[0].Connection.ServerName = "node.example.test"
	if compiled, err := buildInbounds(draft, ids, nil, time.Now(), wild); err != nil || len(compiled) != 1 {
		t.Fatal("explicit matching wildcard service name rejected", err)
	}
}

func TestPanelTLSSettingsAndSecureBoundary(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	p, err := LoadPanelTLS(ctx, s)
	if err != nil || p != nil {
		t.Fatal("default must be HTTP", err)
	}
	handler := Handler(m, s, t.TempDir(), []string{"localhost:5180"}, rt)
	request := func(method, path, body, origin string, secure bool, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "localhost:5180"
		r.RemoteAddr = "127.0.0.1:1234"
		if secure {
			r.TLS = &tls.ConnectionState{}
		}
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/panel/tls", "", "", false, nil); w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("TLS status must require auth")
	}
	w := request("POST", "/api/auth/setup", `{"username":"tls-test","password":"test-only-passphrase-924"}`, "https://localhost:5180", true, nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("TLS setup", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("HTTPS cookie protection missing")
	}
	for _, origin := range []string{"http://localhost:5180", "https://evil.test", ""} {
		if request("PUT", "/api/panel/tls", `{}`, origin, true, cookie).Code != 403 {
			t.Fatal("TLS origin was not strict")
		}
	}
	cert, key := runtimeCertificate(t, t.TempDir())
	body, _ := json.Marshal(map[string]any{"revision": 0, "data": panelTLSDraft{cert, key}})
	w = request("PUT", "/api/panel/tls", string(body), "https://localhost:5180", true, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active":false`) || strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Fatal("save must remain inactive and private", w.Code)
	}
	if request("PUT", "/api/panel/tls", string(body), "https://localhost:5180", true, cookie).Code != 409 {
		t.Fatal("stale overwrite accepted")
	}
	p, err = LoadPanelTLS(ctx, s)
	if err != nil || p == nil || len(p.names) != 1 || p.names[0] != "runtime-qa.test" {
		t.Fatal("restart load", err)
	}
	// 真实TLS握手使用显式本机测试CA，不跳过验证。
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	server.TLS = p.Config()
	server.StartTLS()
	defer server.Close()
	pool := newTestTrust(p)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "runtime-qa.test", MinVersion: tls.VersionTLS12}}, Timeout: time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal("verified HTTPS handshake", err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("HTTPS response")
	}
	// 源文件移走后已加载快照仍可复用，下一次启动则拒绝静默降级。
	if err := os.Rename(key, key+".moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPanelTLS(ctx, s); err == nil {
		t.Fatal("missing TLS must fail closed")
	}
	w = request("POST", "/api/auth/logout", `{}`, "https://localhost:5180", true, cookie)
	if w.Code != 200 || !w.Result().Cookies()[0].Secure || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("TLS logout cookie")
	}
}
