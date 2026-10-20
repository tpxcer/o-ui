package localapi

import (
	"context"
	"encoding/base64"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 独立HTTP/SOCKS5夹具只允许精确临时目标；凭据与数据均为隔离测试专用。
func TestOfficialCommonOutbounds(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证常用出站真实连接")
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
	binaryPath, err := m.Binary(version)
	if err != nil {
		t.Fatal(err)
	}
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
	var hits, httpAttempts, socksAttempts, httpForward, socksForward atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "common-outbound-ok")
	}))
	defer origin.Close()
	destination := strings.TrimPrefix(origin.URL, "http://")
	const user, pass = "outbound-test-user", "outbound-test-passphrase"
	bridge := func(connection net.Conn, reader io.Reader, writer io.Writer, counter *atomic.Int64) {
		upstream, dialErr := net.DialTimeout("tcp", destination, time.Second)
		if dialErr != nil {
			return
		}
		defer upstream.Close()
		_ = upstream.SetDeadline(time.Now().Add(6 * time.Second))
		counter.Add(1)
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, reader); close(done) }()
		_, _ = io.Copy(writer, upstream)
		_ = connection.Close()
		_ = upstream.Close()
		<-done
	}
	httpProxy := func(auth bool) int {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "CONNECT" || r.Host != destination {
				http.Error(w, "blocked", 403)
				return
			}
			httpAttempts.Add(1)
			if auth && r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)) {
				http.Error(w, "test auth required", 407)
				return
			}
			connection, buffer, hijackErr := w.(http.Hijacker).Hijack()
			if hijackErr != nil {
				return
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(6 * time.Second))
			_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			if buffer.Flush() != nil {
				return
			}
			bridge(connection, buffer, connection, &httpForward)
		}))
		t.Cleanup(server.Close)
		return server.Listener.Addr().(*net.TCPAddr).Port
	}
	socksProxy := func(auth bool) int {
		listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			t.Fatal(listenErr)
		}
		var workers sync.WaitGroup
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer connection.Close()
					_ = connection.SetDeadline(time.Now().Add(6 * time.Second))
					var header [4]byte
					if _, e := io.ReadFull(connection, header[:2]); e != nil || header[0] != 5 {
						return
					}
					methods := make([]byte, int(header[1]))
					if _, e := io.ReadFull(connection, methods); e != nil {
						return
					}
					socksAttempts.Add(1)
					method := byte(0)
					if auth {
						method = 2
					}
					found := false
					for _, offered := range methods {
						if offered == method {
							found = true
						}
					}
					if !found {
						_, _ = connection.Write([]byte{5, 255})
						return
					}
					if _, e := connection.Write([]byte{5, method}); e != nil {
						return
					}
					if auth {
						if _, e := io.ReadFull(connection, header[:2]); e != nil || header[0] != 1 {
							return
						}
						name := make([]byte, int(header[1]))
						if _, e := io.ReadFull(connection, name); e != nil {
							return
						}
						if _, e := io.ReadFull(connection, header[:1]); e != nil {
							return
						}
						password := make([]byte, int(header[0]))
						if _, e := io.ReadFull(connection, password); e != nil {
							return
						}
						if string(name) != user || string(password) != pass {
							_, _ = connection.Write([]byte{1, 1})
							return
						}
						if _, e := connection.Write([]byte{1, 0}); e != nil {
							return
						}
					}
					if _, e := io.ReadFull(connection, header[:]); e != nil || header[0] != 5 || header[1] != 1 || header[2] != 0 || header[3] != 1 {
						return
					}
					var address [6]byte
					if _, e := io.ReadFull(connection, address[:]); e != nil {
						return
					}
					target := net.JoinHostPort(net.IP(address[:4]).String(), strconv.Itoa(int(binary.BigEndian.Uint16(address[4:]))))
					if target != destination {
						_, _ = connection.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
						return
					}
					if _, e := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); e != nil {
						return
					}
					bridge(connection, connection, connection, &socksForward)
				}()
			}
		}()
		t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
		return listener.Addr().(*net.TCPAddr).Port
	}
	httpOpen, httpAuth, socksOpen, socksAuth := httpProxy(false), httpProxy(true), socksProxy(false), socksProxy(true)
	cert, key := runtimeCertificate(t, t.TempDir())
	var workspace workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &workspace)
	workspace.Inbounds[0].Preset = "xhttp-tls"
	workspace.Inbounds[0].Port = runtimePort(t)
	workspace.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/outbound-test", Mode: "auto"}
	data, _ := json.Marshal(workspace)
	if _, err = s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	settings := settingsDraft{FreedomStrategy: "AsIs", RoutingStrategy: "AsIs", ProbeURL: "https://example.test/", LogLevel: "warning", Statistics: &on, AccessLog: &off, DNSLog: &off, DNS: `{"servers":["localhost"]}`, Advanced: "{}", Balancers: "[]"}
	var revision int64
	for _, check := range []struct {
		name, protocol             string
		port                       int
		auth, bad, routed, blocked bool
	}{
		{"http-default-no-auth", "http", httpOpen, false, false, false, false},
		{"http-route-with-auth", "http", httpAuth, true, false, true, false},
		{"http-wrong-password-rejected", "http", httpAuth, true, true, true, false},
		{"socks5-default-no-auth", "socks", socksOpen, false, false, false, false},
		{"socks5-route-with-auth", "socks", socksAuth, true, false, true, false},
		{"socks5-wrong-password-rejected", "socks", socksAuth, true, true, true, false},
		{"block-default", "http", httpOpen, false, false, false, true},
		{"block-rule-overrides-default-proxy", "http", httpOpen, false, false, true, true},
	} {
		t.Run(check.name, func(t *testing.T) {
			server := map[string]any{"address": "127.0.0.1", "port": check.port}
			if check.auth {
				password := pass
				if check.bad {
					password = "wrong-test-pass"
				}
				server["users"] = []any{map[string]string{"user": user, "pass": password}}
			}
			proxy := map[string]any{"tag": "proxy", "protocol": check.protocol, "settings": map[string]any{"servers": []any{server}}}
			block := map[string]string{"tag": "block", "protocol": "blackhole"}
			outbounds := []any{proxy, block}
			settings.Rules = "[]"
			if check.routed {
				settings.Rules = `[{"type":"field","network":"tcp","outboundTag":"proxy"}]`
				outbounds = []any{block, proxy}
			}
			if check.blocked {
				if check.routed {
					outbounds = []any{proxy, block}
					settings.Rules = `[{"type":"field","network":"tcp","outboundTag":"block"}]`
				} else {
					outbounds = []any{block, proxy}
				}
			}
			encoded, _ := json.Marshal(outbounds)
			settings.Outbounds = string(encoded)
			encoded, _ = json.Marshal(settings)
			doc, saveErr := s.SaveDocument(context.Background(), "xray-settings", revision, encoded)
			if saveErr != nil {
				t.Fatal(saveErr)
			}
			revision = doc.Revision
			rt.mu.Lock()
			candidate, applyErr := rt.candidate(context.Background(), runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: revision})
			if applyErr == nil {
				applyErr = rt.apply(context.Background(), candidate)
			}
			exported, exportErr := rt.connection(context.Background(), connectionRequest{ClientID: "client-one", WorkspaceRevision: 1})
			rt.mu.Unlock()
			if applyErr != nil || exportErr != nil {
				t.Fatal("apply/export failed", applyErr, exportErr)
			}
			tls := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
			tls["disableSystemRoot"] = true
			tls["certificates"] = []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
			port := runtimePort(t)
			clientConfig := map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}}
			file := filepath.Join(t.TempDir(), "client.json")
			encoded, _ = json.Marshal(clientConfig)
			if err := os.WriteFile(file, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			clientEngine := &engine.Engine{}
			defer clientEngine.Close()
			if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binaryPath, Version: version, File: file, Inbounds: 1}); err != nil {
				t.Fatal(err)
			}
			proxyURL, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			before, beforeHTTP, beforeSOCKS := hits.Load(), httpForward.Load(), socksForward.Load()
			attemptHTTP, attemptSOCKS := httpAttempts.Load(), socksAttempts.Load()
			response, requestErr := client.Get(origin.URL)
			var body []byte
			if response != nil {
				body, _ = io.ReadAll(response.Body)
				_ = response.Body.Close()
			}
			if check.bad || check.blocked {
				if requestErr == nil || hits.Load() != before || httpForward.Load() != beforeHTTP || socksForward.Load() != beforeSOCKS {
					t.Fatal("rejected request reached a forwarding path")
				}
				if check.blocked && (httpAttempts.Load() != attemptHTTP || socksAttempts.Load() != attemptSOCKS) {
					t.Fatal("blocked request tried proxy")
				}
				if check.bad && (check.protocol == "http" && httpAttempts.Load() <= attemptHTTP || check.protocol == "socks" && socksAttempts.Load() <= attemptSOCKS) {
					t.Fatal("wrong auth rejected without trying selected proxy")
				}
				return
			}
			if requestErr != nil || string(body) != "common-outbound-ok" || hits.Load() != before+1 {
				t.Fatal("expected real response through selected outbound")
			}
			if check.protocol == "http" && (httpForward.Load() != beforeHTTP+1 || socksForward.Load() != beforeSOCKS) || check.protocol == "socks" && (socksForward.Load() != beforeSOCKS+1 || httpForward.Load() != beforeHTTP) {
				t.Fatal("response did not use selected proxy exclusively")
			}
		})
	}
}
