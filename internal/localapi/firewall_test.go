package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSyncFirewallAllowsNewAndRemovesDeletedPorts(t *testing.T) {
	previous := runFirewall
	defer func() { runFirewall = previous }()
	var calls []string
	runFirewall = func(_ context.Context, action, port, protocol string) (string, error) {
		calls = append(calls, action+" "+port+"/"+protocol)
		return action + ":" + port + "/" + protocol, nil
	}
	draft := func(ports map[int]string) json.RawMessage {
		var inbounds []map[string]any
		for port, preset := range ports {
			inbounds = append(inbounds, map[string]any{"port": port, "preset": preset})
		}
		data, _ := json.Marshal(map[string]any{"inbounds": inbounds})
		return data
	}
	message := syncFirewall(context.Background(), draft(map[int]string{50001: "reality", 50002: "hy2"}), draft(map[int]string{50001: "reality", 50003: "hy2"}))
	if strings.Join(calls, ",") != "delete 50002/udp,allow 50003/udp" || message != "delete:50002/udp; allow:50003/udp" {
		t.Fatal(calls, message)
	}
	calls = nil
	runFirewall = func(context.Context, string, string, string) (string, error) { return "", errors.New("no helper") }
	message = syncFirewall(context.Background(), draft(map[int]string{}), draft(map[int]string{50004: "xhttp-tls"}))
	if !strings.Contains(message, "50004/tcp") {
		t.Fatal("missing manual firewall hint", message)
	}
	if message = syncFirewall(context.Background(), draft(map[int]string{50004: "xhttp-tls"}), draft(map[int]string{})); !strings.Contains(message, "未能撤销旧端口") {
		t.Fatal("failed deletes must warn", message)
	}
}

func TestQuickCreateWorkspaceAutomaticallyAllowsAllPresets(t *testing.T) {
	for _, preset := range []string{"reality", "xhttp-reality", "xhttp-tls", "hy2"} {
		t.Run(preset, func(t *testing.T) {
			state, _, handler, cookie := subscriptionFixture(t)
			previousFirewall, previousHopping := runFirewall, runPortHopping
			t.Cleanup(func() { runFirewall, runPortHopping = previousFirewall, previousHopping })
			var calls []string
			runFirewall = func(_ context.Context, action, port, protocol string) (string, error) {
				calls = append(calls, action+" "+port+"/"+protocol)
				return "automatically allowed", nil
			}
			runPortHopping = func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, "redirect "+strings.Join(args, " "))
				return "redirect configured", nil
			}
			doc, err := state.ReadDocument(context.Background(), "workspace")
			if err != nil {
				t.Fatal(err)
			}
			var draft workspaceDraft
			if err := json.Unmarshal(doc.Data, &draft); err != nil {
				t.Fatal(err)
			}
			inbound := draft.Inbounds[0]
			inbound.ID, inbound.Name, inbound.Preset, inbound.Port = "in-quick", "测试新建", preset, 58443
			inbound.Listen, inbound.ShareHost = "0.0.0.0", "panel.example.com"
			if preset == "hy2" || preset == "xhttp-tls" {
				inbound.Connection = &connectionDraft{UsePanelCertificate: true}
			}
			if preset == "hy2" {
				inbound.PortHopping = "48000-48199"
			}
			draft.Inbounds = append(draft.Inbounds, inbound)
			client := draft.Clients[0]
			client.ID, client.InboundID, client.Name = "client-quick", inbound.ID, "测试客户端"
			draft.Clients = append(draft.Clients, client)
			body, _ := json.Marshal(map[string]any{"revision": doc.Revision, "data": draft})
			request := httptest.NewRequest("PUT", "/api/workspace", strings.NewReader(string(body)))
			request.Host, request.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
			request.Header.Set("Origin", "http://127.0.0.1:5180")
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			protocol := "tcp"
			want := "allow 58443/tcp"
			if preset == "hy2" {
				protocol, want = "udp", "allow 58443/udp,redirect add 48000-48199 58443"
			}
			if strings.Join(calls, ",") != want {
				t.Fatal("新建保存未自动放行", calls)
			}
			var result savedDocument
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Firewall, "automatically allowed") {
				t.Fatal("自动放行结果未返回")
			}
			// 无测试核心可应用，仍必须完成自动放行并记录，不能依赖用户点重试。
			if result.Applied || result.ApplyError == "" {
				t.Fatal("夹具应处于尚未安装核心状态")
			}
			status, err := state.ReadDocument(context.Background(), "firewall-status")
			if err != nil {
				t.Fatal(err)
			}
			var report firewallReport
			if err := json.Unmarshal(status.Data, &report); err != nil {
				t.Fatal(err)
			}
			if report.Revision != result.Revision || report.Endpoints["58443/"+protocol].State != "allowed" {
				t.Fatal("自动放行状态未持久保存", report)
			}
		})
	}
}

