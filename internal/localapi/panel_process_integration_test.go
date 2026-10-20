package localapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/store"
)

func TestPanelListenerConflict(t *testing.T) {
	if os.Getenv("PANEL_PROCESS_QA") != "1" {
		t.Skip("设置PANEL_PROCESS_QA=1后验证实际面板端口占用退出")
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := processQABinary(t, project)
	existing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "existing-service-unchanged")
	}))
	defer existing.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-listen", strings.TrimPrefix(existing.URL, "http://"), "-state", t.TempDir(), "-data", t.TempDir(), "-dist", filepath.Join(project, "dist"))
	command.Stdout, command.Stderr = io.Discard, io.Discard
	err = command.Run()
	if ctx.Err() != nil {
		t.Fatal("occupied-listener startup did not exit promptly")
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatal("occupied-listener startup must report failure", err)
	}
	response, err := existing.Client().Get(existing.URL)
	if err != nil {
		t.Fatal("startup failure disrupted existing listener", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "existing-service-unchanged" {
		t.Fatal("existing service changed after startup failure")
	}
}

func processQABinary(t *testing.T, project string) string {
	t.Helper()
	if binary := os.Getenv("PANEL_PROCESS_QA_BINARY"); binary != "" {
		info, err := os.Stat(binary)
		if !filepath.IsAbs(binary) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			t.Fatal("PANEL_PROCESS_QA_BINARY must be an absolute executable file")
		}
		return binary
	}
	binary := filepath.Join(t.TempDir(), "panel")
	build := exec.Command("go", "build", "-race", "-o", binary, "./cmd/o-ui")
	build.Dir = project
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build panel: %v\n%s", err, output)
	}
	return binary
}

