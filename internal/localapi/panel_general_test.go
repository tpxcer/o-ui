package localapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

type settingsClient struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
}

func (c *settingsClient) call(method, path string, value any) *httptest.ResponseRecorder {
	c.t.Helper()
	data, _ := json.Marshal(value)
	r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
	r.Header.Set("Origin", "http://127.0.0.1:5180")
	r.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	return w
}

func settingsFixture(t *testing.T) (*settingsClient, *store.Store) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	active, err := LoadPanelAccess(context.Background(), s, "127.0.0.1:5180", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &settingsClient{t: t, handler: PanelHandler(m, s, t.TempDir(), active, rt)}
	w := client.call("POST", "/api/auth/setup", map[string]string{"username": "settingsqa", "password": "settings-passphrase"})
	if w.Code != 200 {
		t.Fatal("setup", w.Code)
	}
	client.cookie = w.Result().Cookies()[0]
	return client, s
}

func testTOTP(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}

func TestTwoFactorAPI(t *testing.T) {
	client, _ := settingsFixture(t)
	anonymous := &settingsClient{t: t, handler: client.handler}
	if w := anonymous.call("POST", "/api/auth/2fa/setup", struct{}{}); w.Code != 401 {
		t.Fatal("anonymous setup", w.Code)
	}
	var status struct{ TwoFactor bool }
	if json.Unmarshal(anonymous.call("GET", "/api/auth/status", nil).Body.Bytes(), &status) != nil || status.TwoFactor {
		t.Fatal("two-factor reported before enabling")
	}
	w := client.call("POST", "/api/auth/2fa/setup", struct{}{})
	var setup struct{ Secret, URI string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &setup) != nil || len(setup.Secret) != 32 {
		t.Fatal("setup response", w.Code, w.Body.String())
	}
	uri, err := url.Parse(setup.URI)
	if err != nil || uri.Scheme != "otpauth" || uri.Host != "totp" || uri.Path != "/o-ui:settingsqa" || uri.Query().Get("secret") != setup.Secret || uri.Query().Get("issuer") != "o-ui" || uri.Query().Get("digits") != "6" || uri.Query().Get("period") != "30" {
		t.Fatal("otpauth URI", setup.URI)
	}
	if w := client.call("POST", "/api/auth/2fa/enable", map[string]string{"code": "000000"}); w.Code != 403 {
		t.Fatal("wrong code enabled", w.Code)
	}
	if w := client.call("POST", "/api/auth/2fa/enable", map[string]string{"code": testTOTP(t, setup.Secret, time.Now())}); w.Code != 200 {
		t.Fatal("enable", w.Code, w.Body.String())
	}
	if json.Unmarshal(anonymous.call("GET", "/api/auth/status", nil).Body.Bytes(), &status) != nil || !status.TwoFactor {
		t.Fatal("two-factor not reported to the sign-in page")
	}
	if w := client.call("POST", "/api/auth/2fa/setup", struct{}{}); w.Code != 409 {
		t.Fatal("setup while enabled", w.Code)
	}
	credentials := map[string]string{"username": "settingsqa", "password": "settings-passphrase"}
	if w := anonymous.call("POST", "/api/auth/login", credentials); w.Code != 401 || !strings.Contains(w.Body.String(), "验证码") {
		t.Fatal("sign-in without code", w.Code, w.Body.String())
	}
	// 启用时用过的时间步不能再用；用允许偏差内的下一时间步登录
	credentials["code"] = testTOTP(t, setup.Secret, time.Now())
	if w := anonymous.call("POST", "/api/auth/login", credentials); w.Code != 401 {
		t.Fatal("enable code replayed for sign-in", w.Code)
	}
	credentials["code"] = testTOTP(t, setup.Secret, time.Now().Add(30*time.Second))
	if w := anonymous.call("POST", "/api/auth/login", credentials); w.Code != 200 {
		t.Fatal("sign-in with code", w.Code, w.Body.String())
	}
	if w := client.call("POST", "/api/auth/password", map[string]string{"currentPassword": "settings-passphrase", "newUsername": "renamed"}); w.Code != 403 {
		t.Fatal("account change without code", w.Code)
	}
	if w := client.call("POST", "/api/auth/2fa/disable", map[string]string{"code": "123456"}); w.Code != 403 {
		t.Fatal("wrong disable code", w.Code)
	}
}

