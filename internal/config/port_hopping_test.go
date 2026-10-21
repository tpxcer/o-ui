package config

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePortRange(t *testing.T) {
	for value, want := range map[string]bool{
		"48000-48199": true, "1024-65535": true, "1024-1025": true,
		"48000": false, "48199-48000": false, "48000-48000": false, "1023-2000": false, "48000-65536": false,
		" 48000-48199": false, "48000-48199-1": false, "04800-48199": false, "48000:48199": false, "-48199": false, "": false,
	} {
		if _, _, ok := ParsePortRange(value); ok != want {
			t.Errorf("%q: got %v", value, ok)
		}
	}
}

// HY2端口跳跃：分享链接带mport，Xray JSON出站带finalmask.quicParams.udpHop，Clash带ports；三者同源。
func TestHY2PortHoppingClientOutputs(t *testing.T) {
	in, client := fixture("hy2")
	compiled, err := compileOne(in, client)
	if err != nil {
		t.Fatal(err)
	}
	hy2 := compiled[0]
	hy2.PortHopping = "48000-48199"
	out, link, err := ClientConnection(hy2, hy2.Settings.Clients[0], "example.test", "节点")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	if u.Query().Get("mport") != "48000-48199" || u.Port() != "18443" {
		t.Fatal("share link must keep the listen port and add mport", link)
	}
	hop := out["streamSettings"].(map[string]any)["finalmask"].(map[string]any)["quicParams"].(map[string]any)["udpHop"].(map[string]any)
	if hop["ports"] != "48000-48199" || len(hop) != 1 {
		t.Fatal("unexpected udpHop", hop)
	}
	proxy, err := ClashProxy(out, "节点")
	if err != nil || !strings.Contains(ClashConfig(proxy), "\n    ports: \"48000-48199\"\n") {
		t.Fatal("Clash proxy must carry ports", err, ClashConfig(proxy))
	}
	cfg, err := XrayClientConfig(out, "节点", JSONOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(cfg)
	if !strings.Contains(string(data), `"finalmask":{"quicParams":{"udpHop":{"ports":"48000-48199"}}}`) {
		t.Fatal("JSON subscription lost udpHop", string(data))
	}
	if binary := os.Getenv("PANEL_XRAY_INTEGRATION"); binary != "" {
		file := filepath.Join(t.TempDir(), "client.json")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(binary, "run", "-test", "-config", file).CombinedOutput(); err != nil {
			t.Fatalf("official core rejected udpHop: %s", output)
		}
	}
	// 没有跳跃范围时不输出这些字段。
	plain, plainLink, _ := ClientConnection(compiled[0], compiled[0].Settings.Clients[0], "example.test", "节点")
	if _, found := plain["streamSettings"].(map[string]any)["finalmask"]; found || strings.Contains(plainLink, "mport") {
		t.Fatal("hopping fields leaked without a range")
	}
	hy2.PortHopping = "48000"
	if _, _, err := ClientConnection(hy2, hy2.Settings.Clients[0], "example.test", "节点"); err == nil {
		t.Fatal("invalid range accepted")
	}
}
