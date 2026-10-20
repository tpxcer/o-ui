package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"independent-panel/internal/engine"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
)

// 只运行实际面板程序和临时数据库，恢复命令不启动面板或核心。
func TestPanelAccessRecoveryProcess(t *testing.T) {
	if os.Getenv("PANEL_PROCESS_QA") != "1" {
		t.Skip("设置PANEL_PROCESS_QA=1后验证实际CLI访问恢复")
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := processQABinary(t, project)
	root, coreRoot := t.TempDir(), t.TempDir()
	address := fmt.Sprintf("127.0.0.1:%d", runtimePort(t))
	fallback := fmt.Sprintf("127.0.0.1:%d", runtimePort(t))
	if address == fallback {
		t.Fatal("ephemeral collision")
	}
	ctx := context.Background()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	session, err := s.Initialize(ctx, "recoveryqa", "isolated-recovery-test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDocument(ctx, "workspace", 0, validWorkspace()); err != nil {
		t.Fatal(err)
	}
	names := []string{"workspace", "xray-settings", "proxy-identities", "traffic", subscriptionsDocument, appliedDocument}
	before, err := s.ReadDocuments(ctx, names)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args []string, want int) []byte {
		t.Helper()
		bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		command := exec.CommandContext(bounded, binary, args...)
		command.Dir = t.TempDir()
		output, err := command.CombinedOutput()
		entries, readErr := os.ReadDir(command.Dir)
		if readErr != nil || len(entries) != 0 {
			t.Fatal("CLI unexpectedly created relative/default data", readErr)
		}
		if bounded.Err() != nil {
			t.Fatal("CLI did not exit promptly")
		}
		if want == 0 {
			if err != nil {
				t.Fatal("CLI failed", err)
			}
		} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != want {
			t.Fatal("CLI exit mismatch", err, want)
		}
		if bytes.Contains(output, []byte(session.Token)) || bytes.Contains(output, []byte("isolated-recovery-test-passphrase")) {
			t.Fatal("CLI exposed test credentials")
		}
		return output
	}
	recoverArgs := []string{"-recover-panel-access", "-state", root, "-listen", address, "-confirm-recovery", "恢复面板本机访问", "-data", filepath.Join(t.TempDir(), "unused-core")}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no-explicit-state", []string{"-recover-panel-access", "-confirm-recovery", "恢复面板本机访问"}},
		{"relative-state", []string{"-recover-panel-access", "-state", "relative-do-not-create", "-confirm-recovery", "恢复面板本机访问"}},
		{"no-confirmation", []string{"-recover-panel-access", "-state", root}},
		{"wrong-confirmation", []string{"-recover-panel-access", "-state", root, "-confirm-recovery", "yes"}},
		{"public-listen", []string{"-recover-panel-access", "-state", root, "-listen", "0.0.0.0:5180", "-confirm-recovery", "恢复面板本机访问"}},
		{"low-port", []string{"-recover-panel-access", "-state", root, "-listen", "127.0.0.1:443", "-confirm-recovery", "恢复面板本机访问"}},
		{"confirmation-without-mode", []string{"-state", root, "-confirm-recovery", "恢复面板本机访问"}},
	} {
		t.Run(tc.name, func(t *testing.T) { run(tc.args, 1) })
	}
	assertClosed := func(target string) {
		t.Helper()
		if conn, err := net.DialTimeout("tcp", target, 300*time.Millisecond); err == nil {
			conn.Close()
			t.Fatal("unexpected listener", target)
		}
	}
	var command *exec.Cmd
	var done chan struct{}
	var processErr error
	stop := func() {
		t.Helper()
		if command == nil {
			return
		}
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
			if processErr != nil {
				t.Error("normal panel shutdown failed", processErr)
			}
		case <-time.After(8 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Error("panel shutdown timeout")
		}
		command = nil
	}
	defer stop()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	call := func(path string) []byte {
		t.Helper()
		r, _ := http.NewRequest("GET", "http://"+address+"/api/"+path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("recovered API unavailable", err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != 200 {
			t.Fatal("recovered API mismatch", response.StatusCode, err)
		}
		return body
	}
	start := func(listen string) {
		t.Helper()
		command = exec.Command(binary, "-listen", listen, "-state", root, "-data", coreRoot, "-dist", filepath.Join(project, "dist"))
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done = make(chan struct{})
		current, finished := command, done
		go func() { processErr = current.Wait(); close(finished) }()
		deadline := time.Now().Add(8 * time.Second)
		for {
			select {
			case <-done:
				t.Fatal("panel exited before ready", processErr)
			default:
			}
			response, err := client.Get("http://" + address + "/api/auth/status")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("panel readiness timeout")
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	start(address)
	run(recoverArgs, 1)
	var saved store.Document
	if json.Unmarshal(call("workspace"), &saved) != nil || !reflect.DeepEqual(saved, before["workspace"]) {
		t.Fatal("rejected recovery changed live workspace")
	}
	stop()
	for _, broken := range []string{"panel-tls", "panel-access"} {
		t.Run(broken, func(t *testing.T) {
			checkpoint, err := s.ReadDocuments(ctx, names)
			if err != nil {
				t.Fatal(err)
			}
			old, err := s.ReadDocument(ctx, broken)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.SaveDocument(ctx, broken, old.Revision, json.RawMessage(`{"broken":true}`)); err != nil {
				t.Fatal(err)
			}
			run([]string{"-listen", fallback, "-state", root, "-data", coreRoot}, 1)
			assertClosed(address)
			assertClosed(fallback)
			output := run(recoverArgs, 0)
			if !bytes.Contains(output, []byte(i18n.Pick(i18n.EnvLang(), "尚未启动服务", "Nothing was started"))) {
				t.Fatal("recovery did not distinguish save from startup")
			}
			assertClosed(address)
			assertClosed(fallback)
			if _, err := os.Lstat(recoverArgs[len(recoverArgs)-1]); !os.IsNotExist(err) {
				t.Fatal("recovery opened core manager")
			}
			after, err := s.ReadDocuments(ctx, names)
			if err != nil || !reflect.DeepEqual(checkpoint, after) {
				t.Fatal("recovery command changed business state", err)
			}
			start(fallback)
			assertClosed(fallback)
			var status engine.Status
			if json.Unmarshal(call("runtime/status"), &status) != nil || status.Running {
				t.Fatal("normal startup automatically started core")
			}
			if json.Unmarshal(call("workspace"), &saved) != nil || !reflect.DeepEqual(saved, before["workspace"]) {
				t.Fatal("recovered startup lost workspace/session")
			}
			stop()
			if _, err := s.Login(ctx, "recoveryqa", "isolated-recovery-test-passphrase"); err != nil {
				t.Fatal("recovery changed administrator password", err)
			}
		})
	}
}
