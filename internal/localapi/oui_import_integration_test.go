package localapi

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

// 真实Xray验收：按OUI分享链接写法手写的旧客户端（不加流控、非首个shortId、第二个SNI、带Host的XHTTP、
// OUI生成的HY2认证值），连接从OUI数据库导入后的o-ui服务端；客户端与服务端流控不一致必须连不上。
func TestOUIImportedClientsConnect(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证从OUI导入的旧客户端连接")
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	version := runtimeQAVersion(t)
	binary, err := m.Binary(version)
	if err != nil {
		t.Fatal(err)
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
	defer func() { _ = rt.Close() }()
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	var cookie *http.Cookie
	request := func(method, path string, value any, want int) []byte {
		t.Helper()
		data, _ := json.Marshal(value)
		r := httptest.NewRequest(method, "http://127.0.0.1:5180"+path, strings.NewReader(string(data)))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: status %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		if cookie == nil && len(w.Result().Cookies()) > 0 {
			cookie = w.Result().Cookies()[0]
		}
		return w.Body.Bytes()
	}
	request("POST", "/api/auth/setup", map[string]string{"username": "ouiqa", "password": "oui-import-test-only-passphrase"}, 200)
	cert, key := runtimeCertificate(t, t.TempDir(), "runtime-qa.test", "www.runtime-qa.test")
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "tls-target") }))
	target.EnableHTTP2 = true
	target.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
	target.StartTLS()
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "oui-import-ok") }))
	defer origin.Close()
	_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
	realityPort, xhttpPort, clientPort := runtimePort(t), runtimePort(t), runtimePort(t)
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hy2Port := udp.LocalAddr().(*net.UDPAddr).Port
	_ = udp.Close()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, publicKey := base64.RawURLEncoding.EncodeToString(private.Bytes()), base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
	shortIDs := []string{"cfe1a2b3c4", "ab", "0123456789abcd", "a1b2c3d4e5f6", "abcd", "0011223344556677", "abcdef", "01234567"}
	quoted, _ := json.Marshal(shortIDs)
	reality := fmt.Sprintf(`"security":"reality","realitySettings":{"show":false,"xver":0,"target":"%s","serverNames":["runtime-qa.test","www.runtime-qa.test"],"privateKey":"%s","minClientVer":"0.0.0","maxClientVer":"","maxTimediff":0,"shortIds":%s,"mldsa65Seed":""}`, strings.TrimPrefix(target.URL, "https://"), privateKey, quoted)
	const vlessID, hy2Auth = "6f9619ff-8b86-4d11-b42d-00c04fc964ff", "Ab3dE6gH9jK2mN5pQ8sT1vW4yZ7bC0eF"
	vless := `{"clients":[{"id":"` + vlessID + `","flow":"","email":"%s","enable":true}],"decryption":"none"}`
	sniff := `{"enabled":true,"destOverride":["http","tls","quic"],"routeOnly":true}`
	db := ouiDatabase(t, []ouiSample{
		{id: 1, remark: "noflow", protocol: "vless", listen: "127.0.0.1", port: realityPort, settings: fmt.Sprintf(vless, "noflow-user"), stream: `{"network":"tcp",` + reality + `,"tcpSettings":{"header":{"type":"none"}}}`, sniff: sniff},
		{id: 2, remark: "xhttp-user", protocol: "vless", listen: "127.0.0.1", port: xhttpPort, settings: fmt.Sprintf(vless, "xhttp-user"), stream: `{"network":"xhttp",` + reality + `,"xhttpSettings":{"path":"/oui-xh-b2","host":"runtime-qa.test","mode":"stream-up","xPaddingBytes":"100-1000","scMaxEachPostBytes":"1000000","scMaxBufferedPosts":30,"scStreamUpServerSecs":"20-80","scMinPostsIntervalMs":"30"}}`, sniff: sniff},
		{id: 37, remark: "HY2", protocol: "hysteria", listen: "127.0.0.1", port: hy2Port, settings: `{"clients":[{"auth":"` + hy2Auth + `","email":"hy2-user","enable":true}],"version":2}`, stream: `{"network":"hysteria","security":"tls","tlsSettings":{"serverName":"runtime-qa.test","alpn":["h3"],"certificates":[{"certificateFile":"` + cert + `","keyFile":"` + key + `"}]},"hysteriaSettings":{"version":2}}`, sniff: sniff},
	})
	body := map[string]string{"archive": base64.StdEncoding.EncodeToString(db), "shareHost": "127.0.0.1"}
	var preview backupPreview
	_ = json.Unmarshal(request("POST", "/api/backup/preview", body, 200), &preview)
	if preview.Inbounds != 3 || len(preview.Notes) != 1 {
		t.Fatal("preview", preview.Inbounds, preview.Notes)
	}
	body["fingerprint"], body["confirmation"] = preview.Fingerprint, "确认覆盖并恢复"
	request("POST", "/api/backup/restore", body, 200)
	settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, originPort), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
	settingsData, _ := json.Marshal(settings)
	var workspace, saved store.Document
	_ = json.Unmarshal(request("GET", "/api/settings", nil, 200), &saved)
	request("PUT", "/api/settings", store.Document{Revision: saved.Revision, Data: settingsData}, 200)
	request("POST", "/api/runtime/stop", map[string]any{}, 200)
	_ = json.Unmarshal(request("GET", "/api/workspace", nil, 200), &workspace)
	_ = json.Unmarshal(request("GET", "/api/settings", nil, 200), &saved)
	request("POST", "/api/runtime/apply", runtimeRequest{Version: version, WorkspaceRevision: workspace.Revision, SettingsRevision: saved.Revision}, 200)

	clientEngine := &engine.Engine{}
	defer clientEngine.Close()
	proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	connect := func(out map[string]any) error {
		t.Helper()
		config := map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}, "outbounds": []any{out}}
		data, _ := json.Marshal(config)
		file := filepath.Join(t.TempDir(), "client.json")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}); err != nil {
			t.Fatal(err)
		}
		transport.CloseIdleConnections()
		response, err := client.Get(origin.URL)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if data, err := io.ReadAll(response.Body); err != nil || string(data) != "oui-import-ok" {
			return fmt.Errorf("bad response")
		}
		return nil
	}
	vlessOut := func(port int, flow, network string, extra map[string]any) map[string]any {
		user := map[string]any{"id": vlessID, "encryption": "none"}
		if flow != "" {
			user["flow"] = flow
		}
		stream := map[string]any{"network": network, "security": "reality", "realitySettings": map[string]any{"serverName": "www.runtime-qa.test", "fingerprint": "chrome", "publicKey": publicKey, "shortId": shortIDs[4], "spiderX": "/"}}
		for k, v := range extra {
			stream[k] = v
		}
		return map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{user}}}}, "streamSettings": stream}
	}
	if err := connect(vlessOut(realityPort, "", "tcp", nil)); err != nil {
		t.Fatal("OUI flow-less REALITY client (5th shortId, 2nd SNI) did not connect:", err)
	}
	if err := connect(vlessOut(realityPort, "xtls-rprx-vision", "tcp", nil)); err == nil {
		t.Fatal("a Vision client must not connect to an imported flow-less inbound")
	}
	if err := connect(vlessOut(xhttpPort, "", "xhttp", map[string]any{"xhttpSettings": map[string]any{"path": "/oui-xh-b2", "host": "runtime-qa.test", "mode": "stream-up"}})); err != nil {
		t.Fatal("OUI XHTTP REALITY client with Host did not connect:", err)
	}
	hy2 := map[string]any{"protocol": "hysteria", "settings": map[string]any{"version": 2, "address": "127.0.0.1", "port": hy2Port}, "streamSettings": map[string]any{"network": "hysteria", "security": "tls", "hysteriaSettings": map[string]any{"version": 2, "auth": hy2Auth}, "tlsSettings": map[string]any{"serverName": "runtime-qa.test", "alpn": []string{"h3"}, "disableSystemRoot": true, "certificates": []any{map[string]string{"certificateFile": cert, "usage": "verify"}}}}}
	if err := connect(hy2); err != nil {
		t.Fatal("OUI HY2 client did not connect:", err)
	}
	hy2["streamSettings"].(map[string]any)["hysteriaSettings"] = map[string]any{"version": 2, "auth": "wrong-auth-value-0123456789abcdef"}
	if err := connect(hy2); err == nil {
		t.Fatal("wrong HY2 auth must be rejected")
	}
}
