package config

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestClientConnectionsFourPresets(t *testing.T) {
	for _, preset := range []string{"reality", "hy2", "xhttp-tls", "xhttp-reality"} {
		t.Run(preset, func(t *testing.T) {
			in, client := fixture(preset)
			in.XHTTP.Path = "/中文/space test/%2F"
			compiled, err := compileOne(in, client)
			if err != nil {
				t.Fatal(err)
			}
			out, link, err := ClientConnection(compiled[0], compiled[0].Settings.Clients[0], "example.test", "中文 名称/#?&")
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(link)
			if err != nil || u.Fragment != "中文 名称/#?&" || u.Host != "127.0.0.1:18443" || u.Query().Get("sni") != "example.test" {
				t.Fatal("URI round trip mismatch")
			}
			data, _ := json.Marshal(out)
			if strings.Contains(string(data), "privateKey") || strings.Contains(string(data), in.Reality.PrivateKey) || strings.Contains(string(data), "allowInsecure") || strings.Contains(link, "insecure") || strings.Contains(string(data), "/fixtures") || strings.Contains(string(data), "client-first") {
				t.Fatal("server data or insecure settings leaked")
			}
			stream := out["streamSettings"].(map[string]any)
			if preset == "hy2" {
				if u.Scheme != "hysteria2" || u.User.Username() != client.Auth || stream["hysteriaSettings"].(map[string]any)["auth"] != client.Auth || out["settings"].(map[string]any)["version"] != 2 {
					t.Fatal("HY2 auth mismatch")
				}
			} else {
				if u.Scheme != "vless" || u.User.Username() != client.UUID || (u.Query().Get("flow") != "") != (preset == "reality") {
					t.Fatal("VLESS auth/flow mismatch")
				}
			}
			if strings.Contains(preset, "reality") {
				private, _ := base64.RawURLEncoding.DecodeString(in.Reality.PrivateKey)
				key, _ := ecdh.X25519().NewPrivateKey(private)
				public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
				if u.Query().Get("pbk") != public || stream["realitySettings"].(map[string]any)["password"] != public || u.Query().Get("sid") != "0123456789abcdef" {
					t.Fatal("REALITY public fields mismatch")
				}
			}
			if strings.HasPrefix(preset, "xhttp-") && (u.Query().Get("path") != in.XHTTP.Path || u.Query().Get("mode") != "auto") {
				t.Fatal("XHTTP URI escaping mismatch")
			}
		})
	}
}

func TestClientConnectionRejectsInvalidInputs(t *testing.T) {
	in, c := fixture("reality")
	compiled, _ := compileOne(in, c)
	item := compiled[0]
	for _, host := range []string{"", "bad name", "x.test\r\nsecret", "https://example.test"} {
		if _, _, err := ClientConnection(item, item.Settings.Clients[0], host, "test"); err == nil {
			t.Fatal("invalid SNI accepted")
		}
	}
	item.Listen = "0.0.0.0"
	if _, _, err := ClientConnection(item, item.Settings.Clients[0], "example.test", "test"); err == nil {
		t.Fatal("public export accepted")
	}
	item = compiled[0]
	item.Stream.Reality.PrivateKey = "do-not-echo-secret"
	if _, _, err := ClientConnection(item, item.Settings.Clients[0], "example.test", "test"); err == nil || strings.Contains(err.Error(), "do-not-echo") {
		t.Fatal("invalid key accepted or leaked")
	}
}
