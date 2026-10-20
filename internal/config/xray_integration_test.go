package config

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 显式选择已校验的官方核心后才运行；所有证书、凭据与监听均为本机隔离测试。
func TestOfficialXrayXHTTPTLS(t *testing.T) {
	binary := os.Getenv("PANEL_XRAY_INTEGRATION")
	if binary == "" {
		t.Skip("设置PANEL_XRAY_INTEGRATION后执行官方核心本机连接验证")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("integration binary must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "Xray 26.7.28 ") {
		t.Fatal("integration requires official default 26.7.28")
	}
	dir := t.TempDir()
	cert, key := integrationCertificate(t, dir)
	uuidBytes := make([]byte, 16)
	if _, err = rand.Read(uuidBytes); err != nil {
		t.Fatal(err)
	}
	uuidBytes[6] = (uuidBytes[6] & 15) | 64
	uuidBytes[8] = (uuidBytes[8] & 63) | 128
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", uuidBytes[:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:])
	serverPort, clientPort := integrationPort(t), integrationPort(t)
	if serverPort == clientPort {
		t.Fatal("ephemeral ports collided; rerun test")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "independent-panel-loopback-ok")
	}))
	defer origin.Close()
	originAddress := strings.TrimPrefix(origin.URL, "http://")
	_, originPort, _ := net.SplitHostPort(originAddress)
	compiled, err := CompileInbounds([]Inbound{{ID: "smoke", Preset: "xhttp-tls", Listen: "127.0.0.1", Port: serverPort, Enabled: true, Certificate: &Certificate{cert, key}, XHTTP: &XHTTP{Path: "/local-smoke", Mode: "auto"}, Sniffing: Sniffing{}}}, []Client{{ID: "smoke-client", InboundID: "smoke", Enabled: true, UUID: uuid}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 官方默认阻止私网出站；测试只放行临时HTTP目标的确切回环IP/端口，不改变产品默认保护。
	serverConfig := map[string]any{"log": map[string]string{"loglevel": "info", "access": "none"}, "inbounds": compiled, "outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "network": "tcp", "ip": []string{"127.0.0.1/32"}, "port": originPort}}}}}}
	serverFile := integrationConfig(t, dir, "server", serverConfig)
	clientConfig := func(identity string) map[string]any {
		return map[string]any{
			"log":       map[string]string{"loglevel": "info", "access": "none"},
			"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
			"outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": serverPort, "users": []any{map[string]any{"id": identity, "encryption": "none"}}}}}, "streamSettings": map[string]any{"network": "xhttp", "security": "tls", "xhttpSettings": map[string]any{"path": "/local-smoke", "mode": "auto"}, "tlsSettings": map[string]any{"serverName": "panel-smoke.test", "alpn": []string{"h2"}, "disableSystemRoot": true, "certificates": []any{map[string]any{"certificateFile": cert, "usage": "verify"}}}}}},
		}
	}
	clientFile := integrationConfig(t, dir, "client", clientConfig(uuid))
	for _, path := range []string{serverFile, clientFile} {
		if err := exec.CommandContext(ctx, binary, "run", "-test", "-config", path).Run(); err != nil {
			t.Fatal("official config preflight failed", filepath.Base(path), err)
		}
	}
	stopServer := integrationStart(t, ctx, binary, serverFile, serverPort)
	stopClient := integrationStart(t, ctx, binary, clientFile, clientPort)
	if err := integrationRequest(ctx, clientPort, originAddress, true); err != nil {
		t.Fatal("trusted TLS proxy round trip failed", err)
	}
	stopClient()
	wrong := integrationConfig(t, dir, "wrong-client", clientConfig("00000000-0000-4000-8000-000000000001"))
	stopWrong := integrationStart(t, ctx, binary, wrong, clientPort)
	if err := integrationRequest(ctx, clientPort, originAddress, true); err == nil {
		t.Fatal("invalid proxy credential was accepted")
	}
	stopWrong()
	badCertificate := clientConfig(uuid)
	badCertificate["outbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)["serverName"] = "wrong-name.test"
	stopBadTLS := integrationStart(t, ctx, binary, integrationConfig(t, dir, "wrong-tls", badCertificate), clientPort)
	if err := integrationRequest(ctx, clientPort, originAddress, true); err == nil {
		t.Fatal("mismatched TLS server name was accepted")
	}
	stopBadTLS()
	stopServer()
	for _, port := range []int{serverPort, clientPort} {
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			_ = connection.Close()
			t.Fatal("test core listener survived stop")
		}
	}
}

func integrationCertificate(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "panel-smoke.test"}, DNSNames: []string{"panel-smoke.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "private.pem")
	if err = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func integrationPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func integrationConfig(t *testing.T, dir, name string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func integrationStart(t *testing.T, ctx context.Context, binary, path string, port int) func() {
	t.Helper()
	command := exec.CommandContext(ctx, binary, "run", "-config", path)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			t.Log(filepath.Base(path), output.String())
		}
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			stopped = true
			t.Fatal("test core exited before ready", filepath.Base(path))
		case <-deadline.C:
			t.Fatal("test core startup timed out", filepath.Base(path))
		case <-tick.C:
			connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if err == nil {
				_ = connection.Close()
				return stop
			}
		}
	}
}

func integrationRequest(ctx context.Context, port int, address string, wantResponse bool) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = connection.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	hello := make([]byte, 2)
	if _, err = io.ReadFull(connection, hello); err != nil {
		return err
	}
	if hello[0] != 5 || hello[1] != 0 {
		return fmt.Errorf("SOCKS greeting rejected")
	}
	host, service, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	targetPort, err := net.LookupPort("tcp", service)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return fmt.Errorf("test destination must be IPv4")
	}
	packet := append([]byte{5, 1, 0, 1}, ip...)
	packet = append(packet, byte(targetPort>>8), byte(targetPort))
	if _, err = connection.Write(packet); err != nil {
		return err
	}
	reply := make([]byte, 10)
	if _, err = io.ReadFull(connection, reply); err != nil {
		return err
	}
	if reply[1] != 0 {
		return fmt.Errorf("SOCKS connect rejected")
	}
	if _, err = fmt.Fprintf(connection, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", address); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		return fmt.Errorf("read HTTP response: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return err
	}
	if wantResponse && !strings.Contains(string(data), "independent-panel-loopback-ok") {
		return fmt.Errorf("proxy did not return expected local response")
	}
	return nil
}
