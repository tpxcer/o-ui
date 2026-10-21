package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
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
	if message = syncFirewall(context.Background(), draft(map[int]string{50004: "xhttp-tls"}), draft(map[int]string{})); message != "" {
		t.Fatal("failed deletes must stay quiet", message)
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
