package localapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// 只保留TLS错误类别计数，不缓存或打印握手日志原文。
type dohTLSAudit struct {
	hellos, rejected, other, requests atomic.Int64
}

func (a *dohTLSAudit) Write(p []byte) (int, error) {
	message := string(p)
	if strings.Contains(message, "remote error: tls: bad certificate") || strings.Contains(message, "remote error: tls: unknown certificate authority") {
		a.rejected.Add(1)
	} else {
		a.other.Add(1)
	}
	return len(p), nil
}

// 自有HTTP2 DoH仅转交固定.invalid域名的本机DNS夹具，不访问外站。
func localDoHFixture(t *testing.T) (*httptest.Server, *dohTLSAudit) {
	t.Helper()
	dnsAddress, a, aaaa := localDNSFixture(t, false, false)
	audit := &dohTLSAudit{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		audit.requests.Add(1)
		if r.Method != "POST" || r.URL.Path != "/dns-query" || r.ProtoMajor != 2 || r.TLS == nil || r.Header.Get("Content-Type") != "application/dns-message" {
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil || len(body) < 17 {
			http.Error(w, "invalid test query", http.StatusBadRequest)
			return
		}
		conn, err := net.DialTimeout("tcp", dnsAddress, time.Second)
		if err != nil {
			http.Error(w, "test DNS unavailable", http.StatusBadGateway)
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var prefix [2]byte
		binary.BigEndian.PutUint16(prefix[:], uint16(len(body)))
		if _, err = conn.Write(append(prefix[:], body...)); err != nil {
			return
		}
		if _, err = io.ReadFull(conn, prefix[:]); err != nil {
			return
		}
		size := int(binary.BigEndian.Uint16(prefix[:]))
		if size < 17 || size > 4096 {
			return
		}
		answer := make([]byte, size)
		if _, err = io.ReadFull(conn, answer); err != nil {
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(answer)
	}))
	cert, key := runtimeCertificate(t, t.TempDir(), "127.0.0.1")
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		audit.hellos.Add(1)
		return nil, nil
	}}
	server.Config.ErrorLog = log.New(audit, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)

	// 先用只信此测试CA的客户端证实HTTP2/证书名/真实DNS应答有效。
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(cert)
	if err != nil || !pool.AppendCertsFromPEM(pem) {
		t.Fatal("test CA unavailable")
	}
	transport := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}}
	defer transport.CloseIdleConnections()
	query := make([]byte, 12)
	binary.BigEndian.PutUint16(query[2:4], 0x0100)
	binary.BigEndian.PutUint16(query[4:6], 1)
	for _, label := range strings.Split("panel-dns-qa.invalid", ".") {
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 1, 0, 1)
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Post(server.URL+"/dns-query", "application/dns-message", bytes.NewReader(query))
	if err != nil {
		t.Fatal("trusted test client did not reach DoH fixture")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != 2 || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || len(body) < 16 || binary.BigEndian.Uint16(body[6:8]) != 1 || !bytes.Equal(body[len(body)-4:], net.ParseIP("127.0.0.1").To4()) || a.Load() != 1 || aaaa.Load() != 0 {
		t.Fatal("trusted HTTP2 DoH fixture self-check failed")
	}
	if audit.hellos.Load() != 1 || audit.requests.Load() != 1 || audit.rejected.Load() != 0 || audit.other.Load() != 0 {
		t.Fatal("unexpected self-check TLS or request counters")
	}
	return server, audit
}

func TestLocalDoHFixture(t *testing.T) {
	localDoHFixture(t)
}

