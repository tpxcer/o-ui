package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"independent-panel/internal/store"
)

// 两个不同核心目录不得绕过同一个面板库的单实例所有权。
func TestPanelStateOwnership(t *testing.T) {
	if os.Getenv("PANEL_PROCESS_QA") != "1" {
		t.Skip("设置PANEL_PROCESS_QA=1后验证实际面板状态库单实例")
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := processQABinary(t, project)
	for _, shutdown := range []string{"normal", "killed"} {
		t.Run(shutdown, func(t *testing.T) {
			stateRoot, firstCore, secondCore := t.TempDir(), t.TempDir(), t.TempDir()
			firstAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(runtimePort(t)))
			secondAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(runtimePort(t)))
			if firstAddress == secondAddress {
				t.Fatal("ephemeral collision")
			}
			type instance struct {
				command *exec.Cmd
				done    chan struct{}
				err     error
			}
			start := func(address, coreRoot string) *instance {
				t.Helper()
				p := &instance{command: exec.Command(binary, "-listen", address, "-state", stateRoot, "-data", coreRoot, "-dist", filepath.Join(project, "dist")), done: make(chan struct{})}
				p.command.Stdout, p.command.Stderr = io.Discard, io.Discard
				if err := p.command.Start(); err != nil {
					t.Fatal(err)
				}
				go func() { p.err = p.command.Wait(); close(p.done) }()
				t.Cleanup(func() {
					select {
					case <-p.done:
					default:
						_ = p.command.Process.Kill()
						<-p.done
					}
				})
				return p
			}
			jar, _ := cookiejar.New(nil)
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Jar: jar, Timeout: time.Second}
			ready := func(p *instance, address string) {
				t.Helper()
				deadline := time.Now().Add(8 * time.Second)
				for {
					select {
					case <-p.done:
						t.Fatal("panel exited before ready", p.err)
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
			call := func(address, method, path string, value any) []byte {
				t.Helper()
				data, _ := json.Marshal(value)
				request, _ := http.NewRequest(method, "http://"+address+path, bytes.NewReader(data))
				request.Header.Set("Origin", "http://"+address)
				request.Header.Set("Content-Type", "application/json")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal("owner API unavailable", err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
				if err != nil || response.StatusCode != 200 {
					t.Fatal("owner API mismatch", response.StatusCode, err)
				}
				return body
			}
			first := start(firstAddress, firstCore)
			ready(first, firstAddress)
			call(firstAddress, "POST", "/api/auth/setup", map[string]string{"username": "ownerqa", "password": "isolated-owner-test-passphrase"})
			call(firstAddress, "PUT", "/api/workspace", store.Document{Data: validWorkspace()})
			before := call(firstAddress, "GET", "/api/workspace", nil)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			second := exec.CommandContext(ctx, binary, "-listen", secondAddress, "-state", stateRoot, "-data", secondCore, "-dist", filepath.Join(project, "dist"))
			second.Stdout, second.Stderr = io.Discard, io.Discard
			err := second.Run()
			cancel()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Fatal("duplicate panel did not reject shared state promptly", err)
			}
			if connection, err := net.DialTimeout("tcp", secondAddress, time.Second); err == nil {
				connection.Close()
				t.Fatal("duplicate panel opened listener")
			}
			if !bytes.Equal(before, call(firstAddress, "GET", "/api/workspace", nil)) {
				t.Fatal("duplicate opener changed owner workspace")
			}
			var auth struct {
				Initialized, Authenticated bool
				Username                   string
			}
			if json.Unmarshal(call(firstAddress, "GET", "/api/auth/status", nil), &auth) != nil || !auth.Initialized || !auth.Authenticated || auth.Username != "ownerqa" {
				t.Fatal("duplicate opener changed administrator/session")
			}
			if shutdown == "normal" {
				err = first.command.Process.Signal(os.Interrupt)
			} else {
				err = first.command.Process.Kill()
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-first.done:
			case <-time.After(8 * time.Second):
				t.Fatal("owned panel did not stop")
			}
			if shutdown == "normal" && first.err != nil {
				t.Fatal("normal owner shutdown failed", first.err)
			}
			replacement := start(secondAddress, secondCore)
			ready(replacement, secondAddress)
			if !bytes.Equal(before, call(secondAddress, "GET", "/api/workspace", nil)) {
				t.Fatal("ownership release lost workspace")
			}
			if json.Unmarshal(call(secondAddress, "GET", "/api/auth/status", nil), &auth) != nil || !auth.Initialized || !auth.Authenticated {
				t.Fatal("ownership release lost administrator/session")
			}
			if err := replacement.command.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			select {
			case <-replacement.done:
				if replacement.err != nil {
					t.Fatal(replacement.err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("replacement did not stop normally")
			}
		})
	}
}
