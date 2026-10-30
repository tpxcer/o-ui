package localapi

import (
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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

// 同一账户在TCP和UDP入站真实传输，验证核心按同一email合计且额度覆盖全部节点。
func TestOfficialMultiInboundClient(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证多入站共享额度")
	}
	ctx := context.Background()
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
	session, err := s.Initialize(ctx, "multiqa", "isolated-multi-client-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	cert, key := runtimeCertificate(t, t.TempDir())
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", 32<<10)) }))
	defer origin.Close()
	_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	draft.Inbounds[0].Preset = "xhttp-tls"
	draft.Inbounds[0].Port = runtimePort(t)
	draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/multi-test", Mode: "auto"}
	second := draft.Inbounds[0]
	second.ID = "second"
	second.Name = draft.Inbounds[0].Name
	second.Preset = "hy2"
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	second.Port = packet.LocalAddr().(*net.UDPAddr).Port
	packet.Close()
	draft.Inbounds = append(draft.Inbounds, second)
	disabled := second
	disabled.ID = "disabled"
	disabled.Enabled = false
	disabled.Port++
	draft.Inbounds = append(draft.Inbounds, disabled)
	draft.Clients[0].InboundIDs = []string{draft.Inbounds[0].ID, "second", "disabled"}
	draft.Clients[0].Subscription = true
	other := draft.Clients[0]
	other.ID = "other"
	other.Name = "other"
	other.InboundIDs = nil
	draft.Clients = append(draft.Clients, other)
	data, _ := json.Marshal(draft)
	if err = validateWorkspaceDraft(data, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveDocument(ctx, "workspace", 0, data); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s"}]}}]`, originPort), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
	data, _ = json.Marshal(settings)
	if _, err = s.SaveDocument(ctx, "xray-settings", 0, data); err != nil {
		t.Fatal(err)
	}
	initial := traffic.Empty()
	initial.Totals["client-one"] = traffic.Count{Downlink: 123}
	data, _ = json.Marshal(initial)
	_, _ = s.SaveDocument(ctx, "traffic", 0, data)
	revision := int64(1)
	apply := func() {
		t.Helper()
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if err := rt.checkpointCurrent(ctx); err != nil {
			t.Fatal(err)
		}
		c, err := rt.candidate(ctx, runtimeRequest{Version: version, WorkspaceRevision: revision, SettingsRevision: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err = rt.apply(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	rt.mu.Lock()
	exported, err := rt.connection(ctx, connectionRequest{ClientID: "client-one", WorkspaceRevision: revision})
	rt.mu.Unlock()
	if err != nil || len(exported.Nodes) != 2 {
		t.Fatal("multi connection", err)
	}
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	granted := subscriptionCall(t, handler, "rotate", subscriptionRequest{ClientID: "client-one", WorkspaceRevision: revision, Confirm: true}, &http.Cookie{Name: sessionCookie, Value: session.Token}, 200)
	payload, err := base64.StdEncoding.DecodeString(string(subscriptionGET(t, handler, granted.Path, 200)))
	if err != nil || len(strings.Fields(string(payload))) != 2 {
		t.Fatal("base64 subscription lost nodes", err)
	}
	suffix := strings.TrimPrefix(granted.Path, "/sub/")
	clash := string(subscriptionGET(t, handler, "/sub/clash/"+suffix, 200))
	if strings.Count(clash, "  - name:") != 3 || !strings.Contains(clash, "#2") {
		t.Fatal("Clash nodes or unique names missing")
	}
	var configs []map[string]any
	if json.Unmarshal(subscriptionGET(t, handler, "/sub/json/"+suffix, 200), &configs) != nil || len(configs) != 2 {
		t.Fatal("JSON subscription lost nodes")
	}
	startClient := func(node connectionResult) *http.Client {
		t.Helper()
		// 导出副本仅信任测试证书，生产导出保持默认验证。
		data, _ := json.Marshal(node.Outbound)
		var outbound map[string]any
		_ = json.Unmarshal(data, &outbound)
		tlsSettings := outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
		tlsSettings["disableSystemRoot"] = true
		tlsSettings["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
		port := runtimePort(t)
		data, _ = json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}, "outbounds": []any{outbound}})
		file := filepath.Join(t.TempDir(), "client.json")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		clientEngine := &engine.Engine{}
		t.Cleanup(func() { _ = clientEngine.Close() })
		if err := clientEngine.Apply(ctx, engine.Candidate{Binary: binary, File: file, Inbounds: 1}); err != nil {
			t.Fatal(err)
		}
		proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 3 * time.Second}
	}
	clients := []*http.Client{startClient(exported.Nodes[0]), startClient(exported.Nodes[1])}
	transfer := func(client *http.Client) {
		t.Helper()
		response, err := client.Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err != nil || n != 32<<10 {
			t.Fatal("proxy transfer", err)
		}
	}
	sample := func() traffic.Ledger {
		t.Helper()
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if err := rt.checkpointCurrent(ctx); err != nil {
			t.Fatal(err)
		}
		l, _, err := rt.ledger(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	before := sample()
	for i, client := range clients {
		transfer(client)
		after := sample()
		if after.Totals["client-one"].Total() < before.Totals["client-one"].Total()+32<<10 {
			t.Fatalf("shared account missed node %d", i)
		}
		before = after
	}
	if before.Inbounds == nil || before.Inbounds.Totals[draft.Inbounds[0].ID].Total() == 0 || before.Inbounds.Totals["second"].Total() == 0 {
		t.Fatal("independent inbound traffic missing")
	}
	apply()
	restarted := sample()
	if restarted.Totals["client-one"] != before.Totals["client-one"] || restarted.Inbounds.Totals["second"] != before.Inbounds.Totals["second"] {
		t.Fatal("restart duplicated/lost usage")
	}
	draft.Clients[0].QuotaGB = float64(restarted.Totals["client-one"].Total()+50000) / 1e9
	data, _ = json.Marshal(draft)
	if _, err = s.SaveDocument(ctx, "workspace", revision, data); err != nil {
		t.Fatal(err)
	}
	revision++
	apply()
	clients = []*http.Client{startClient(exported.Nodes[0]), startClient(exported.Nodes[1])}
	transfer(clients[0])
	first := sample()
	if first.Totals["client-one"].Total() >= quotaBytes(draft.Clients[0].QuotaGB) {
		t.Fatal("first node alone exhausted quota")
	}
	transfer(clients[1])
	after := sample()
	if after.Totals["client-one"].Total() < quotaBytes(draft.Clients[0].QuotaGB) {
		t.Fatal("combined usage did not exhaust quota")
	}
	rt.mu.Lock()
	err = rt.guard(ctx)
	_, connectionErr := rt.connection(ctx, connectionRequest{ClientID: "client-one", WorkspaceRevision: revision})
	rt.mu.Unlock()
	if err != nil || connectionErr == nil {
		t.Fatal("shared quota not enforced", err)
	}
	for _, client := range clients {
		if response, err := client.Get(origin.URL); err == nil {
			response.Body.Close()
			t.Fatal("exhausted account remained usable")
		}
	}
	subscriptionGET(t, handler, granted.Path, 404)
	rt.mu.Lock()
	survivor, err := rt.connection(ctx, connectionRequest{ClientID: "other", WorkspaceRevision: revision})
	rt.mu.Unlock()
	if err != nil {
		t.Fatal("unrelated account removed", err)
	}
	transfer(startClient(survivor))
	final := sample()
	encoded, _ := json.Marshal(final)
	if _, err := backupLedger(encoded); err != nil {
		t.Fatal("new ledger backup rejected", err)
	}
	identitiesBefore, _ := s.ReadDocument(ctx, "proxy-identities")
	draft.Clients[0].QuotaGB = 0
	draft.Clients[0].InboundID, draft.Clients[0].InboundIDs = "second", []string{"second"}
	doc, _ := s.ReadDocument(ctx, "workspace")
	data, _ = json.Marshal(draft)
	if _, err = saveSubscriptionWorkspace(ctx, s, doc, data); err != nil {
		t.Fatal(err)
	}
	revision++
	apply()
	rt.mu.Lock()
	unlinked, err := rt.connection(ctx, connectionRequest{ClientID: "client-one", WorkspaceRevision: revision})
	rt.mu.Unlock()
	if err != nil || len(unlinked.Nodes) != 1 || unlinked.InboundID != "second" {
		t.Fatal("unlink did not retain other node", err)
	}
	identitiesAfter, _ := s.ReadDocument(ctx, "proxy-identities")
	if string(identitiesBefore.Data) != string(identitiesAfter.Data) {
		t.Fatal("unlink rotated account identity")
	}
	status := subscriptionCall(t, handler, "status", subscriptionRequest{ClientID: "client-one", WorkspaceRevision: revision}, &http.Cookie{Name: sessionCookie, Value: session.Token}, 200)
	if status.Path != granted.Path || !status.Active {
		t.Fatal("unlink rotated subscription grant")
	}
	var single map[string]any
	if json.Unmarshal(subscriptionGET(t, handler, "/sub/json/"+suffix, 200), &single) != nil || single["remarks"] == nil {
		t.Fatal("single-node JSON compatibility lost")
	}
	for _, kind := range []string{"disabled", "expired"} {
		if kind == "disabled" {
			draft.Clients[0].Enabled = false
		} else {
			draft.Clients[0].Enabled = true
			past := time.Now().Add(-time.Minute).UnixMilli()
			draft.Clients[0].ExpiresAt = &past
		}
		doc, _ = s.ReadDocument(ctx, "workspace")
		data, _ = json.Marshal(draft)
		if _, err = saveSubscriptionWorkspace(ctx, s, doc, data); err != nil {
			t.Fatal(err)
		}
		revision++
		apply()
		for _, path := range []string{granted.Path, "/sub/clash/" + suffix, "/sub/json/" + suffix} {
			subscriptionGET(t, handler, path, 404)
		}
	}

}
