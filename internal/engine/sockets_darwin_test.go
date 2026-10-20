package engine

import "testing"

func TestParseSockets(t *testing.T) {
	data := "p12\nf3\ntIPv4\nPTCP\nTST=LISTEN\nf4\ntIPv6\nPTCP\nTST=ESTABLISHED\nf5\ntIPv4\nPUDP\n"
	if tcp, udp, err := parseSockets(data, 12); err != nil || tcp != 1 || udp != 1 {
		t.Fatal(tcp, udp, err)
	}
	for _, data := range []string{"", "p12\n", "p13\nf3\ntIPv4\nPUDP\n", "p12\nf3\ntIPv4\nPTCP\n", "p12\nf3\ntIPv4\nPUNKNOWN\n", "p12\nf3\ntIPv4\nPUDP\nxunexpected\n"} {
		if _, _, err := parseSockets(data, 12); err == nil {
			t.Fatal("无效响应当成零", data)
		}
	}
}
