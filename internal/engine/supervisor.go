package engine

import (
	"bytes"
	"encoding/json"
	"independent-panel/internal/i18n"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"independent-panel/internal/diagnostics"
)

const supervisorArgument = "--internal-core-supervisor"

type supervisorRequest struct {
	Binary    string `json:"binary"`
	File      string `json:"file"`
	Preflight bool   `json:"preflight"`
}

// Supervisor仅由main显式分派；生命管道关闭后结束本次启动的核心。
// 不读取PID文件、不搜索进程，也不在面板重启时接管旧进程。
func Supervisor() (int, bool) {
	if len(os.Args) != 2 || os.Args[1] != supervisorArgument {
		return 0, false
	}
	life := os.NewFile(3, "panel-lifetime")
	if life == nil {
		return 2, true
	}
	defer life.Close()
	info, err := life.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return 2, true
	}
	syscall.CloseOnExec(3)
	var request supervisorRequest
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || !filepath.IsAbs(request.Binary) || !filepath.IsAbs(request.File) {
		return 2, true
	}
	parentGone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, life); close(parentGone) }()
	stopSignal := make(chan os.Signal, 1)
	signal.Notify(stopSignal, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(stopSignal)
	select {
	case <-parentGone:
		return 0, true
	default:
	}
	// Linux的Pdeathsig与创建线程绑定，锁住监督线程直至核心结束。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	args := []string{"run", "-config", request.File}
	if request.Preflight {
		args = []string{"run", "-test", "-config", request.File}
	}
	cmd := command(Candidate{Binary: request.Binary}, args...)
	filter := diagnostics.NewFilter(func(code string) { _ = json.NewEncoder(os.Stderr).Encode(code) })
	cmd.Stdout, cmd.Stderr = filter, filter
	cmd.SysProcAttr = coreAttributes()
	if cmd.Start() != nil {
		return 3, true
	}
	// 仅报告本次实际子进程；核心自己的输出仍丢弃，不能混入管道。
	if json.NewEncoder(os.Stdout).Encode(cmd.Process.Pid) != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 3, true
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); filter.Flush(); done <- err }()
	select {
	case err = <-done:
		if err != nil {
			return 1, true
		}
		return 0, true
	case <-parentGone:
	case <-stopSignal:
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return 0, true
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return 0, true
	}
}

func startManaged(c Candidate, preflight bool) (*process, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	pidReader, pidWriter, err := os.Pipe()
	if err != nil {
		writer.Close()
		return nil, err
	}
	defer pidReader.Close()
	defer pidWriter.Close()
	data, err := json.Marshal(supervisorRequest{Binary: c.Binary, File: c.File, Preflight: preflight})
	if err != nil {
		writer.Close()
		return nil, err
	}
	cmd := exec.Command(self, supervisorArgument)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Stdin = bytes.NewReader(data)
	cmd.ExtraFiles = []*os.File{reader}
	cmd.Stdout, cmd.Stderr = pidWriter, diagnostics.NewReceiver(c.Diagnostics)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p := &process{cmd: cmd, lifetime: writer, done: make(chan struct{}), started: time.Now().UTC()}
	if err = cmd.Start(); err != nil {
		writer.Close()
		return nil, err
	}
	_ = pidWriter.Close()
	go func() {
		p.result = cmd.Wait()
		// 监督进程自身异常退出时，清理仍在本次专属进程组内的子进程。
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = writer.Close()
		p.exited = time.Now().UTC()
		if !preflight && !p.stopping.Load() {
			c.Diagnostics.Add("xray.unexpected")
		}
		close(p.done)
	}()
	timeout := time.AfterFunc(2*time.Second, func() { _ = pidReader.Close() })
	err = json.NewDecoder(io.LimitReader(pidReader, 32)).Decode(&p.corePID)
	timeout.Stop()
	if err != nil || p.corePID <= 0 || p.corePID == int32(cmd.Process.Pid) {
		_ = stop(p)
		return nil, i18n.New("无法确认本次核心子进程", "Cannot confirm this core child process")
	}
	return p, nil
}
