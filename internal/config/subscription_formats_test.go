package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func presetOutbound(t *testing.T, preset string) map[string]any {
	t.Helper()
	in, client := fixture(preset)
	compiled, err := compileOne(in, client)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := ClientConnection(compiled[0], compiled[0].Settings.Clients[0], "example.test", "节点")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestClashProxyFourPresets(t *testing.T) {
	for preset, want := range map[string][]string{
		"hy2":           {`type: "hysteria2"`, `password: "`, `sni: "example.test"`, `alpn:` + "\n" + `      - "h3"`},
		"reality":       {`type: "vless"`, `network: "tcp"`, `flow: "xtls-rprx-vision"`, `servername: "example.test"`, `client-fingerprint: "chrome"`, "reality-opts:\n      public-key: \"", `short-id: "0123456789abcdef"`},
		"xhttp-tls":     {`network: "xhttp"`, `tls: true`, `servername: "example.test"`, "xhttp-opts:\n      path: \"", `mode: "auto"`},
		"xhttp-reality": {`network: "xhttp"`, "reality-opts:", "xhttp-opts:"},
	} {
		t.Run(preset, func(t *testing.T) {
			proxy, err := ClashProxy(presetOutbound(t, preset), `中文 "名称": yes`)
			if err != nil {
				t.Fatal(err)
			}
			yaml := ClashConfig(proxy)
			if !strings.HasPrefix(yaml, "proxies:\n  - name: \"中文 \\\"名称\\\": yes\"\n") || !strings.Contains(yaml, "proxy-groups:\n  - name: \"PROXY\"\n    type: \"select\"\n    proxies:\n      - \"中文 \\\"名称\\\": yes\"\n      - \"DIRECT\"\nrules:\n  - \"MATCH,PROXY\"\n") {
				t.Fatal(yaml)
			}
			for _, part := range want {
				if !strings.Contains(yaml, part) {
					t.Fatalf("missing %q in\n%s", part, yaml)
				}
			}
			if strings.Contains(yaml, "skip-cert-verify") || strings.Contains(yaml, "privateKey") {
				t.Fatal("insecure or server-only field leaked")
			}
		})
	}
	if _, err := ClashProxy(map[string]any{"protocol": "vmess"}, "x"); err == nil {
		t.Fatal("unsupported protocol accepted")
	}
}

func TestXrayClientConfigOptions(t *testing.T) {
	for _, preset := range []string{"reality", "hy2", "xhttp-tls", "xhttp-reality"} {
		t.Run(preset, func(t *testing.T) {
			out := presetOutbound(t, preset)
			original, _ := json.Marshal(out)
			cfg, err := XrayClientConfig(out, "节点", JSONOptions{Fragment: true, FragmentPackets: "tlshello", FragmentLength: "100-200", FragmentInterval: "10-20", DirectIP: []string{"geoip:private"}, DirectDomain: []string{"geosite:cn"}})
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(out)
			if string(after) != string(original) {
				t.Fatal("source outbound was modified")
			}
			data, _ := json.Marshal(cfg)
			text := string(data)
			for _, part := range []string{`"remarks":"节点"`, `"listen":"127.0.0.1","port":10808`, `"listen":"127.0.0.1","port":10809`, `{"ip":["geoip:private"],"outboundTag":"direct","type":"field"},{"domain":["geosite:cn"],"outboundTag":"direct","type":"field"},{"network":"tcp,udp","outboundTag":"proxy","type":"field"}`} {
				if !strings.Contains(text, part) {
					t.Fatalf("missing %s in %s", part, text)
				}
			}
			fragment := strings.Contains(text, `"dialerProxy":"fragment"`) && strings.Contains(text, `"fragment":{"interval":"10-20","length":"100-200","packets":"tlshello"}`)
			if fragment == (preset == "hy2") {
				t.Fatal("fragment must apply to TCP transports only", text)
			}
			if binary := os.Getenv("PANEL_XRAY_INTEGRATION"); binary != "" {
				file := filepath.Join(t.TempDir(), "client.json")
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				if output, err := exec.Command(binary, "run", "-test", "-config", file).CombinedOutput(); err != nil {
					t.Fatalf("official core rejected JSON subscription: %s", output)
				}
			}
		})
	}
	cfg, err := XrayClientConfig(presetOutbound(t, "reality"), "x", JSONOptions{})
	data, _ := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), "fragment") || strings.Count(string(data), `"type":"field"`) != 1 {
		t.Fatal("default JSON subscription should only route everything through proxy", string(data))
	}
}
