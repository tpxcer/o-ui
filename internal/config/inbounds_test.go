package config

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var fixtureNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func fixture(preset string) (Inbound, Client) {
	// 固定虚构测试值，不生成或读取正式凭据、证书、可运行配置文件。
	return Inbound{ID: "one", Preset: preset, Listen: "127.0.0.1", Port: 18443, Enabled: true,
			Sniffing:    Sniffing{Enabled: true, DestOverride: []string{"http", "tls", "quic"}, RouteOnly: true},
			Reality:     &Reality{Target: "example.test:443", ServerNames: []string{"example.test"}, PrivateKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("X", 32))), ShortIDs: []string{"0123456789abcdef"}},
			Certificate: &Certificate{File: "/fixtures/cert.pem", Key: "/fixtures/key.pem"}, XHTTP: &XHTTP{Path: "/test-xhttp", Mode: "auto"}},
		Client{ID: "first", InboundID: "one", Enabled: true, UUID: "00000000-0000-4000-8000-000000000001", Auth: "fictional-test-auth-only"}
}

func compileOne(in Inbound, client Client) ([]CompiledInbound, error) {
	return CompileInbounds([]Inbound{in}, []Client{client}, fixtureNow)
}

func TestFourPresets(t *testing.T) {
	for _, preset := range []string{"reality", "hy2", "xhttp-tls", "xhttp-reality"} {
		t.Run(preset, func(t *testing.T) {
			in, client := fixture(preset)
			result, err := compileOne(in, client)
			if err != nil || len(result) != 1 {
				t.Fatalf("compile failed: %v", err)
			}
			got := result[0]
			if got.Tag != "in-one" || got.Listen != "127.0.0.1" || got.Settings.Clients[0].Email != "client-first" || !got.Sniffing.RouteOnly || len(got.Sniffing.DestOverride) != 3 {
				t.Fatal("stable tags, explicit listener or sniffing lost")
			}
			if preset == "hy2" {
				if got.Protocol != "hysteria" || got.Settings.Version != 2 || got.Stream.Network != "hysteria" || got.Stream.Hysteria.Version != 2 || got.Stream.TLS.ALPN[0] != "h3" || got.Settings.Clients[0].Auth != client.Auth || got.Settings.Clients[0].ID != "" {
					t.Fatal("HY2 protocol/transport version or auth mismatch")
				}
			} else if got.Settings.Decryption != "none" || got.Settings.Clients[0].ID != client.UUID || got.Settings.Clients[0].Auth != "" {
				t.Fatal("VLESS identity or decryption mismatch")
			}
			if (got.Settings.Clients[0].Flow != "") != (preset == "reality") {
				t.Fatal("Vision must only be set for TCP REALITY")
			}
			if strings.HasPrefix(preset, "xhttp") && (got.Stream.Network != "xhttp" || got.Stream.XHTTP.Path != in.XHTTP.Path) {
				t.Fatal("XHTTP fields lost")
			}
			if (got.Stream.TLS != nil) != (preset == "hy2" || preset == "xhttp-tls") || (got.Stream.Reality != nil) != (preset == "reality" || preset == "xhttp-reality") {
				t.Fatal("security layer mismatch")
			}
			data, _ := json.Marshal(got)
			if strings.Contains(string(data), "allowInsecure") || strings.Contains(string(data), "api") {
				t.Fatal("unexpected insecure or control setting")
			}
		})
	}
}

func TestRealityCompatibilityIsExplicit(t *testing.T) {
	for _, preset := range []string{"reality", "xhttp-reality"} {
		in, client := fixture(preset)
		for _, enabled := range []bool{false, true, false} {
			in.Reality.MinClientVer = ""
			if enabled {
				in.Reality.MinClientVer = "1.8.2"
			}
			compiled, err := compileOne(in, client)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(compiled)
			if strings.Contains(string(data), `"minClientVer":"1.8.2"`) != enabled || !enabled && strings.Contains(string(data), "minClientVer") {
				t.Fatal("REALITY compatibility default/toggle serialization changed")
			}
		}
		in.Reality.MinClientVer = "0.0.0"
		if _, err := compileOne(in, client); err == nil {
			t.Fatal("arbitrary minimum version accepted")
		}
	}
}

func TestClientFiltering(t *testing.T) {
	in, client := fixture("reality")
	past, equal, future := fixtureNow.Add(-time.Second), fixtureNow, fixtureNow.Add(time.Second)
	for _, tc := range []struct {
		name string
		edit func(*Client)
	}{
		{"disabled", func(c *Client) { c.Enabled = false }},
		{"expired", func(c *Client) { c.ExpiresAt = &past }},
		{"exact expiry", func(c *Client) { c.ExpiresAt = &equal }},
		{"quota reached", func(c *Client) { c.QuotaBytes, c.UsedBytes = 100, 100 }},
		{"quota exceeded", func(c *Client) { c.QuotaBytes, c.UsedBytes = 100, 101 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := client
			tc.edit(&c)
			c.UUID = "" // 被排除用户不需要读取或生成凭据。
			got, err := compileOne(in, c)
			if err != nil || len(got) != 0 {
				t.Fatalf("excluded user left a listener: %v", err)
			}
		})
	}
	client.ExpiresAt, client.UsedBytes = &future, 999
	if got, err := compileOne(in, client); err != nil || len(got) != 1 {
		t.Fatalf("zero quota must be unlimited: %v", err)
	}
	in.Enabled = false
	if got, err := compileOne(in, client); err != nil || len(got) != 0 {
		t.Fatal("disabled inbound was emitted")
	}
	if got, err := CompileInbounds([]Inbound{in}, nil, fixtureNow); err != nil || len(got) != 0 {
		t.Fatal("empty users must not create a listener")
	}
}

