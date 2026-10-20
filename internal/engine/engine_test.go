package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 本测试进程充当可控假核心；真实官方连接另有显式集成测试。
func TestMain(m *testing.M) {
	if code, handled := Supervisor(); handled {
		os.Exit(code)
	}
	if len(os.Args) == 3 && os.Args[1] == "--fixture-parent" {
		binary, _ := os.Executable()
		e := &Engine{}
		if e.Apply(context.Background(), Candidate{Binary: binary, File: os.Args[2], Inbounds: 1}) != nil {
			os.Exit(5)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		_, _ = bufio.NewReader(os.Stdin).ReadByte()
		_ = e.Close()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "run" {
		var fixture struct {
			Address    string
			Invalid    bool
			FailStart  bool
			IgnoreTerm bool
			UDP        bool
			Diagnostic string
		}
		data, err := os.ReadFile(os.Args[len(os.Args)-1])
		if err != nil || json.Unmarshal(data, &fixture) != nil {
			os.Exit(2)
		}
		if fixture.Diagnostic != "" {
			_, _ = os.Stderr.WriteString(fixture.Diagnostic)
		}
		if fixture.Invalid {
			os.Exit(2)
		}
		if len(os.Args) > 2 && os.Args[2] == "-test" {
			os.Exit(0)
		}
		if fixture.FailStart {
			os.Exit(3)
		}
		listener, err := net.Listen("tcp", fixture.Address)
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(4)
		}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func() { defer conn.Close(); _, _ = io.Copy(io.Discard, conn) }()
			}
		}()
		if fixture.UDP {
			udp, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				os.Exit(4)
			}
			defer udp.Close()
		}
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
		for {
			<-stop
			if !fixture.IgnoreTerm {
				break
			}
		}
		_ = listener.Close()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func waitClosed(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			return
		}
		connection.Close()
		if time.Now().After(deadline) {
			t.Fatal("自有核心仍在监听")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestPanelKilledStopsOwnCore(t *testing.T) {
	for _, stubborn := range []bool{false, true} {
		t.Run(map[bool]string{false: "正常TERM", true: "忽略TERM后强制结束"}[stubborn], func(t *testing.T) {
			address := freeAddress(t)
			c := fakeCandidate(t, address, false, false)
			if stubborn {
				data, _ := json.Marshal(map[string]any{"Address": address, "IgnoreTerm": true})
				if err := os.WriteFile(c.File, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			binary, _ := os.Executable()
			parent := exec.Command(binary, "--fixture-parent", c.File)
			stdin, err := parent.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := parent.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = parent.Start(); err != nil {
				t.Fatal(err)
			}
			defer parent.Process.Kill()
			ready := make(chan string, 1)
			go func() { s, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- s }()
			select {
			case s := <-ready:
				if s != "ready\n" {
					t.Fatal("隔离父面板未就绪")
				}
			case <-time.After(8 * time.Second):
				t.Fatal("启动超时")
			}
			foreign, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer foreign.Close()
			if err = parent.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = parent.Wait()
			waitClosed(t, address)
			connection, err := net.Dial("tcp", foreign.Addr().String())
			if err != nil {
				t.Fatal("影响了外部服务")
			}
			connection.Close()
		})
	}
}

func TestSupervisorInputBoundary(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{}`, `{"binary":"relative","file":"/tmp/test"}`, `{"binary":"/tmp/test","file":"/tmp/test","extra":true}`, `{"binary":"/tmp/test","file":"/tmp/test"} {}`, strings.Repeat("x", 65537)} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// CommandContext保证无效输入不能把测试挂住。
		cmd := exec.CommandContext(ctx, binary, supervisorArgument)
		cmd.Stdin, cmd.ExtraFiles = strings.NewReader(input), []*os.File{reader}
		err = cmd.Run()
		cancel()
		reader.Close()
		writer.Close()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatal("invalid supervisor input accepted", err)
		}
	}
	cmd := exec.Command(binary, supervisorArgument)
	if err := cmd.Run(); err == nil {
		t.Fatal("missing lifetime pipe accepted")
	}
}

func TestSupervisorCrashAndUnexpectedStatus(t *testing.T) {
	e := &Engine{}
	defer e.Close()
	c := fakeCandidate(t, freeAddress(t), false, false)
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := e.process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.process.done:
	case <-time.After(5 * time.Second):
		t.Fatal("监督进程未退出")
	}
	waitClosed(t, readFixtureAddress(t, c.File))
	s := e.Status()
	if s.Running || !s.UnexpectedExit || s.ExitedAt == nil {
		t.Fatal(s)
	}
	if _, running := e.Current(); running {
		t.Fatal("异常后仍能导出存活配置")
	}
	called := false
	if err := e.ApplyChecked(context.Background(), c, nil, nil, func() error { called = true; return errors.New("账本保存失败") }); err == nil || !called || e.Status().Running {
		t.Fatal("异常后重新应用绕过账本缺口保护")
	}
	if err := e.ApplyChecked(context.Background(), c, nil, nil, func() error { return nil }); err != nil || !e.Status().Running || e.Status().UnexpectedExit {
		t.Fatal("显式重新应用未恢复独立核心", err)
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if e.Status().UnexpectedExit {
		t.Fatal("显式停止后未清除异常状态")
	}
}

func readFixtureAddress(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Address string }
	if json.Unmarshal(data, &v) != nil {
		t.Fatal("夹具无效")
	}
	return v.Address
}

func fakeCandidate(t *testing.T, address string, invalid, fail bool) Candidate {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"Address": address, "Invalid": invalid, "FailStart": fail})
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return Candidate{Binary: binary, File: file, Version: "26.7.28", WorkspaceRevision: 1, SettingsRevision: 2, Inbounds: 1}
}

func freeAddress(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	return address
}

func TestLifecycleRollback(t *testing.T) {
	e := &Engine{}
	t.Cleanup(func() { _ = e.Close() })
	c := fakeCandidate(t, freeAddress(t), false, false)
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	first := e.Status()
	if !first.Running || first.Version != c.Version {
		t.Fatal("not running")
	}
	if current, running := e.Current(); !running || current.File != c.File {
		t.Fatal("running configuration snapshot missing")
	}
	invalid := fakeCandidate(t, freeAddress(t), true, false)
	if e.Apply(context.Background(), invalid) == nil || !e.Status().StartedAt.Equal(*first.StartedAt) {
		t.Fatal("invalid preflight interrupted old process")
	}
	failing := fakeCandidate(t, freeAddress(t), false, true)
	failing.WorkspaceRevision = 99
	if e.Apply(context.Background(), failing) == nil {
		t.Fatal("failed start accepted")
	}
	if s := e.Status(); !s.Running || s.WorkspaceRevision != 1 {
		t.Fatal("rollback failed")
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if e.Status().Running {
		t.Fatal("stop failed")
	}
	if _, running := e.Current(); running {
		t.Fatal("stopped configuration exported")
	}
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if e.Status().Running {
		t.Fatal("close left child")
	}
	if e.Apply(context.Background(), c) == nil {
		t.Fatal("closed controller restarted")
	}
}

func TestOccupiedPortNotTakenOver(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	e := &Engine{}
	defer e.Close()
	if e.Apply(context.Background(), fakeCandidate(t, listener.Addr().String(), false, false)) == nil {
		t.Fatal("foreign listener mistaken for own core")
	}
	if e.Status().Running {
		t.Fatal("foreign service counted as running")
	}
	if connection, err := net.Dial("tcp", listener.Addr().String()); err != nil {
		t.Fatal("foreign service stopped")
	} else {
		connection.Close()
	}
}

func TestCancelledApplyLeavesOldProcess(t *testing.T) {
	e := &Engine{}
	defer e.Close()
	c := fakeCandidate(t, freeAddress(t), false, false)
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	first := e.Status()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Apply(ctx, c); err == nil {
		t.Fatal("cancelled apply accepted")
	}
	if !e.Status().StartedAt.Equal(*first.StartedAt) {
		t.Fatal("cancelled apply interrupted old process")
	}
}

func TestEmptyAppliedConfigStopsOldListener(t *testing.T) {
	e := &Engine{}
	defer e.Close()
	c := fakeCandidate(t, freeAddress(t), false, false)
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	c.Inbounds = 0
	c.WorkspaceRevision = 3
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if status := e.Status(); status.Running || status.Inbounds != 0 || status.WorkspaceRevision != 3 {
		t.Fatal("empty applied config left active core")
	}
}

func TestCheckpointFailureKeepsOldProcess(t *testing.T) {
	e := &Engine{}
	defer e.Close()
	c := fakeCandidate(t, freeAddress(t), false, false)
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	first := e.Status()
	called := false
	checkpoint := func(old Candidate, started time.Time) error {
		called = true
		if old.File != c.File || !started.Equal(*first.StartedAt) {
			t.Fatal("wrong checkpoint instance")
		}
		return errors.New("fixture checkpoint failure")
	}
	if e.ApplyChecked(context.Background(), fakeCandidate(t, freeAddress(t), true, false), checkpoint, nil) == nil || called {
		t.Fatal("checkpoint before successful preflight")
	}
	if e.ApplyChecked(context.Background(), c, checkpoint, nil) == nil || !called {
		t.Fatal("checkpoint failure ignored")
	}
	if !e.Status().StartedAt.Equal(*first.StartedAt) {
		t.Fatal("checkpoint failure restarted old process")
	}
	if e.ApplyChecked(context.Background(), c, nil, func() error { return errors.New("deadline crossed") }) == nil || !e.Status().StartedAt.Equal(*first.StartedAt) {
		t.Fatal("late validation failure restarted old process")
	}
}