func TestFirewallReportPersistsFailuresAndRetryDoesNotRestartCore(t *testing.T) {
	state, rt, handler, cookie := subscriptionFixture(t)
	previous := runFirewall
	t.Cleanup(func() { runFirewall = previous })
	runFirewall = func(context.Context, string, string, string) (string, error) {
		return "unsupported input filter", errors.New("blocked")
	}
	ctx := context.Background()
	doc, err := state.ReadDocument(ctx, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	report, err := saveFirewallReport(ctx, state, nil, doc.Data, doc.Revision)
	if err != nil || report.Endpoints["18443/tcp"].State != "failed" {
		t.Fatal(report, err)
	}
	// 无关保存不能清除仍未修复端口的告警。
	report, err = saveFirewallReport(ctx, state, doc.Data, doc.Data, doc.Revision)
	if err != nil || report.Endpoints["18443/tcp"].State != "failed" {
		t.Fatal(report, err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	read := call("GET", "/api/runtime/firewall", "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"state":"failed"`) {
		t.Fatal(read.Code, read.Body.String())
	}
	runFirewall = func(context.Context, string, string, string) (string, error) { return "allowed", nil }
	before := rt.engine.Status()
	result := call("POST", "/api/runtime/firewall/retry", `{"inboundId":"in-one"}`)
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"state":"allowed"`) {
		t.Fatal(result.Code, result.Body.String())
	}
	after := rt.engine.Status()
	if before.Running != after.Running || before.WorkspaceRevision != after.WorkspaceRevision {
		t.Fatal("retry changed core state")
	}
	if missing := call("POST", "/api/runtime/firewall/retry", `{"inboundId":"missing"}`); missing.Code != 404 {
		t.Fatal(missing.Code)
	}
	runFirewall = func(context.Context, string, string, string) (string, error) {
		return "no supported active firewall detected", exec.Command("sh", "-c", "exit 4").Run()
	}
	report, err = saveFirewallReport(ctx, state, nil, doc.Data, doc.Revision)
	if err != nil || report.Endpoints["18443/tcp"].State != "unverified" {
		t.Fatal(report, err)
	}
}

func TestFirewallReportMarksPortHoppingFailure(t *testing.T) {
	previousFirewall, previousHopping := runFirewall, runPortHopping
	t.Cleanup(func() { runFirewall, runPortHopping = previousFirewall, previousHopping })
	runFirewall = func(context.Context, string, string, string) (string, error) { return "allowed", nil }
	runPortHopping = func(context.Context, ...string) (string, error) { return "", errors.New("NAT failed") }
	data := json.RawMessage(`{"inbounds":[{"preset":"hy2","port":50574,"portHopping":"59556-59755"}]}`)
	for _, pair := range [][2]json.RawMessage{{nil, data}, {data, nil}} {
		report := syncFirewallReport(context.Background(), pair[0], pair[1])
		if report.Endpoints["50574/udp"].State != "failed" {
			t.Fatal("NAT failure hidden by port allow status", report)
		}
	}
}

func TestOldFirewallCleanupRequiresPendingUnusedEndpointAndKeepsFailures(t *testing.T) {
	state, rt, handler, cookie := subscriptionFixture(t)
	ctx := context.Background()
	previous := runFirewall
	t.Cleanup(func() { runFirewall = previous })
	doc, err := state.ReadDocument(ctx, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	fail := true
	runFirewall = func(_ context.Context, action, port, protocol string) (string, error) {
		commands = append(commands, action+" "+port+"/"+protocol)
		if fail {
			return "unsupported input filter", errors.New("blocked")
		}
		return "not added by o-ui; unchanged", nil
	}
	seed := firewallReport{Revision: doc.Revision, Endpoints: map[string]firewallEndpoint{
		"18443/tcp": {State: "failed", Message: "active"},
		"18443/udp": {State: "failed", Message: "other protocol"},
		"51514/tcp": {State: "failed", Message: "legacy failure"},
		"60890/udp": {State: "failed", Operation: "delete"},
		"62388/tcp": {State: "unverified", Operation: "delete"},
		"52000/udp": {State: "failed", Operation: "redirect-delete", Message: "Port hopping update failed"},
		"52001/udp": {State: "failed", Message: "端口跳跃转发操作失败"},
	}}
	data, _ := json.Marshal(seed)
	if _, err = state.SaveDocument(ctx, "firewall-status", 0, data); err != nil {
		t.Fatal(err)
	}
	call := func(endpoint string, authenticated bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"endpoint": endpoint})
		r := httptest.NewRequest("POST", "/api/runtime/firewall/cleanup", strings.NewReader(string(body)))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for endpoint, code := range map[string]int{"18443/tcp": 409, "18443/udp": 409, "52000/udp": 409, "52001/udp": 409, "1023/tcp": 400, "65536/tcp": 400, "051514/tcp": 400, "51514/tcp;id": 400, "51515/tcp": 404} {
		if result := call(endpoint, true); result.Code != code {
			t.Fatalf("%s: %d %s", endpoint, result.Code, result.Body.String())
		}
	}
	if result := call("51514/tcp", false); result.Code != 401 {
		t.Fatal(result.Code)
	}
	if len(commands) != 0 {
		t.Fatal("unsafe request invoked helper", commands)
	}
	before := rt.engine.Status()
	result := call("51514/tcp", true)
	if result.Code != 200 || !strings.Contains(result.Body.String(), "unsupported input filter") {
		t.Fatal(result.Code, result.Body.String())
	}
	stored, _ := state.ReadDocument(ctx, "firewall-status")
	var report firewallReport
	_ = json.Unmarshal(stored.Data, &report)
	if report.Endpoints["51514/tcp"].State != "failed" || report.Endpoints["51514/tcp"].Operation != "delete" {
		t.Fatal("failure record lost", report)
	}
	fail = false
	for _, endpoint := range []string{"51514/tcp", "60890/udp", "62388/tcp"} {
		if result := call(endpoint, true); result.Code != 200 || strings.Contains(result.Body.String(), `"`+endpoint+`"`) {
			t.Fatal(result.Code, result.Body.String())
		}
	}
	stored, _ = state.ReadDocument(ctx, "firewall-status")
	_ = json.Unmarshal(stored.Data, &report)
	if !strings.Contains(string(stored.Data), `"18443/tcp"`) || !strings.Contains(string(stored.Data), `"52000/udp"`) {
		t.Fatal("unrelated records changed", string(stored.Data))
	}
	if result := call("51514/tcp", true); result.Code != 404 || len(commands) != 4 {
		t.Fatal("duplicate cleanup invoked helper", result.Code, commands)
	}
	after := rt.engine.Status()
	if before.Running != after.Running || before.WorkspaceRevision != after.WorkspaceRevision {
		t.Fatal("cleanup changed core state")
	}
	workspace, _ := state.ReadDocument(ctx, "workspace")
	if workspace.Revision != doc.Revision || string(workspace.Data) != string(doc.Data) {
		t.Fatal("cleanup changed business workspace")
	}
}

// 保存已提交后，浏览器断开（例如面板经本机代理访问、保存重启Xray时连接被切断）不能取消放行防火墙。
func TestWorkspaceSaveFinishesFirewallAfterClientDisconnects(t *testing.T) {
	_, _, handler, cookie := subscriptionFixture(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	previous := runFirewall
	defer func() { runFirewall = previous }()
	result := make(chan string, 1)
	runFirewall = func(ctx context.Context, action, port, protocol string) (string, error) {
		select {
		case <-ctx.Done():
			result <- "canceled " + action + " " + port
			return "", ctx.Err()
		case <-time.After(1500 * time.Millisecond):
			result <- action + " " + port + "/" + protocol
			return "ok", nil
		}
	}
	data := strings.Replace(strings.ReplaceAll(string(validWorkspace()), `"subscription":false`, `"subscription":true`), `"port":18443`, `"port":18443,"listen":"0.0.0.0","shareHost":"203.0.113.10"`, 1)
	data = strings.Replace(data, `}],"clients"`, `},{"id":"in-two","name":"新入站","preset":"reality","port":57836,"listen":"0.0.0.0","shareHost":"203.0.113.10","enabled":true,"sniffing":{"enabled":true,"http":true,"tls":true,"quic":true,"routeOnly":true,"fakeDns":false,"metadataOnly":false}}],"clients"`, 1)
	body, _ := json.Marshal(map[string]any{"revision": 1, "data": json.RawMessage(data)})
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf("PUT /api/workspace HTTP/1.1\r\nHost: 127.0.0.1:5180\r\nOrigin: http://127.0.0.1:5180\r\nContent-Type: application/json\r\nCookie: %s=%s\r\nContent-Length: %d\r\n\r\n%s", cookie.Name, cookie.Value, len(body), body)
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	_ = conn.Close()
	select {
	case got := <-result:
		if !strings.Contains(got, "57836/tcp") {
			t.Fatal("firewall allow was cancelled by the client disconnect:", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("firewall sync never ran")
	}
}
