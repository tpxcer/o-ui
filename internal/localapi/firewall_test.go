package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
