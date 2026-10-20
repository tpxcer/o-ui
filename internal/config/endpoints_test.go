package config

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestProxyEndpointValidation(t *testing.T) {
	for _, item := range []struct{ listen, host, want string }{
		{"", "", "127.0.0.1"}, {"::1", "", "::1"}, {"::ffff:127.0.0.1", "", "127.0.0.1"},
		{"0.0.0.0", "proxy.example.test", "0.0.0.0"}, {"::", "2001:db8::1", "::"}, {"192.0.2.10", "proxy.example.test", "192.0.2.10"},
	} {
		listen, host, err := ProxyEndpoint(item.listen, item.host)
		if err != nil || listen != item.want || host == "" {
			t.Fatal("valid endpoint rejected", err)
		}
	}
	for _, listen := range []string{"localhost", "[::1]", "127.0.0.1:8000", "fe80::1%en0", "bad\n", "224.0.0.1", "ff02::1", "::ffff:224.0.0.1", "255.255.255.255"} {
		if _, _, err := ProxyEndpoint(listen, "proxy.test"); err == nil {
			t.Fatalf("invalid listen accepted: %q", listen)
		}
	}
	for _, listen := range []string{"0.0.0.0", "::", "192.0.2.1", "::ffff:192.0.2.1"} {
		if _, _, err := ProxyEndpoint(listen, ""); err == nil {
			t.Fatal("non-loopback needs explicit share host")
		}
	}
	for _, host := range []string{"https://proxy.test", "user@proxy.test", "proxy.test:443", "proxy.test/", "proxy.test?x", "proxy.test#x", "[2001:db8::1]", "fe80::1%en0", "0.0.0.0", "::", "::ffff:0.0.0.0", "224.0.0.1", "ff02::1", "::ffff:224.0.0.1", "255.255.255.255", "bad name", "bad\r\n", "proxy..test", "-proxy.test", "proxy-.test", strings.Repeat("x", 64) + ".test", "1.2.3.999", "2130706433"} {
		if _, _, err := ProxyEndpoint("127.0.0.1", host); err == nil {
			t.Fatalf("invalid share host accepted: %q", host)
		}
	}
}

func TestProxyEndpointShareFourPresets(t *testing.T) {
	for _, preset := range []string{"reality", "hy2", "xhttp-tls", "xhttp-reality"} {
		for _, host := range []string{"proxy.example.test", "192.0.2.10", "2001:db8::10"} {
			t.Run(preset+"/"+host, func(t *testing.T) {
				in, client := fixture(preset)
				in.Listen = "0.0.0.0"
				compiled, err := compileOne(in, client)
				if err != nil {
					t.Fatal(err)
				}
				out, link, err := ClientConnection(compiled[0], compiled[0].Settings.Clients[0], "sni.example.test", "中文 /?#&", host)
				if err != nil {
					t.Fatal(err)
				}
				u, err := url.Parse(link)
				if err != nil || u.Hostname() != host || u.Port() != "18443" || u.Query().Get("sni") != "sni.example.test" || u.Fragment != "中文 /?#&" {
					t.Fatal("share host/SNI/URI escaping mismatch")
				}
				settings := out["settings"].(map[string]any)
				if preset != "hy2" {
					settings = settings["vnext"].([]any)[0].(map[string]any)
				}
				if settings["address"] != host {
					t.Fatal("outbound address differs from URI")
				}
				data, _ := json.Marshal(out)
				if strings.Contains(string(data), "privateKey") || strings.Contains(string(data), in.Reality.PrivateKey) || strings.Contains(string(data), "allowInsecure") || strings.Contains(string(data), "0.0.0.0") {
					t.Fatal("private/listen/insecure fields leaked")
				}
			})
		}
	}
}
