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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 按安装器顺序运行实际程序：离线设管理员→离线写HTTPS→启动；首次登录只能经HTTPS，网页初始化被拒绝。
// 只用临时目录、回环地址与测试CA，不改系统信任。
func TestInstallerOfflineSetupProcess(t *testing.T) {
	if os.Getenv("PANEL_PROCESS_QA") != "1" {
		t.Skip("设置PANEL_PROCESS_QA=1后验证实际CLI离线初始化与HTTPS首登")
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := processQABinary(t, project)
	stateRoot, coreRoot := filepath.Join(t.TempDir(), "panel"), t.TempDir()
	address := fmt.Sprintf("127.0.0.1:%d", runtimePort(t))
	fallback := fmt.Sprintf("127.0.0.1:%d", runtimePort(t))
	origin := "https://" + address
	cert, key := runtimeCertificate(t, t.TempDir(), "127.0.0.1")
	const password = "installer-process-passphrase"
	run := func(stdin string, want int, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Dir, command.Stdin = t.TempDir(), strings.NewReader(stdin)
		output, err := command.CombinedOutput()
		if want == 0 && err != nil {
			t.Fatal("CLI failed", args, err, string(output))
		}
		if exit, ok := err.(*exec.ExitError); want != 0 && (!ok || exit.ExitCode() != want) {
			t.Fatal("CLI exit mismatch", args, err)
		}
		if strings.Contains(string(output), password) {
			t.Fatal("CLI echoed the administrator password")
		}
		return string(output)
	}
	initArgs := []string{"-init-panel-admin", "-state", stateRoot, "-username", "installqa"}
	httpsArgs := []string{"-enable-panel-https", "-state", stateRoot, "-panel-cert", cert, "-panel-key", key, "-panel-listen", address, "-panel-origin", origin, "-confirm-https", "确认启用HTTPS非本机访问"}
	run(password+"\n", 1, "-init-panel-admin", "-username", "installqa")
	run(password+"\n", 1, append(initArgs, "-enable-panel-https")...)
	run(password+"\n", 1, "-state", stateRoot, "-username", "installqa")
	run("short\n", 1, initArgs...)
	if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
		t.Fatal("rejected CLI created state", err)
	}
	run(password+"\n", 1, httpsArgs...)
	run(password+"\n", 0, initArgs...)
	run("another-installer-passphrase\n", 1, initArgs...)
	run("", 1, append(append([]string{}, httpsArgs[:len(httpsArgs)-1]...), "yes")...)
	out := run("", 0, httpsArgs...)
	if !strings.Contains(out, origin) {
		t.Fatal("HTTPS CLI output missing origin", out)
	}

	pool := x509.NewCertPool()
	data, err := os.ReadFile(cert)
	if err != nil || !pool.AppendCertsFromPEM(data) {
		t.Fatal("test CA unavailable", err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	command := exec.Command(binary, "-listen", fallback, "-state", stateRoot, "-data", coreRoot, "-dist", filepath.Join(project, "dist"))
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	exited := false
	defer func() {
		if !exited {
			_ = command.Process.Kill()
			<-done
		}
	}()
	call := func(method, path string, value any) (*http.Response, []byte) {
		t.Helper()
		body, _ := json.Marshal(value)
		request, _ := http.NewRequest(method, origin+path, bytes.NewReader(body))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("HTTPS request failed", path, err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return response, data
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		select {
		case err := <-done:
			exited = true
			t.Fatal("panel exited before ready", err)
		default:
		}
		if response, err := client.Get(origin + "/api/auth/status"); err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTPS panel readiness timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn, err := net.DialTimeout("tcp", fallback, 300*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("fallback HTTP listener opened despite saved HTTPS access")
	}
	plainClient := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	plain, err := plainClient.Get("http://" + address + "/api/auth/status")
	if err != nil {
		t.Fatal("plain HTTP redirect failed", err)
	}
	data, _ = io.ReadAll(plain.Body)
	plain.Body.Close()
	if plain.StatusCode != 308 || plain.Header.Get("Location") != origin+"/api/auth/status" || strings.Contains(string(data), "initialized") || len(plain.Cookies()) != 0 {
		t.Fatal("plain HTTP must only redirect without exposing API or setting a session")
	}
	if response, data := call("GET", "/api/auth/status", nil); response.StatusCode != 200 || !strings.Contains(string(data), `"initialized":true`) {
		t.Fatal("first HTTPS status mismatch", response.StatusCode, string(data))
	}
	if response, _ := call("POST", "/api/auth/setup", map[string]string{"username": "attacker", "password": "attacker-chosen-passphrase"}); response.StatusCode != 403 {
		t.Fatal("web initialization reachable over public HTTPS", response.StatusCode)
	}
	response, _ := call("POST", "/api/auth/login", map[string]string{"username": "installqa", "password": password})
	if response.StatusCode != 200 || len(response.Cookies()) != 1 || !response.Cookies()[0].Secure {
		t.Fatal("first HTTPS login failed", response.StatusCode)
	}
	// systemd以SIGTERM停止：正常退出码0，Restart=on-failure不会重启
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		exited = true
		if err != nil {
			t.Fatal("SIGTERM did not stop the panel cleanly", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("panel did not stop after SIGTERM")
	}
}