func TestPanelGeneralAPI(t *testing.T) {
	client, _ := settingsFixture(t)
	var status generalStatus
	read := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatal("status", w.Code, code, w.Body.String())
		}
		if code == 200 && json.Unmarshal(w.Body.Bytes(), &status) != nil {
			t.Fatal("invalid general status")
		}
	}
	read(client.call("GET", "/api/panel/general", nil), 200)
	if status.Access.Data != nil || status.Access.Active.Listen != "127.0.0.1:5180" || status.TLS.Data != nil || status.Session.Minutes != store.DefaultSessionMinutes || status.Session.Revision != 0 {
		t.Fatal("defaults", status)
	}
	request := func() map[string]any {
		return map[string]any{"accessRevision": status.Access.Revision, "tlsRevision": status.TLS.Revision, "sessionRevision": status.Session.Revision,
			"access": PanelAccessDraft{Listen: "127.0.0.1:5180"}, "tls": panelTLSDraft{}, "sessionMinutes": status.Session.Minutes}
	}
	// 未改动不写入，修订号不变
	read(client.call("PUT", "/api/panel/general", request()), 200)
	if status.Access.Revision != 0 || status.TLS.Revision != 0 || status.Session.Revision != 0 {
		t.Fatal("unchanged save bumped revisions", status)
	}
	for _, minutes := range []int{4, 43201} {
		body := request()
		body["sessionMinutes"] = minutes
		read(client.call("PUT", "/api/panel/general", body), 400)
	}
	body := request()
	body["sessionMinutes"] = 30
	read(client.call("PUT", "/api/panel/general", body), 200)
	if status.Session.Minutes != 30 || status.Session.Revision != 1 || status.Access.Revision != 0 {
		t.Fatal("session save", status)
	}
	w := client.call("POST", "/api/auth/login", map[string]string{"username": "settingsqa", "password": "settings-passphrase"})
	// MaxAge按剩余时间取整，慢速（如race）下允许少1秒。
	if maxAge := w.Result().Cookies()[0].MaxAge; w.Code != 200 || maxAge < 1799 || maxAge > 1800 {
		t.Fatal("session duration not applied to sign-in", w.Code)
	}
	read(client.call("PUT", "/api/panel/general", body), 409)
	// 改为公网HTTPS：需证书匹配域名，并明确确认
	cert, key := runtimeCertificate(t, t.TempDir(), "old.example.test")
	body = request()
	body["access"] = PanelAccessDraft{Public: true, Listen: "0.0.0.0:5190", Origin: "https://old.example.test:5190", BasePath: "/qa/"}
	read(client.call("PUT", "/api/panel/general", body), 400)
	body["tls"] = panelTLSDraft{cert, key}
	read(client.call("PUT", "/api/panel/general", body), 409)
	body["confirmation"] = httpsConfirmation
	read(client.call("PUT", "/api/panel/general", body), 200)
	if !status.Access.Data.Public || status.Access.Data.BasePath != "/qa/" || status.TLS.Data == nil || status.TLS.Data.CertificateFile != cert || !status.Access.Pending || !status.TLS.Pending {
		t.Fatal("public save", status)
	}
	// 同时换域名与证书一次保存成功（分开保存会互相校验失败）
	newCert, newKey := runtimeCertificate(t, t.TempDir(), "new.example.test")
	body = request()
	body["access"] = PanelAccessDraft{Public: true, Listen: "0.0.0.0:5190", Origin: "https://new.example.test:5190", BasePath: "/qa/"}
	body["tls"], body["confirmation"] = panelTLSDraft{newCert, newKey}, httpsConfirmation
	read(client.call("PUT", "/api/panel/general", body), 200)
	if status.Access.Data.Origin != "https://new.example.test:5190" || status.TLS.Data.KeyFile != newKey {
		t.Fatal("domain and certificate change", status)
	}
	// 非法路径与不匹配证书都不部分写入
	before := status
	body = request()
	body["access"] = PanelAccessDraft{Public: true, Listen: "0.0.0.0:5190", Origin: "https://new.example.test:5190", BasePath: "/api/"}
	body["tls"], body["confirmation"] = panelTLSDraft{newCert, newKey}, httpsConfirmation
	read(client.call("PUT", "/api/panel/general", body), 400)
	body["access"] = PanelAccessDraft{Public: true, Listen: "0.0.0.0:5190", Origin: "https://new.example.test:5190"}
	body["tls"] = panelTLSDraft{cert, key}
	read(client.call("PUT", "/api/panel/general", body), 400)
	read(client.call("GET", "/api/panel/general", nil), 200)
	if status.Access.Revision != before.Access.Revision || status.TLS.Revision != before.TLS.Revision {
		t.Fatal("rejected save changed documents")
	}
	// 本次启动的访问边界不随保存改变
	r := httptest.NewRequest("GET", "/api/auth/status", nil)
	r.Host, r.RemoteAddr, r.TLS = "new.example.test:5190", "198.51.100.1:1234", &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	client.handler.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatal("saved settings changed the live boundary", rec.Code)
	}
}

func TestSubscriptionRemarkSetting(t *testing.T) {
	s, err := validateSubscriptionSettings(json.RawMessage(`{"enabled":true,"updateHours":12,"title":"","showInfo":true}`))
	if err != nil || !s.RemarkClientName {
		t.Fatal("older settings without the field must keep client names", s, err)
	}
	if s, err = validateSubscriptionSettings(json.RawMessage(`{"enabled":true,"updateHours":12,"title":"","showInfo":true,"remarkClientName":false}`)); err != nil || s.RemarkClientName {
		t.Fatal("explicit false ignored", err)
	}
	workspace := store.Document{Data: json.RawMessage(`{"inbounds":[{"id":"in-1","name":"香港 HY2"}],"clients":[]}`)}
	link := "hysteria2://secret@203.0.113.1:443?sni=a.example#old%20name"
	for _, tc := range []struct {
		with   bool
		client clientDraft
		want   string
	}{
		{true, clientDraft{InboundID: "in-1", Name: "alice"}, "#%E9%A6%99%E6%B8%AF%20HY2-alice"},
		{false, clientDraft{InboundID: "in-1", Name: "alice"}, "#%E9%A6%99%E6%B8%AF%20HY2"},
		{true, clientDraft{InboundID: "missing", Name: "bob"}, "#bob"},
	} {
		got := subscriptionLinkName(link, tc.with, workspace, tc.client)
		if got != "hysteria2://secret@203.0.113.1:443?sni=a.example"+tc.want {
			t.Fatal("subscription name", got)
		}
	}
}
