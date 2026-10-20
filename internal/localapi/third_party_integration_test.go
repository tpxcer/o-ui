package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 显式传入已核验的第三方程序；不下载、不改系统代理/路由/证书信任。
func verifyThirdPartyClients(t *testing.T, rt *Runtime, preset, link string, subscription []byte, certificate, origin string, wantConnection bool) {
	t.Helper()
	for _, kind := range []string{"hysteria", "mihomo"} {
		binary := os.Getenv("PANEL_CLIENT_QA_" + strings.ToUpper(kind))
		if binary == "" || kind == "hysteria" && preset != "hy2" {
			continue
		}
		t.Run(kind+"-import", func(t *testing.T) {
			info, err := os.Stat(binary)
			if !filepath.IsAbs(binary) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				t.Fatal("third-party QA binary must be an absolute executable file")
			}
			launch := func(t *testing.T, uri string, payload []byte, ca string) (*http.Client, func()) {
				t.Helper()
				root := t.TempDir()
				clientPort := runtimePort(t)
				address := net.JoinHostPort("127.0.0.1", strconv.Itoa(clientPort))
				var settings map[string]any
				var args []string
				controller := ""
				const controllerSecret = "isolated-client-qa-control-only"
				if kind == "hysteria" {
					settings = map[string]any{"server": uri, "lazy": true, "tls": map[string]any{"ca": ca}, "socks5": map[string]any{"listen": address, "disableUDP": true}}
					args = []string{"client", "--disable-update-check", "--log-level", "error", "-c", filepath.Join(root, "client.json")}
				} else {
					controller = net.JoinHostPort("127.0.0.1", strconv.Itoa(runtimePort(t)))
					if err := os.WriteFile(filepath.Join(root, "subscription.txt"), payload, 0600); err != nil {
						t.Fatal("write isolated subscription fixture")
					}
					caPEM, err := os.ReadFile(ca)
					if err != nil {
						t.Fatal("read test CA")
					}
					settings = map[string]any{
						"socks-port": clientPort, "allow-lan": false, "bind-address": "127.0.0.1", "mode": "rule", "log-level": "warning",
						"ipv6": false, "external-controller": controller, "secret": controllerSecret, "geo-auto-update": false, "find-process-mode": "off",
						"dns": map[string]any{"enable": false}, "tun": map[string]any{"enable": false},
						"profile":         map[string]any{"store-selected": false, "store-fake-ip": false},
						"tls":             map[string]any{"custom-certifactes": []string{string(caPEM)}},
						"proxy-providers": map[string]any{"qa": map[string]any{"type": "file", "path": "subscription.txt", "health-check": map[string]any{"enable": false}}},
						"proxy-groups":    []any{map[string]any{"name": "only-qa", "type": "select", "use": []string{"qa"}}},
						"rules":           []string{"MATCH,only-qa"},
					}
					args = []string{"-d", root, "-f", filepath.Join(root, "client.json")}
				}
				data, err := json.Marshal(settings)
				if err != nil || os.WriteFile(filepath.Join(root, "client.json"), data, 0600) != nil {
					t.Fatal("write isolated client fixture")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				command := exec.CommandContext(ctx, binary, args...)
				command.Dir = root
				command.Env = []string{"PATH=/usr/bin:/bin", "HYSTERIA_DISABLE_UPDATE_CHECK=1", "DISABLE_SYSTEM_CA=true", "DISABLE_EMBED_CA=true"}
				command.Stdout, command.Stderr = io.Discard, io.Discard
				if err := command.Start(); err != nil {
					cancel()
					t.Fatal("third-party client could not start")
				}
				finished := make(chan struct{})
				go func() { _ = command.Wait(); close(finished) }()
				proxyURL, _ := url.Parse("socks5://" + address)
				transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
				stop := func() {
					transport.CloseIdleConnections()
					select {
					case <-finished:
					default:
						_ = command.Process.Signal(os.Interrupt)
						select {
						case <-finished:
						case <-time.After(3 * time.Second):
							cancel()
							<-finished
						}
					}
					cancel()
				}
				t.Cleanup(stop)
				deadline := time.Now().Add(5 * time.Second)
				for {
					if c, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
						_ = c.Close()
						break
					}
					select {
					case <-finished:
						t.Fatal("third-party client exited before SOCKS readiness")
					default:
					}
					if time.Now().After(deadline) {
						t.Fatal("third-party SOCKS readiness timeout")
					}
					time.Sleep(25 * time.Millisecond)
				}
				if controller != "" {
					apiTransport := &http.Transport{}
					defer apiTransport.CloseIdleConnections()
					api := &http.Client{Transport: apiTransport, Timeout: 500 * time.Millisecond}
					read := func(path string, result any) bool {
						request, _ := http.NewRequest("GET", "http://"+controller+path, nil)
						request.Header.Set("Authorization", "Bearer "+controllerSecret)
						response, err := api.Do(request)
						if err != nil {
							return false
						}
						defer response.Body.Close()
						return response.StatusCode == 200 && json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(result) == nil
					}
					u, _ := url.Parse(uri)
					deadline := time.Now().Add(5 * time.Second)
					for {
						var provider struct{ Proxies []struct{ Name string } }
						var group struct{ Now string }
						if read("/providers/proxies/qa", &provider) && len(provider.Proxies) == 1 && provider.Proxies[0].Name == u.Fragment && read("/proxies/only-qa", &group) && group.Now == u.Fragment {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("Mihomo did not import and select the exact subscription node")
						}
						time.Sleep(25 * time.Millisecond)
					}
				}
				return &http.Client{Transport: transport, Timeout: 3 * time.Second}, stop
			}
			transfer := func(client *http.Client) bool {
				response, err := client.Get(origin)
				if err != nil {
					return false
				}
				defer response.Body.Close()
				body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
				return err == nil && response.StatusCode == 200 && string(body) == "four-presets-ok"
			}
			usage := func() uint64 {
				t.Helper()
				rt.mu.Lock()
				defer rt.mu.Unlock()
				if err := rt.checkpointCurrent(context.Background()); err != nil {
					t.Fatal("third-party counters unavailable")
				}
				ledger, _, err := rt.ledger(context.Background())
				if err != nil {
					t.Fatal("third-party ledger unavailable")
				}
				return ledger.Totals["client-one"].Uplink + ledger.Totals["client-one"].Downlink
			}
			before := usage()
			client, stop := launch(t, link, subscription, certificate)
			if !wantConnection {
				if transfer(client) {
					t.Fatal("official default minimum client version was bypassed")
				}
				stop()
				if usage() > before {
					t.Fatal("rejected third-party client transferred authenticated application data")
				}
				return
			}
			deadline := time.Now().Add(5 * time.Second)
			for !transfer(client) {
				if time.Now().After(deadline) {
					t.Fatal("third-party imported configuration did not connect")
				}
				time.Sleep(50 * time.Millisecond)
			}
			stop()
			deadline = time.Now().Add(3 * time.Second)
			for usage() <= before {
				if time.Now().After(deadline) {
					t.Fatal("third-party request bypassed Xray or was not counted")
				}
				time.Sleep(50 * time.Millisecond)
			}
			failures := []string{"wrong-auth", "wrong-sni", "untrusted-ca"}
			if strings.Contains(preset, "reality") {
				failures = []string{"wrong-auth", "wrong-reality-key", "wrong-short-id"}
			}
			for _, failure := range failures {
				t.Run(failure, func(t *testing.T) {
					u, _ := url.Parse(link)
					ca := certificate
					switch failure {
					case "wrong-auth":
						auth := "00000000-0000-4000-8000-000000000001"
						if preset == "hy2" {
							auth = "fictional-wrong-third-party-auth"
						}
						u.User = url.User(auth)
					case "wrong-sni":
						query := u.Query()
						query.Set("sni", "untrusted-qa.invalid")
						u.RawQuery = query.Encode()
					case "untrusted-ca":
						ca, _ = runtimeCertificate(t, t.TempDir())
					case "wrong-reality-key", "wrong-short-id":
						query := u.Query()
						if failure == "wrong-reality-key" {
							query.Set("pbk", "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE")
						} else {
							query.Set("sid", "0000000000000001")
						}
						u.RawQuery = query.Encode()
					}
					badLink := u.String()
					badSubscription := []byte(base64.StdEncoding.EncodeToString([]byte(badLink + "\n")))
					bad, finish := launch(t, badLink, badSubscription, ca)
					defer finish()
					if transfer(bad) {
						t.Fatal("third-party accepted invalid authentication or TLS")
					}
				})
			}
		})
	}
}
