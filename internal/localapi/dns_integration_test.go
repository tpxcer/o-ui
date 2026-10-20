package localapi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 自有回环DNS，仅回答固定.invalid域名，不调用系统或公共解析器。
func localDNSFixture(t *testing.T, ipv6, missing bool) (string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	a, aaaa := &atomic.Int64{}, &atomic.Int64{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				for {
					var prefix [2]byte
					if _, err := io.ReadFull(conn, prefix[:]); err != nil {
						return
					}
					message := make([]byte, int(binary.BigEndian.Uint16(prefix[:])))
					if len(message) < 17 || len(message) > 4096 {
						return
					}
					if _, err := io.ReadFull(conn, message); err != nil || binary.BigEndian.Uint16(message[4:6]) != 1 {
						return
					}
					end, labels := 12, []string{}
					for end < len(message) && message[end] != 0 {
						size := int(message[end])
						if size > 63 || end+size+1 >= len(message) {
							return
						}
						labels = append(labels, string(message[end+1:end+size+1]))
						end += size + 1
					}
					if end+5 > len(message) || strings.Join(labels, ".") != "panel-dns-qa.invalid" {
						return
					}
					end++
					kind := binary.BigEndian.Uint16(message[end : end+2])
					if kind == 1 {
						a.Add(1)
					} else if kind == 28 {
						aaaa.Add(1)
					} else {
						return
					}
					answer := append([]byte(nil), message[:end+4]...)
					binary.BigEndian.PutUint16(answer[2:4], 0x8180)
					for i := 6; i < 12; i++ {
						answer[i] = 0
					}
					var address []byte
					if !missing && kind == 1 && !ipv6 {
						address = net.ParseIP("127.0.0.1").To4()
					} else if !missing && kind == 28 && ipv6 {
						address = net.ParseIP("::1").To16()
					}
					if missing {
						binary.BigEndian.PutUint16(answer[2:4], 0x8183)
					} else if address != nil {
						binary.BigEndian.PutUint16(answer[6:8], 1)
						answer = append(answer, 0xc0, 0x0c, byte(kind>>8), byte(kind), 0, 1, 0, 0, 0, 60, 0, byte(len(address)))
						answer = append(answer, address...)
					}
					binary.BigEndian.PutUint16(prefix[:], uint16(len(answer)))
					if _, err := conn.Write(append(prefix[:], answer...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), a, aaaa
}

func TestOfficialDNSSettings(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证真实DNS策略/缓存")
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	version := runtimeQAVersion(t)
	if m.Selected() != version {
		t.Fatal("selected core does not match explicit QA version")
	}
	binary, err := m.Binary(version)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name, strategy                string
		noCache, ipv6, missing, local bool
		fallback                      string
		secondary                     bool
	}{
		{name: "ipv4-cached", strategy: "UseIPv4"},
		{name: "ipv4-no-cache", strategy: "UseIPv4", noCache: true},
		{name: "ipv6-only", strategy: "UseIPv6", noCache: true, ipv6: true},
		{name: "dual-stack", strategy: "UseIP", noCache: true},
		{name: "tcp-local", strategy: "UseIPv4", noCache: true, local: true},
		{name: "nxdomain-rejected", strategy: "UseIPv4", noCache: true, missing: true},
		{name: "matched-fallback", strategy: "UseIPv4", noCache: true, fallback: "allow", secondary: true},
		{name: "matched-no-fallback", strategy: "UseIPv4", noCache: true, missing: true, fallback: "disable"},
		{name: "matched-no-fallback-if-match", strategy: "UseIPv4", noCache: true, missing: true, fallback: "if-match"},
		{name: "skipped-fallback", strategy: "UseIPv4", noCache: true, missing: true, fallback: "skip"},
		{name: "matched-server-despite-skip", strategy: "UseIPv4", noCache: true, fallback: "matched-skip", secondary: true},
		{name: "no-match-still-first-server", strategy: "UseIPv4", noCache: true, fallback: "no-match"},
	} {
		t.Run(check.name, func(t *testing.T) {
			primaryMissing := check.missing || (check.fallback != "" && check.fallback != "no-match")
			dnsAddress, a, aaaa := localDNSFixture(t, check.ipv6, primaryMissing)
			var hits atomic.Int64
			origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = io.WriteString(w, "dns-proxy-ok") }))
			_ = origin.Listener.Close()
			address := "127.0.0.1:0"
			if check.ipv6 {
				address = "[::1]:0"
			}
			origin.Listener, err = net.Listen("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			origin.Start()
			defer origin.Close()
			_, originPort, _ := net.SplitHostPort(origin.Listener.Addr().String())
			_, dnsPort, _ := net.SplitHostPort(dnsAddress)
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			rt, err := NewRuntime(m, s, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			cert, key := runtimeCertificate(t, t.TempDir())
			var workspace workspaceDraft
			_ = json.Unmarshal(validWorkspace(), &workspace)
			workspace.Inbounds[0].Preset, workspace.Inbounds[0].Port = "xhttp-tls", runtimePort(t)
			workspace.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/dns-test", Mode: "auto"}
			data, _ := json.Marshal(workspace)
			if _, err := s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
				t.Fatal(err)
			}
			on, off := true, false
			settings := settingsDraft{FreedomStrategy: "ForceIP", RoutingStrategy: "AsIs", ProbeURL: "https://example.test/", Statistics: &on, AccessLog: &off, DNSLog: &off, LogLevel: "warning", Rules: "[]", Balancers: "[]", Advanced: "{}"}
			scheme := "tcp"
			if check.local {
				scheme = "tcp+local"
			}
			settings.DNS = fmt.Sprintf(`{"servers":["%s://%s"],"queryStrategy":%q,"disableCache":%t,"disableFallback":true}`, scheme, dnsAddress, check.strategy, check.noCache)
			secondaryA, secondaryAAAA := &atomic.Int64{}, &atomic.Int64{}
			if check.fallback != "" {
				var secondaryAddress string
				secondaryAddress, secondaryA, secondaryAAAA = localDNSFixture(t, false, false)
				_, secondaryPort, _ := net.SplitHostPort(secondaryAddress)
				dnsPort += "," + secondaryPort
				primary := map[string]any{"address": "tcp://" + dnsAddress, "domains": []string{"full:panel-dns-qa.invalid"}}
				secondary := map[string]any{"address": "tcp://" + secondaryAddress, "skipFallback": check.fallback == "skip" || check.fallback == "matched-skip"}
				servers := []any{secondary, primary}
				if check.fallback == "matched-skip" {
					secondary["domains"] = []string{"full:panel-dns-qa.invalid"}
					servers = []any{primary, secondary}
				}
				if check.fallback == "no-match" {
					primary["domains"] = []string{"full:other-dns-qa.invalid"}
					primary["skipFallback"], secondary["skipFallback"] = true, true
					servers = []any{primary, secondary}
				}
				dns := map[string]any{"servers": servers, "queryStrategy": check.strategy, "disableCache": true, "disableFallback": check.fallback == "disable" || check.fallback == "no-match", "disableFallbackIfMatch": check.fallback == "if-match", "enableParallelQuery": false}
				data, err = json.Marshal(dns)
				if err != nil {
					t.Fatal(err)
				}
				settings.DNS = string(data)
			}
			settings.Outbounds = fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32","::1/128"],"port":"%s,%s"}]}}]`, originPort, dnsPort)
			data, _ = json.Marshal(settings)
			if _, err := s.SaveDocument(context.Background(), "xray-settings", 0, data); err != nil {
				t.Fatal(err)
			}
			rt.mu.Lock()
			candidate, err := rt.candidate(context.Background(), runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1})
			if err == nil {
				err = rt.apply(context.Background(), candidate)
			}
			exported, exportErr := rt.connection(context.Background(), connectionRequest{ClientID: "client-one", WorkspaceRevision: 1})
			rt.mu.Unlock()
			if err != nil || exportErr != nil {
				t.Fatal("apply/export failed", err, exportErr)
			}
			tls := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
			tls["disableSystemRoot"], tls["certificates"] = true, []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
			port := runtimePort(t)
			clientConfig := map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}}
			data, _ = json.Marshal(clientConfig)
			file := filepath.Join(t.TempDir(), "client.json")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			clientEngine := &engine.Engine{}
			defer clientEngine.Close()
			if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binary, File: file, Inbounds: 1}); err != nil {
				t.Fatal(err)
			}
			proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			var first int64
			for request := 0; request < 2; request++ {
				response, requestErr := (&http.Client{Transport: transport, Timeout: 4 * time.Second}).Get("http://panel-dns-qa.invalid:" + originPort)
				var body []byte
				if response != nil {
					body, _ = io.ReadAll(response.Body)
					_ = response.Body.Close()
				}
				if check.missing {
					if requestErr == nil || hits.Load() != 0 {
						t.Fatal("NXDOMAIN unexpectedly reached origin")
					}
				} else if requestErr != nil || string(body) != "dns-proxy-ok" || hits.Load() != int64(request+1) {
					t.Fatal("DNS-selected address did not reach loopback origin")
				}
				count := a.Load() + aaaa.Load()
				if request == 0 {
					first = count
				} else if !check.missing && ((!check.noCache && count != first) || (check.noCache && count <= first)) {
					t.Fatal("cache behavior did not match setting")
				}
			}
			if (check.strategy != "UseIPv6" && a.Load() == 0) || (check.strategy != "UseIPv4" && aaaa.Load() == 0) || (check.strategy == "UseIPv4" && aaaa.Load() != 0) || (check.strategy == "UseIPv6" && a.Load() != 0) {
				t.Fatal("DNS query types did not match strategy")
			}
			if check.fallback != "" && ((secondaryA.Load() > 0) != check.secondary || secondaryAAAA.Load() != 0) {
				t.Fatal("fallback DNS queries did not match selection policy")
			}
		})
	}
}
