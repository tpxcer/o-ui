package localapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 此项必须显式开启；仅向用户已授权的1.1.1.1 DoH查询example.com。
func TestOfficialExternalDoH(t *testing.T) {
	if os.Getenv("PANEL_DOH_EXTERNAL_QA") != "1" {
		t.Skip("需显式PANEL_DOH_EXTERNAL_QA=1和已授权固定Cloudflare/example.com外部查询")
	}
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Fatal("external DoH QA requires verified official core directory")
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Selected() != "26.7.28" {
		t.Fatal("requires fixed default 26.7.28")
	}
	binaryPath, err := m.Binary("26.7.28")
	if err != nil {
		t.Fatal(err)
	}
	for _, scheme := range []string{"https", "https+local"} {
		t.Run(scheme, func(t *testing.T) {
			var tunnels, tlsStarts, otherTargets atomic.Int64
			tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Host != "1.1.1.1:443" {
					otherTargets.Add(1)
					http.Error(w, "not authorized", http.StatusForbidden)
					return
				}
				tunnels.Add(1)
				upstream, err := net.DialTimeout("tcp", "1.1.1.1:443", 4*time.Second)
				if err != nil {
					http.Error(w, "DoH unreachable", http.StatusBadGateway)
					return
				}
				defer upstream.Close()
				_ = upstream.SetDeadline(time.Now().Add(20 * time.Second))
				conn, buffer, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
				_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
				if buffer.Flush() != nil {
					return
				}
				var header [5]byte
				if _, err = io.ReadFull(buffer, header[:]); err != nil || header[0] != 22 || header[1] != 3 {
					return
				}
				tlsStarts.Add(1)
				if _, err = upstream.Write(header[:]); err != nil {
					return
				}
				done := make(chan struct{})
				go func() { _, _ = io.Copy(upstream, buffer); close(done) }()
				_, _ = io.Copy(conn, upstream)
				_ = conn.Close()
				_ = upstream.Close()
				<-done
			}))
			defer tunnel.Close()
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
			defer func() {
				if t.Failed() {
					rt.mu.Lock()
					issue := rt.issue
					rt.mu.Unlock()
					t.Logf("DoH route counters: tunnels=%d TLS=%d other=%d; issue=%s; diagnostics=%+v", tunnels.Load(), tlsStarts.Load(), otherTargets.Load(), issue, rt.logs.Snapshot())
				}
			}()
			cert, key := runtimeCertificate(t, t.TempDir())
			var workspace workspaceDraft
			_ = json.Unmarshal(validWorkspace(), &workspace)
			workspace.Inbounds[0].Preset, workspace.Inbounds[0].Port = "xhttp-tls", runtimePort(t)
			workspace.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/external-doh-test", Mode: "auto"}
			data, _ := json.Marshal(workspace)
			if _, err := s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
				t.Fatal(err)
			}
			on, off := true, false
			dnsPort := runtimePort(t)
			settings := settingsDraft{FreedomStrategy: "AsIs", RoutingStrategy: "AsIs", ProbeURL: "https://example.test/", Statistics: &on, AccessLog: &off, DNSLog: &off, LogLevel: "warning", Balancers: "[]", Advanced: "{}"}
			settings.DNS = fmt.Sprintf(`{"servers":["%s://1.1.1.1/dns-query"],"queryStrategy":"UseIP","disableCache":true,"disableFallback":true}`, scheme)
			settings.Rules = fmt.Sprintf(`[{"type":"field","ip":["127.0.0.1/32"],"port":"%d","outboundTag":"test-dns"},{"type":"field","ip":["1.1.1.1/32"],"port":"443","outboundTag":"test-cf-tunnel"}]`, dnsPort)
			settings.Outbounds = fmt.Sprintf(`[{"tag":"block","protocol":"blackhole","settings":{}},{"tag":"test-dns","protocol":"dns","settings":{"rules":[{"action":"hijack","qType":"1,28","domain":["full:example.com"]},{"action":"return","rCode":5}]}},{"tag":"test-cf-tunnel","protocol":"http","settings":{"servers":[{"address":"127.0.0.1","port":%d}]}}]`, tunnel.Listener.Addr().(*net.TCPAddr).Port)
			data, _ = json.Marshal(settings)
			if err := validateSettingsDraft(data); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SaveDocument(context.Background(), "xray-settings", 0, data); err != nil {
				t.Fatal(err)
			}
			rt.mu.Lock()
			candidate, err := rt.candidate(context.Background(), runtimeRequest{Version: "26.7.28", WorkspaceRevision: 1, SettingsRevision: 1})
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
			for _, kind := range []uint16{1, 28} {
				t.Run(fmt.Sprintf("type-%d", kind), func(t *testing.T) {
					query := []byte{0x17, 0x67, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, byte(kind >> 8), byte(kind), 0, 1}
					answer := queryDNSViaSOCKS(t, port, dnsPort, query)
					if len(answer) < len(query)+16 || binary.BigEndian.Uint16(answer[:2]) != 0x1767 || answer[2]&0x80 == 0 || answer[3]&15 != 0 || binary.BigEndian.Uint16(answer[4:6]) != 1 || binary.BigEndian.Uint16(answer[6:8]) == 0 {
						t.Fatal("official DoH returned no successful DNS answer")
					}
					offset := len(query)
					if !bytes.Equal(answer[12:offset], query[12:]) || answer[offset]&0xc0 != 0xc0 {
						t.Fatal("DNS answer did not match the fixed query")
					}
					size := 4
					if kind == 28 {
						size = 16
					}
					if len(answer) < offset+12+size || binary.BigEndian.Uint16(answer[offset+2:offset+4]) != kind || binary.BigEndian.Uint16(answer[offset+4:offset+6]) != 1 || int(binary.BigEndian.Uint16(answer[offset+10:offset+12])) != size {
						t.Fatal("DNS answer record family or size mismatch")
					}
					if ip := net.IP(answer[offset+12 : offset+12+size]); ip.IsUnspecified() || ip.IsLoopback() {
						t.Fatal("external DoH unexpectedly resolved to a loopback target")
					}
					if otherTargets.Load() != 0 || (scheme == "https" && (tunnels.Load() == 0 || tlsStarts.Load() == 0)) || (scheme == "https+local" && tunnels.Load() != 0) {
						t.Fatal("DoH traffic did not match the exact authorized route")
					}
				})
			}
		})
	}
}