// 只启动测试面板与临时数据，不使用用户账号、核心目录或系统证书信任。
func TestPanelHTTPSProcessLifecycle(t *testing.T) {
	if os.Getenv("PANEL_PROCESS_QA") != "1" {
		t.Skip("设置PANEL_PROCESS_QA=1后验证实际面板HTTPS重启")
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := processQABinary(t, project)
	stateRoot, coreRoot := t.TempDir(), t.TempDir()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(runtimePort(t)))
	base := "http://" + address
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 2 * time.Second}
	var command *exec.Cmd
	var done chan struct{}
	var processErr error
	stop := func() {
		t.Helper()
		if command == nil {
			return
		}
		select {
		case <-done:
		default:
			_ = command.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Error("panel did not stop normally")
			}
		}
		command = nil
	}
	defer stop()
	launch := func() {
		t.Helper()
		command = exec.Command(binary, "-listen", address, "-state", stateRoot, "-data", coreRoot, "-dist", filepath.Join(project, "dist"))
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done = make(chan struct{})
		current, finished := command, done
		go func() { processErr = current.Wait(); close(finished) }()
	}
	ready := func() {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			select {
			case <-done:
				t.Fatal("panel exited before ready", processErr)
			default:
			}
			response, err := client.Get(base + "/api/auth/status")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("panel readiness timeout")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	call := func(method, path string, value any, want int) ([]byte, []*http.Cookie) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest(method, base+path, bytes.NewReader(data))
		request.Header.Set("Origin", base)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("panel request failed", method, path, err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != want || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("panel API status/boundary mismatch", method, path, response.StatusCode, err)
		}
		return body, response.Cookies()
	}
	credentials := map[string]string{"username": "processqa", "password": "process-only-test-passphrase"}
	launch()
	ready()
	call("GET", "/api/panel/tls", nil, 401)
	_, cookies := call("POST", "/api/auth/setup", credentials, 200)
	if len(cookies) != 1 || cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("HTTP test session cookie invalid")
	}
	call("PUT", "/api/workspace", store.Document{Data: validWorkspace()}, 200)
	cert, key := runtimeCertificate(t, t.TempDir(), "localhost", "127.0.0.1", "panel-process-qa.invalid")
	call("PUT", "/api/panel/tls", map[string]any{"revision": 0, "data": panelTLSDraft{cert, key}}, 200)
	checkStatus := func(active bool) {
		t.Helper()
		body, _ := call("GET", "/api/panel/tls", nil, 200)
		var status struct {
			Active                   bool
			Revision, ActiveRevision int64
		}
		if json.Unmarshal(body, &status) != nil || status.Active != active || status.Revision != 1 || (active && status.ActiveRevision != 1) || (!active && status.ActiveRevision != 0) {
			t.Fatal("saved/active revision mismatch")
		}
	}
	checkStatus(false)
	stop()
	pool := x509.NewCertPool()
	certificate, err := os.ReadFile(cert)
	if err != nil || !pool.AppendCertsFromPEM(certificate) {
		t.Fatal("test CA unavailable", err)
	}
	secureTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	defer secureTransport.CloseIdleConnections()
	client.Transport, base = secureTransport, "https://"+address
	launch()
	ready()
	checkStatus(true)
	body, _ := call("GET", "/api/auth/status", nil, 200)
	var auth struct {
		Initialized, Authenticated bool
		Username                   string
	}
	if json.Unmarshal(body, &auth) != nil || !auth.Initialized || !auth.Authenticated || auth.Username != credentials["username"] {
		t.Fatal("HTTP-to-HTTPS restart lost administrator/session")
	}
	body, _ = call("GET", "/api/workspace", nil, 200)
	var saved store.Document
	if json.Unmarshal(body, &saved) != nil || saved.Revision != 1 || !json.Valid(saved.Data) {
		t.Fatal("HTTPS restart lost workspace")
	}
	var workspace workspaceDraft
	if json.Unmarshal(saved.Data, &workspace) != nil || len(workspace.Inbounds) != 1 || len(workspace.Clients) != 1 {
		t.Fatal("workspace content lost")
	}
	_, cookies = call("POST", "/api/auth/login", credentials, 200)
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("HTTPS login cookie invalid")
	}
	// Go与浏览器对回环地址豁免Secure限制；用不联网的非回环域检查协议隔离。
	remoteJar, _ := cookiejar.New(nil)
	secureOrigin, _ := url.Parse("https://panel-process-qa.invalid/")
	plainOrigin, _ := url.Parse("http://panel-process-qa.invalid/")
	remoteJar.SetCookies(secureOrigin, cookies)
	if len(remoteJar.Cookies(secureOrigin)) != 1 {
		t.Fatal("secure session missing on HTTPS")
	}
	for _, cookie := range remoteJar.Cookies(plainOrigin) {
		if cookie.Name == sessionCookie {
			t.Fatal("secure session would be sent over HTTP")
		}
	}
	plainClient := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	if response, err := plainClient.Get("http://" + address + "/api/auth/status"); err == nil {
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal("HTTPS listener accepted a plaintext API request")
		}
	}
	_, cookies = call("POST", "/api/auth/logout", map[string]any{}, 200)
	if len(cookies) != 1 || !cookies[0].Secure || cookies[0].MaxAge != -1 {
		t.Fatal("HTTPS logout did not clear secure cookie")
	}
	call("GET", "/api/workspace", nil, 401)
	call("POST", "/api/auth/login", credentials, 200)
	// 非本机模式只在回环监听验收；测试域名经固定Dial直达回环，不查询外部DNS。
	publicAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(runtimePort(t)))
	if publicAddress == address {
		t.Fatal("ephemeral port collision")
	}
	_, publicPort, _ := net.SplitHostPort(publicAddress)
	publicOrigin := "https://panel-process-qa.invalid:" + publicPort
	publicAccess := PanelAccessDraft{Public: true, Listen: publicAddress, Origin: publicOrigin}
	call("PUT", "/api/panel/access", map[string]any{"revision": 0, "data": publicAccess, "confirmation": "确认启用HTTPS非本机访问"}, 200)
	checkAccess := func(active bool) {
		t.Helper()
		body, _ := call("GET", "/api/panel/access", nil, 200)
		var status struct {
			Active                   PanelAccessDraft
			Revision, ActiveRevision int64
		}
		if json.Unmarshal(body, &status) != nil || status.Active.Public != active || status.Revision != 1 || active && status.ActiveRevision != 1 || !active && status.ActiveRevision != 0 {
			t.Fatal("saved access unexpectedly changed running listener or lost revision")
		}
	}
	checkAccess(false)
	stop()
	publicTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != "panel-process-qa.invalid:"+publicPort {
			return nil, fmt.Errorf("unexpected non-loopback test destination")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", publicAddress)
	}}
	defer publicTransport.CloseIdleConnections()
	client.Transport, base = publicTransport, publicOrigin
	launch()
	ready()
	if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		connection.Close()
		t.Fatal("saved access failed to replace fallback listener")
	}
	call("GET", "/api/panel/access", nil, 401)
	_, cookies = call("POST", "/api/auth/login", credentials, 200)
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatal("explicit HTTPS access did not issue Secure session")
	}
	checkAccess(true)
	call("POST", "/api/auth/setup", credentials, 403)
	wrongHost, _ := http.NewRequest("GET", base+"/api/auth/status", nil)
	wrongHost.Host = address
	response, err := client.Do(wrongHost)
	if err != nil {
		t.Fatal("fixed host boundary request failed", err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("explicit HTTPS host boundary missing")
	}
	response, err = client.Get(base + "/api/auth/status")
	if err != nil {
		t.Fatal("HTTPS status request failed", err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("explicit HTTPS HSTS missing")
	}
	if response, err := plainClient.Get("http://" + publicAddress + "/api/auth/status"); err == nil {
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatal("explicit HTTPS mode accepted plaintext")
		}
	}
	stop()
	// 损坏只作用于临时源文件；实际main须拒绝启动且不能回退HTTP。
	if err := os.Rename(key, key+".unavailable"); err != nil {
		t.Fatal(err)
	}
	launch()
	select {
	case <-done:
		if processErr == nil {
			t.Fatal("missing certificate did not fail startup")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("invalid certificate startup did not terminate")
	}
	if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		connection.Close()
		t.Fatal("invalid TLS silently opened a listener")
	}
	stop()
	if err := os.Rename(key+".unavailable", key); err != nil {
		t.Fatal(err)
	}
	launch()
	ready()
	checkStatus(true)
	checkAccess(true)
	call("GET", "/api/workspace", nil, 200)
	call("PUT", "/api/panel/access", map[string]any{"revision": 1, "data": PanelAccessDraft{Listen: address}}, 200)
	stop()
	client.Transport, base = secureTransport, "https://"+address
	launch()
	ready()
	call("POST", "/api/auth/login", credentials, 200)
	accessBody, _ := call("GET", "/api/panel/access", nil, 200)
	var privateStatus struct {
		Active                   PanelAccessDraft
		Revision, ActiveRevision int64
	}
	if json.Unmarshal(accessBody, &privateStatus) != nil || privateStatus.Active.Public || privateStatus.Active.Listen != address || privateStatus.Revision != 2 || privateStatus.ActiveRevision != 2 {
		t.Fatal("disabling public access did not persist across restart")
	}
	if connection, err := net.DialTimeout("tcp", publicAddress, time.Second); err == nil {
		connection.Close()
		t.Fatal("previous explicit listener remained after disabling access")
	}
	stop()
	// 损坏的访问设置同样必须让实际main失败，不能沿用命令行默认监听。
	s, err := store.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDocument(context.Background(), "panel-access", 2, json.RawMessage(`{"public":true,"listen":"127.0.0.1:1","origin":"https://wrong.invalid"}`)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	launch()
	select {
	case <-done:
		if processErr == nil {
			t.Fatal("invalid access did not fail startup")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("invalid access startup did not terminate")
	}
	if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		connection.Close()
		t.Fatal("invalid access silently opened a listener")
	}
	stop()
}