func TestIntegrityAndUnsupportedLimits(t *testing.T) {
	in, client := fixture("reality")
	for _, tc := range []struct {
		name string
		edit func(*Inbound, *Client)
	}{
		{"inbound id", func(i *Inbound, _ *Client) { i.ID = "../bad" }},
		{"client id", func(_ *Inbound, c *Client) { c.ID = "bad@email" }},
		{"orphan", func(_ *Inbound, c *Client) { c.InboundID = "missing" }},
		{"unknown preset", func(i *Inbound, _ *Client) { i.Preset = "vmess" }},
		{"implicit bind", func(i *Inbound, _ *Client) { i.Listen = "" }},
		{"port", func(i *Inbound, _ *Client) { i.Port = 65536 }},
		{"UUID", func(_ *Inbound, c *Client) { c.UUID = "do-not-disclose-this-value" }},
		{"nil UUID", func(_ *Inbound, c *Client) { c.UUID = "00000000-0000-0000-0000-000000000000" }},
		{"IP limit", func(_ *Inbound, c *Client) { c.IPLimit = 1 }},
		{"sniffing", func(i *Inbound, _ *Client) { i.Sniffing.DestOverride = []string{"invalid"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, c := in, client
			tc.edit(&i, &c)
			got, err := compileOne(i, c)
			if err == nil || got != nil || strings.Contains(err.Error(), "do-not-disclose") {
				t.Fatal("invalid input accepted, partial output or secret echoed")
			}
		})
	}
	if _, err := CompileInbounds([]Inbound{in, in}, []Client{client}, fixtureNow); err == nil {
		t.Fatal("duplicate inbound accepted")
	}
	if _, err := CompileInbounds([]Inbound{in}, []Client{client, client}, fixtureNow); err == nil {
		t.Fatal("duplicate client accepted")
	}
	second := client
	second.ID = "second"
	if _, err := CompileInbounds([]Inbound{in}, []Client{client, second}, fixtureNow); err == nil {
		t.Fatal("duplicate credential in inbound accepted")
	}
	if _, err := CompileInbounds(nil, nil, time.Time{}); err == nil {
		t.Fatal("implicit wall clock accepted")
	}
}

func TestSecurityInputs(t *testing.T) {
	for _, tc := range []struct {
		name, preset string
		edit         func(*Inbound, *Client)
	}{
		{"missing reality", "reality", func(i *Inbound, _ *Client) { i.Reality = nil }},
		{"private key", "reality", func(i *Inbound, _ *Client) { i.Reality.PrivateKey = "not-a-key" }},
		{"target", "reality", func(i *Inbound, _ *Client) { i.Reality.Target = "example.test" }},
		{"target port", "reality", func(i *Inbound, _ *Client) { i.Reality.Target = "example.test:0" }},
		{"SNI", "reality", func(i *Inbound, _ *Client) { i.Reality.ServerNames = []string{"example.test:443"} }},
		{"short ID", "reality", func(i *Inbound, _ *Client) { i.Reality.ShortIDs = []string{"123"} }},
		{"missing cert", "hy2", func(i *Inbound, _ *Client) { i.Certificate = nil }},
		{"relative cert", "xhttp-tls", func(i *Inbound, _ *Client) { i.Certificate.File = "cert.pem" }},
		{"auth", "hy2", func(_ *Inbound, c *Client) { c.Auth = "short" }},
		{"auth control", "hy2", func(_ *Inbound, c *Client) { c.Auth += "\n" }},
		{"missing path", "xhttp-tls", func(i *Inbound, _ *Client) { i.XHTTP = nil }},
		{"path query", "xhttp-reality", func(i *Inbound, _ *Client) { i.XHTTP.Path = "/x?y=1" }},
		{"mode", "xhttp-tls", func(i *Inbound, _ *Client) { i.XHTTP.Mode = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, client := fixture(tc.preset)
			tc.edit(&in, &client)
			if got, err := compileOne(in, client); err == nil || got != nil {
				t.Fatal("invalid security input accepted")
			}
		})
	}
}

func TestListenerConflicts(t *testing.T) {
	in, client := fixture("reality")
	for _, address := range []string{"127.0.0.1", "0.0.0.0", "::", "::ffff:127.0.0.1"} {
		other, c := fixture("xhttp-reality")
		other.ID, c.ID, c.InboundID, other.Listen = "two", "second", "two", address
		if got, err := CompileInbounds([]Inbound{in, other}, []Client{client, c}, fixtureNow); err == nil || got != nil {
			t.Fatalf("listener collision not rejected: %s", address)
		}
	}
	other, c := fixture("hy2")
	other.ID, c.ID, c.InboundID = "two", "second", "two"
	if got, err := CompileInbounds([]Inbound{in, other}, []Client{client, c}, fixtureNow); err != nil || len(got) != 2 {
		t.Fatalf("TCP/UDP same numeric port should coexist: %v", err)
	}
}

func TestPureDeterministicOutput(t *testing.T) {
	in, client := fixture("reality")
	before, _ := json.Marshal(in)
	first, err := compileOne(in, client)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := compileOne(in, client)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("output not deterministic")
	}
	first[0].Stream.Reality.ServerNames[0] = "changed.test"
	first[0].Sniffing.DestOverride[0] = "quic"
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("compiler output aliases input")
	}
}
