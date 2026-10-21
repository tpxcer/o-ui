package localapi

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
)

func hoppingWorkspace(hopA, hopB, hopC string) json.RawMessage {
	sniffing := `"sniffing":{"enabled":true,"http":true,"tls":true,"quic":true,"routeOnly":true,"fakeDns":false,"metadataOnly":false}`
	inbound := func(id, preset string, port int, hop string) string {
		extra := ""
		if hop != "" {
			extra = `,"portHopping":"` + hop + `"`
		}
		return `{"id":"` + id + `","name":"` + id + `","preset":"` + preset + `","port":` + strconv.Itoa(port) + extra + `,"enabled":true,` + sniffing + `}`
	}
	return json.RawMessage(`{"inbounds":[` + inbound("a", "hy2", 50000, hopA) + `,` + inbound("b", "hy2", 51000, hopB) + `,` + inbound("c", "reality", 48100, hopC) + `],"clients":[]}`)
}

func TestValidatePortHopping(t *testing.T) {
	for _, test := range []struct {
		name             string
		hopA, hopB, hopC string
		ok               bool
	}{
		{"none", "", "", "", true},
		{"tcp-port-inside-range-is-fine", "48000-48199", "", "", true},
		{"two-separate-ranges", "48000-48199", "48200-48399", "", true},
		{"vless-has-no-hopping", "", "", "40000-40199", false},
		{"bad-format", "48000", "", "", false},
		{"reversed", "48199-48000", "", "", false},
		{"privileged", "1000-1199", "", "", false},
		{"includes-own-port", "49900-50100", "", "", false},
		{"includes-other-hy2-port", "50900-51100", "", "", false},
		{"overlapping-ranges", "48000-48199", "48100-48300", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateWorkspaceDraft(hoppingWorkspace(test.hopA, test.hopB, test.hopC), nil); (err == nil) != test.ok {
				t.Fatal(err)
			}
		})
	}
}

func TestSyncFirewallUpdatesPortHopping(t *testing.T) {
	previousFirewall, previousHopping := runFirewall, runPortHopping
	defer func() { runFirewall, runPortHopping = previousFirewall, previousHopping }()
	runFirewall = func(context.Context, string, string, string) (string, error) { return "", nil }
	var calls []string
	runPortHopping = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "hop:" + strings.Join(args, " "), nil
	}
	message := syncFirewall(context.Background(), hoppingWorkspace("48000-48199", "", ""), hoppingWorkspace("48200-48399", "", ""))
	if strings.Join(calls, ",") != "delete 48000-48199 50000,add 48200-48399 50000" || !strings.Contains(message, "hop:delete 48000-48199 50000; hop:add 48200-48399 50000") {
		t.Fatal("range change must delete the old forward before adding the new one", calls, message)
	}
	calls = nil
	if syncFirewall(context.Background(), hoppingWorkspace("48200-48399", "", ""), hoppingWorkspace("48200-48399", "", "")); len(calls) != 0 {
		t.Fatal("unchanged ranges must not touch the helper", calls)
	}
	runPortHopping = func(context.Context, ...string) (string, error) { return "", errPortHoppingHelperOld }
	message = syncFirewall(context.Background(), hoppingWorkspace("", "", ""), hoppingWorkspace("", "52000-52199", ""))
	if !strings.Contains(message, "52000-52199→51000") || !strings.Contains(message, "o-ui update") {
		t.Fatal("missing port hopping failure hint", message)
	}
	if message = syncFirewall(context.Background(), hoppingWorkspace("", "52000-52199", ""), hoppingWorkspace("", "", "")); message != "" {
		t.Fatal("failed deletes must stay quiet", message)
	}
}

// 面板启动时先清空本链，再按已保存入站重建（服务器重启后内核规则已清空）。
func TestRestorePortHoppingFlushesThenRebuilds(t *testing.T) {
	state, rt, _, _ := subscriptionFixture(t)
	doc, err := state.ReadDocument(context.Background(), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.SaveDocument(context.Background(), "workspace", doc.Revision, hoppingWorkspace("48000-48199", "52000-52199", "")); err != nil {
		t.Fatal(err)
	}
	previous := runPortHopping
	defer func() { runPortHopping = previous }()
	var calls []string
	runPortHopping = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	if err := rt.RestorePortHopping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0] != "flush" || !strings.Contains(strings.Join(calls, ","), "add 48000-48199 50000") || !strings.Contains(strings.Join(calls, ","), "add 52000-52199 51000") {
		t.Fatal(calls)
	}
}

func TestFreeHopRangeSkipsUsedAndBusyPorts(t *testing.T) {
	busy, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	taken := busy.LocalAddr().(*net.UDPAddr).Port
	free, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	candidate := free.LocalAddr().(*net.UDPAddr).Port
	free.Close()
	saved := candidate + 1000
	sequence := []int{saved - 10, taken - 10, candidate}
	hop, ok := freeHopRange(map[int]bool{saved: true}, func() int { next := sequence[0]; sequence = sequence[1:]; return next })
	if !ok || hop != strconv.Itoa(candidate)+"-"+strconv.Itoa(candidate+hopRangeSize-1) {
		t.Skip("candidate range is busy on this machine:", hop, ok)
	}
	if len(sequence) != 0 {
		t.Fatal("ranges with saved or busy ports must be skipped", sequence)
	}
}