func queryDNSViaSOCKS(t *testing.T, proxyPort, dnsPort int, query []byte) []byte {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), time.Second)
	if err != nil {
		t.Fatal("test SOCKS unavailable")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var auth [2]byte
	if _, err = io.ReadFull(conn, auth[:]); err != nil || auth != [2]byte{5, 0} {
		t.Fatal("test SOCKS greeting failed")
	}
	if _, err = conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, byte(dnsPort >> 8), byte(dnsPort)}); err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	if _, err = io.ReadFull(conn, header[:]); err != nil || header[0] != 5 || header[1] != 0 {
		t.Fatal("test SOCKS connect failed")
	}
	size := 0
	switch header[3] {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		var count [1]byte
		if _, err = io.ReadFull(conn, count[:]); err != nil {
			t.Fatal(err)
		}
		size = int(count[0])
	default:
		t.Fatal("invalid test SOCKS reply")
	}
	if _, err = io.CopyN(io.Discard, conn, int64(size+2)); err != nil {
		t.Fatal(err)
	}
	var prefix [2]byte
	binary.BigEndian.PutUint16(prefix[:], uint16(len(query)))
	if _, err = conn.Write(append(prefix[:], query...)); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(conn, prefix[:]); err != nil {
		t.Fatalf("official DoH DNS response unavailable: %T", err)
	}
	size = int(binary.BigEndian.Uint16(prefix[:]))
	if size < 12 || size > 4096 {
		t.Fatal("invalid test DNS response size")
	}
	answer := make([]byte, size)
	if _, err = io.ReadFull(conn, answer); err != nil {
		t.Fatal("incomplete official DoH DNS response")
	}
	return answer
}
