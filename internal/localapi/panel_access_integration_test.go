package localapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 固定HTTPS来源的真实订阅/官方代理链路；所有监听与Dial仅限127.0.0.1。
func TestOfficialPublicHTTPSSubscription(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证回环HTTPS订阅与真实代理")
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	version := runtimeQAVersion(t)
	if m.Selected() != version {
		t.Fatal("explicit QA version mismatch")
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
	credentials := map[string]string{"username": "httpsqa", "password": "isolated-https-test-passphrase"}
	if _, err := s.Initialize(context.Background(), credentials["username"], credentials["password"]); err != nil {
		t.Fatal(err)
	}
	cert, key := runtimeCertificate(t, t.TempDir(), "panel-sub-qa.invalid", "127.0.0.1")
	settings, _ := json.Marshal(panelTLSDraft{cert, key})
	if _, err := s.SaveDocument(context.Background(), "panel-tls", 0, settings); err != nil {
		t.Fatal(err)
	}
	panelTLS, err := LoadPanelTLS(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(m, s, t.TempDir(), panelTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	server := httptest.NewUnstartedServer(nil)
	address := server.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	origin := "https://panel-sub-qa.invalid:" + port
	accessData, _ := json.Marshal(PanelAccessDraft{Public: true, Listen: address, Origin: origin})
	if _, err := s.SaveDocument(context.Background(), "panel-access", 0, accessData); err != nil {
		t.Fatal(err)
	}
	access, err := LoadPanelAccess(context.Background(), s, address, panelTLS)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler, server.TLS = PanelHandler(m, s, t.TempDir(), access, rt), panelTLS.Config()
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(panelTLS.cert) {
		t.Fatal("test CA unavailable")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != "panel-sub-qa.invalid:"+port {
			return nil, fmt.Errorf("unexpected test destination")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}}
	defer transport.CloseIdleConnections()
	jar, _ := cookiejar.New(nil)
	admin := &http.Client{Transport: transport, Jar: jar, Timeout: 10 * time.Second}
	anonymous := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	call := func(client *http.Client, method, path string, value any, host, requestOrigin string, want int) []byte {
		t.Helper()
		data, _ := json.Marshal(value)
		r, _ := http.NewRequest(method, origin+path, bytes.NewReader(data))
		r.Host = host
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("HTTPS fixture request failed")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != want || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("HTTPS fixture response mismatch", response.StatusCode, want)
		}
		return body
	}
	call(admin, "POST", "/api/auth/login", credentials, "", origin, 200)
	call(anonymous, "GET", "/api/workspace", nil, "", "", 401)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "https-subscription-proxy-ok") }))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	draft.Clients[0].Subscription = true
	draft.Inbounds[0].Preset, draft.Inbounds[0].Port = "xhttp-tls", runtimePort(t)
	draft.Inbounds[0].Connection = &connectionDraft{UsePanelCertificate: true, Path: "/https-sub-qa", Mode: "auto"}
	workspace, _ := json.Marshal(draft)
	call(admin, "PUT", "/api/workspace", store.Document{Data: workspace}, "", origin, 200)
	settings = json.RawMessage(fmt.Sprintf(`{"freedomStrategy":"AsIs","routingStrategy":"AsIs","probeURL":"https://example.test/","statistics":true,"accessLog":false,"dnsLog":false,"logLevel":"warning","rules":"[]","outbounds":%q,"balancers":"[]","dns":"{\"servers\":[\"localhost\"]}","advanced":"{}"}`, fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, targetPort)))
	call(admin, "PUT", "/api/settings", store.Document{Data: settings}, "", origin, 200)
	call(admin, "POST", "/api/runtime/apply", runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}, "", origin, 200)
	var exported connectionResult
	if json.Unmarshal(call(admin, "POST", "/api/runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, "", origin, 200), &exported) != nil {
		t.Fatal("export failed")
	}
	grantRequest := subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1, Confirm: true}
	var grant subscriptionResult
	if json.Unmarshal(call(admin, "POST", "/api/subscription/rotate", grantRequest, "", origin, 200), &grant) != nil {
		t.Fatal("grant failed")
	}
	decoded, err := base64.StdEncoding.DecodeString(string(call(anonymous, "GET", grant.Path, nil, "", "", 200)))
	if err != nil || string(decoded) != exported.Link+"\n" {
		t.Fatal("HTTPS subscription did not return the applied URI")
	}
	call(anonymous, "GET", grant.Path, nil, address, "", 403)
	call(anonymous, "GET", grant.Path, nil, "", "https://other.invalid", 403)
	stream := exported.Outbound["streamSettings"].(map[string]any)
	tlsSettings := stream["tlsSettings"].(map[string]any)
	tlsSettings["disableSystemRoot"], tlsSettings["certificates"] = true, []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
	clientPort := runtimePort(t)
	clientData, _ := json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}})
	file := filepath.Join(t.TempDir(), "client.json")
	if err := os.WriteFile(file, clientData, 0600); err != nil {
		t.Fatal(err)
	}
	clientEngine := &engine.Engine{}
	defer clientEngine.Close()
	if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}); err != nil {
		t.Fatal(err)
	}
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
	proxyTransport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer proxyTransport.CloseIdleConnections()
	proxyClient := &http.Client{Transport: proxyTransport, Timeout: 5 * time.Second}
	response, err := proxyClient.Get(target.URL)
	if err != nil {
		t.Fatal("HTTPS subscription corresponding official proxy failed")
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "https-subscription-proxy-ok" {
		t.Fatal("proxy response mismatch")
	}
	grantRequest.Revision = grant.Revision
	var rotated subscriptionResult
	if json.Unmarshal(call(admin, "POST", "/api/subscription/rotate", grantRequest, "", origin, 200), &rotated) != nil {
		t.Fatal("rotate failed")
	}
	call(anonymous, "GET", grant.Path, nil, "", "", 404)
	call(anonymous, "GET", rotated.Path, nil, "", "", 200)
	grantRequest.Revision = rotated.Revision
	call(admin, "POST", "/api/subscription/revoke", grantRequest, "", origin, 200)
	call(anonymous, "GET", rotated.Path, nil, "", "", 404)
	call(admin, "POST", "/api/runtime/stop", struct{}{}, "", origin, 200)
}
