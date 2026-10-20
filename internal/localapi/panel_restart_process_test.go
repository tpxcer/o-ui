package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// “重启面板”接口：同一进程正常关闭后原地重新执行，重新监听，并按“上次退出时在运行”自动恢复核心。
// 只用临时库、回环端口和复制的已校验官方核心。
func TestPanelRestartProcessResumesCore(t *testing.T) {
	source := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if source == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证实际面板进程重启与核心自动恢复")
	}
	version := runtimeQAVersion(t)
	coreRoot := t.TempDir()
	from, to := filepath.Join(source, "v"+version), filepath.Join(coreRoot, "v"+version)
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(to, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := fs.FileMode(0600)
		if entry.IsDir() || entry.Name() == "xray" {
			mode = 0700
		}
		return os.Chmod(path, mode)
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := core.NewManager(coreRoot)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.Install(context.Background(), version)
	_ = manager.Close()
	if err != nil {
		t.Fatal("select verified temporary test core", err)
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := processQABinary(t, project)
	stateRoot := t.TempDir()
	address := fmt.Sprintf("127.0.0.1:%d", runtimePort(t))
	base := "http://" + address
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 25 * time.Second}
	command := exec.Command(binary, "-listen", address, "-state", stateRoot, "-data", coreRoot, "-dist", filepath.Join(project, "dist"))
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
	ready := func() {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			select {
			case err := <-done:
				exited = true
				t.Fatal("panel process exited instead of restarting in place", err)
			default:
			}
			if response, err := client.Get(base + "/api/auth/status"); err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("panel readiness timeout")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	call := func(method, path string, value any, want int) []byte {
		t.Helper()
		data, _ := json.Marshal(value)
		request, _ := http.NewRequest(method, base+"/api/"+path, bytes.NewReader(data))
		request.Header.Set("Origin", base)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("panel request failed", method, path, err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if response.StatusCode != want {
			t.Fatal("panel API status mismatch", method, path, response.StatusCode, "want", want, string(body))
		}
		return body
	}
	status := func() engine.Status {
		t.Helper()
		var result engine.Status
		if json.Unmarshal(call("GET", "runtime/status", nil, 200), &result) != nil {
			t.Fatal("invalid runtime status")
		}
		return result
	}
	ready()
	call("POST", "auth/setup", map[string]string{"username": "restartqa", "password": "restart-process-test-passphrase"}, 200)
	cert, key := runtimeCertificate(t, t.TempDir())
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	draft.Inbounds[0].Preset, draft.Inbounds[0].Port = "xhttp-tls", runtimePort(t)
	draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/restart-test", Mode: "auto"}
	data, _ := json.Marshal(draft)
	call("PUT", "workspace", store.Document{Data: data}, 200)
	settings := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": "[]", "outbounds": `[{"tag":"direct","protocol":"freedom"}]`, "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
	data, _ = json.Marshal(settings)
	call("PUT", "settings", store.Document{Data: data}, 200)
	call("POST", "runtime/apply", runtimeRequest{Version: version, WorkspaceRevision: 1, SettingsRevision: 1}, 200)
	before := status()
	if !before.Running || before.StartedAt == nil {
		t.Fatal("core not running before restart")
	}
	pid := command.Process.Pid
	call("POST", "panel/restart", struct{}{}, 202)
	// 先确认旧监听已关闭，避免把重启前的响应当成重启完成。
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get(base + "/api/auth/status")
		if err != nil {
			break
		}
		response.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("panel never stopped listening after restart request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ready()
	if command.Process.Pid != pid {
		t.Fatal("restart did not stay in the same process")
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		after := status()
		if after.Running && after.StartedAt != nil && after.StartedAt.After(*before.StartedAt) && after.Version == version && after.WorkspaceRevision == 1 && after.SettingsRevision == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("core was not auto-resumed after panel restart", after)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		exited = true
		if err != nil {
			t.Fatal("restarted panel did not stop cleanly", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restarted panel did not stop after SIGTERM")
	}
	// 自动恢复的官方核心一开始监听就发SIGTERM（此时应用收尾通常未结束）：面板应等收尾完成再退出，下次启动仍自动恢复。
	relaunch := func() {
		t.Helper()
		command = exec.Command(binary, "-listen", address, "-state", stateRoot, "-data", coreRoot, "-dist", filepath.Join(project, "dist"))
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		current, finished := command, make(chan error, 1)
		done, exited = finished, false
		go func() { finished <- current.Wait() }()
	}
	terminate := func() {
		t.Helper()
		if err := command.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			exited = true
			if err != nil {
				t.Fatal("panel did not stop cleanly after SIGTERM", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("panel did not stop after SIGTERM")
		}
	}
	relaunch()
	inbound := fmt.Sprintf("127.0.0.1:%d", draft.Inbounds[0].Port)
	deadline = time.Now().Add(15 * time.Second)
	for {
		select {
		case err := <-done:
			exited = true
			t.Fatal("panel exited during auto-resume", err)
		default:
		}
		if connection, err := net.DialTimeout("tcp", inbound, 100*time.Millisecond); err == nil {
			connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("auto-resumed core never listened")
		}
		time.Sleep(5 * time.Millisecond)
	}
	terminate()
	relaunch()
	ready()
	deadline = time.Now().Add(15 * time.Second)
	for {
		after := status()
		if after.Running && after.Version == version && after.WorkspaceRevision == 1 && after.SettingsRevision == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("core was not auto-resumed after SIGTERM during auto-resume", after, string(call("GET", "runtime/history", nil, 200)))
		}
		time.Sleep(100 * time.Millisecond)
	}
	terminate()
}
