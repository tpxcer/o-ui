package localapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 管理菜单用的离线命令：访问路径、运行中只读查询、停服后重置账号；只用临时目录、回环地址与测试CA。
func TestManagementCommandsProcess(t *testing.T) {
	if os.Getenv("PANEL_PROCESS_QA") != "1" {
		t.Skip("设置PANEL_PROCESS_QA=1后验证管理命令的实际程序行为")
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := processQABinary(t, project)
	stateRoot, coreRoot := filepath.Join(t.TempDir(), "panel"), t.TempDir()
	address := fmt.Sprintf("127.0.0.1:%d", runtimePort(t))
	origin := "https://" + address
	cert, key := runtimeCertificate(t, t.TempDir(), "127.0.0.1")
	run := func(stdin string, want int, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Dir, command.Stdin = t.TempDir(), strings.NewReader(stdin)
		output, err := command.CombinedOutput()
		if exit, ok := err.(*exec.ExitError); (want == 0 && err != nil) || (want != 0 && (!ok || exit.ExitCode() != want)) {
			t.Fatal("CLI exit mismatch", args, err, string(output))
		}
		if strings.Contains(string(output), "manage-old-passphrase") || strings.Contains(string(output), "manage-new-passphrase") {
			t.Fatal("CLI echoed a password")
		}
		return string(output)
	}
	run("manage-old-passphrase\n", 0, "-init-panel-admin", "-state", stateRoot, "-username", "olduser")
	run("", 1, "-set-panel-path", "/mgmt/", "-state", stateRoot)
	run("", 0, "-enable-panel-https", "-state", stateRoot, "-panel-cert", cert, "-panel-key", key, "-panel-listen", address, "-panel-origin", origin, "-confirm-https", "确认启用HTTPS非本机访问")
	for _, bad := range []string{"/api/", "mgmt", "/a/b/", "/bad path/"} {
		run("", 1, "-set-panel-path", bad, "-state", stateRoot)
	}
	run("", 1, "-set-panel-path", "/mgmt/", "-show-panel", "-state", stateRoot)
	run("", 1, "-show-panel")
	run("", 0, "-set-panel-path", "/mgmt/", "-state", stateRoot)
	// 换证书路径时保留访问路径
	run("", 0, "-enable-panel-https", "-state", stateRoot, "-panel-cert", cert, "-panel-key", key, "-panel-listen", address, "-panel-origin", origin, "-confirm-https", "确认启用HTTPS非本机访问")

	pool := x509.NewCertPool()
	data, err := os.ReadFile(cert)
	if err != nil || !pool.AppendCertsFromPEM(data) {
		t.Fatal("test CA unavailable", err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	var command *exec.Cmd
	var done chan error
	launch := func() {
		t.Helper()
		command = exec.Command(binary, "-listen", fmt.Sprintf("127.0.0.1:%d", runtimePort(t)), "-state", stateRoot, "-data", coreRoot, "-dist", filepath.Join(project, "dist"))
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done = make(chan error, 1)
		current, finished := command, done
		go func() { finished <- current.Wait() }()
		deadline := time.Now().Add(8 * time.Second)
		for {
			select {
			case err := <-done:
				command = nil
				t.Fatal("panel exited before ready", err)
			default:
			}
			if response, err := client.Get(origin + "/mgmt/api/auth/status"); err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("panel readiness timeout under the access path")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	stop := func() {
		t.Helper()
		if command == nil {
			return
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Error("panel did not stop cleanly", err)
			}
		case <-time.After(8 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Error("panel did not stop after SIGTERM")
		}
		command = nil
	}
	defer stop()
	login := func(username, password string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": username, "password": password})
		request, _ := http.NewRequest("POST", origin+"/mgmt/api/auth/login", bytes.NewReader(body))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("login request failed", err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	launch()
	var info PanelInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(run("", 0, "-show-panel", "-state", stateRoot))), &info); err != nil {
		t.Fatal("show-panel output is not JSON", err)
	}
	if !info.Public || info.BasePath != "/mgmt/" || info.URL != origin+"/mgmt/" || info.Username != "olduser" || info.CertificateFile != cert || info.KeyFile != key {
		t.Fatal("show-panel mismatch while running", info)
	}
	if response, err := client.Get(origin + "/api/auth/status"); err == nil {
		response.Body.Close()
		if response.StatusCode == 200 {
			t.Fatal("API answered outside the access path")
		}
	}
	if code := login("olduser", "manage-old-passphrase"); code != 200 {
		t.Fatal("original administrator cannot sign in under the access path", code)
	}
	run("manage-new-passphrase\n", 1, "-reset-panel-admin", "-state", stateRoot, "-username", "newuser")
	run("", 1, "-set-panel-path", "/", "-state", stateRoot)
	stop()
	run("short\n", 1, "-reset-panel-admin", "-state", stateRoot, "-username", "newuser")
	run("manage-new-passphrase\n", 0, "-reset-panel-admin", "-state", stateRoot, "-username", "newuser")
	launch()
	if code := login("olduser", "manage-old-passphrase"); code == 200 {
		t.Fatal("old administrator still signs in after reset")
	}
	if code := login("newuser", "manage-new-passphrase"); code != 200 {
		t.Fatal("reset administrator cannot sign in", code)
	}
}