func TestOfficialDoHCertificateRejection(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证真实DoH不可信证书拒绝与回退")
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
	for _, check := range []struct {
		name, scheme string
		fallback     bool
	}{
		{"routed-rejected-no-fallback", "https", false},
		{"local-rejected-no-fallback", "https+local", false},
		{"routed-rejected-tcp-fallback", "https", true},
		{"local-rejected-tcp-fallback", "https+local", true},
	} {
		t.Run(check.name, func(t *testing.T) {
			doh, audit := localDoHFixture(t)
			fallbackAddress, a, aaaa := localDNSFixture(t, false, false)
			var hits atomic.Int64
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, "doh-fallback-ok")
			}))
			defer origin.Close()
			_, originPort, _ := net.SplitHostPort(origin.Listener.Addr().String())
			_, dohPort, _ := net.SplitHostPort(doh.Listener.Addr().String())
			_, fallbackPort, _ := net.SplitHostPort(fallbackAddress)
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
			workspace.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/doh-test", Mode: "auto"}
			data, _ := json.Marshal(workspace)
			if _, err := s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
				t.Fatal(err)
			}
			on, off := true, false
			settings := settingsDraft{FreedomStrategy: "ForceIP", RoutingStrategy: "AsIs", ProbeURL: "https://example.test/", Statistics: &on, AccessLog: &off, DNSLog: &off, LogLevel: "warning", Rules: "[]", Balancers: "[]", Advanced: "{}"}
			dns := map[string]any{
				"servers":       []any{map[string]any{"address": check.scheme + "://" + doh.Listener.Addr().String() + "/dns-query", "domains": []string{"full:panel-dns-qa.invalid"}}, "tcp://" + fallbackAddress},
				"queryStrategy": "UseIPv4", "disableCache": true, "disableFallback": !check.fallback, "enableParallelQuery": false,
			}
			data, _ = json.Marshal(dns)
			settings.DNS = string(data)
			settings.Outbounds = fmt.Sprintf(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":"tcp","ip":["127.0.0.1/32"],"port":"%s,%s,%s"}]}}]`, originPort, dohPort, fallbackPort)
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
			tlsSettings := exported.Outbound["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
			tlsSettings["disableSystemRoot"], tlsSettings["certificates"] = true, []any{map[string]string{"certificateFile": cert, "usage": "verify"}}
			port := runtimePort(t)
			clientConfig := map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{exported.Outbound}}
			data, _ = json.Marshal(clientConfig)
			file := filepath.Join(t.TempDir(), "client.json")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			clientEngine := &engine.Engine{}
			defer clientEngine.Close()
			if err := clientEngine.Apply(context.Background(), engine.Candidate{Binary: binaryPath, File: file, Inbounds: 1}); err != nil {
				t.Fatal(err)
			}
			proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			for request := 0; request < 2; request++ {
				response, requestErr := (&http.Client{Transport: transport, Timeout: 4 * time.Second}).Get("http://panel-dns-qa.invalid:" + originPort)
				var body []byte
				if response != nil {
					body, _ = io.ReadAll(response.Body)
					_ = response.Body.Close()
				}
				if check.fallback {
					if requestErr != nil || string(body) != "doh-fallback-ok" || hits.Load() != int64(request+1) || a.Load() < int64(request+1) {
						t.Fatal("allowed TCP fallback did not reach origin")
					}
				} else if requestErr == nil || hits.Load() != 0 || a.Load() != 0 {
					t.Fatal("untrusted DoH bypassed disabled fallback")
				}
				deadline := time.Now().Add(time.Second)
				for audit.rejected.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				// 重复业务请求不保证新建TLS；检查真实拒绝证据与持续无HTTPS查询。
				if audit.hellos.Load() < 2 || audit.rejected.Load() == 0 || audit.requests.Load() != 1 || audit.other.Load() != 0 || aaaa.Load() != 0 {
					t.Fatalf("DoH rejection invariant: request=%d hellos=%d rejected=%d requests=%d other=%d fallbackA=%d fallbackAAAA=%d", request, audit.hellos.Load(), audit.rejected.Load(), audit.requests.Load(), audit.other.Load(), a.Load(), aaaa.Load())
				}
			}
		})
	}
}
