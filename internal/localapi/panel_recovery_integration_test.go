package localapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
)

// 面板证书源丢失后离线恢复访问；原运行快照仍须使用原身份/证书完成真代理。
func TestOfficialPanelAccessRecovery(t *testing.T) {
	source := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if source == "" || os.Getenv("PANEL_PROCESS_QA") != "1" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE与PANEL_PROCESS_QA=1后验证CLI恢复及官方代理")
	}
	version := runtimeQAVersion(t)
	m, err := core.NewManager(source)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Selected() != version {
		t.Fatal("explicit QA version mismatch")
	}
	coreBinary, err := m.Binary(version)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	session, err := s.Initialize(ctx, "recoveryqa", "isolated-recovery-test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	cert, key := runtimeCertificate(t, t.TempDir())
	certificate, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(panelTLSDraft{cert, key})
	if _, err := s.SaveDocument(ctx, "panel-tls", 0, data); err != nil {
		t.Fatal(err)
	}
	pair, err := LoadPanelTLS(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	rt, err := NewRuntime(m, s, runtimeRoot, pair)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close() }()
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	cookie := &http.Cookie{Name: sessionCookie, Value: session.Token}
	call := func(method, path string, value any, want int) []byte {
		t.Helper()
		data, _ := json.Marshal(value)
		r := httptest.NewRequest(method, "http://127.0.0.1:5180/api/"+path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal("recovery fixture API mismatch", method, path, w.Code, want)
		}
		return w.Body.Bytes()
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "access-recovery-original-proxy-ok")
	}))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	draft.Clients[0].Subscription = true
	draft.Inbounds[0].Preset, draft.Inbounds[0].Port = "xhttp-tls", runtimePort(t)
	draft.Inbounds[0].Connection = &connectionDraft{UsePanelCertificate: true, Path: "/access-recovery-qa", Mode: "auto"}
	data, _ = json.Marshal(draft)
	call("PUT", "workspace", store.Document{Data: data}, 200)
	settings := json.RawMessage(fmt.Sprintf(`{"freedomStrategy":"AsIs","routingStrategy":"AsIs","probeURL":"https://example.test/","statistics":true,"accessLog":false,"dnsLog":false,"logLevel":"warning","rules":"[]","outbounds":%q,"balancers":"[]","dns":"{\"servers\":[\"localhost\"]}","advanced":"{}"}`, fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, targetPort)))
	call("PUT", "settings", store.Document{Data: settings}, 200)
	request := runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}
	call("POST", "runtime/apply", request, 200)
	var exported connectionResult
	if json.Unmarshal(call("POST", "runtime/connection", connectionRequest{ClientID: "client-one", WorkspaceRevision: 1}, 200), &exported) != nil {
		t.Fatal("connection export failed")
	}
	grant := subscriptionCall(t, handler, "rotate", subscriptionRequest{ClientID: "client-one", WorkspaceRevision: 1, Confirm: true}, cookie, 200)
	trust := filepath.Join(t.TempDir(), "client-trust.pem")
	if err := writePrivate(trust, certificate); err != nil {
		t.Fatal(err)
	}
	tlsSettings := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
	tlsSettings["disableSystemRoot"], tlsSettings["certificates"] = true, []any{map[string]string{"certificateFile": trust, "usage": "verify"}}
	clientPort := runtimePort(t)
	data, _ = json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}})
	clientFile := filepath.Join(t.TempDir(), "client.json")
	if err := writePrivate(clientFile, data); err != nil {
		t.Fatal(err)
	}
	clientEngine := &engine.Engine{}
	defer clientEngine.Close()
	if err := clientEngine.Apply(ctx, engine.Candidate{Binary: coreBinary, Version: version, File: clientFile, Inbounds: 1}); err != nil {
		t.Fatal(err)
	}
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", clientPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	connect := func() {
		t.Helper()
		response, err := client.Get(target.URL)
		if err != nil {
			t.Fatal("original client cannot connect", err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "access-recovery-original-proxy-ok" {
			t.Fatal("original proxy response mismatch")
		}
	}
	connect()
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	names := []string{"workspace", "xray-settings", "proxy-identities", "traffic", subscriptionsDocument, appliedDocument}
	before, err := s.ReadDocuments(ctx, names)
	if err != nil {
		t.Fatal(err)
	}
	ledger, _, err := rt.ledger(ctx)
	if err != nil || ledger.Totals["client-one"].Total() == 0 || ledger.Active {
		t.Fatal("test did not preserve actual stopped usage", err)
	}
	for _, path := range []string{cert, key} {
		if err := os.Rename(path, path+".unavailable"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadPanelTLS(ctx, s); err == nil {
		t.Fatal("missing original certificate did not block normal startup")
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	panelBinary := processQABinary(t, project)
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	command := exec.CommandContext(bounded, panelBinary, "-recover-panel-access", "-state", root, "-listen", "127.0.0.1:5180", "-confirm-recovery", "恢复面板本机访问")
	if output, err := command.CombinedOutput(); err != nil || !bytes.Contains(output, []byte(i18n.Pick(i18n.EnvLang(), "尚未启动服务", "Nothing was started"))) {
		t.Fatal("actual recovery CLI failed", err)
	}
	after, err := s.ReadDocuments(ctx, names)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("recovery changed original identities, usage, subscription or history", err)
	}
	if _, err := s.Authenticate(ctx, session.Token); err != nil {
		t.Fatal("recovery invalidated existing administrator/session")
	}
	rt, err = NewRuntime(m, s, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	handler = Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	if rt.engine.Status().Running {
		t.Fatal("recovery/startup automatically started core")
	}
	subscriptionGET(t, handler, grant.Path, 503)
	call("POST", "runtime/apply", request, 422) // 新草稿不得静默沿用已停用的面板证书。
	history := rt.historySummary(ctx)
	if !history.Available || history.Version != version {
		t.Fatal("original applied snapshot lost")
	}
	call("POST", "runtime/resume", map[string]any{"revision": history.Revision, "confirmation": "确认恢复上次应用"}, 200)
	connect()
	decoded, err := base64.StdEncoding.DecodeString(string(subscriptionGET(t, handler, grant.Path, 200)))
	if err != nil || string(decoded) != exported.Link+"\n" {
		t.Fatal("recovery rotated subscription or original proxy identity")
	}
	current, _, err := rt.ledger(ctx)
	if err != nil || current.Totals["client-one"].Total() < ledger.Totals["client-one"].Total() {
		t.Fatal("restored runtime reduced accumulated usage", err)
	}
}
