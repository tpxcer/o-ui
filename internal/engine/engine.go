// Package engine只控制由本项目启动的子进程，不按端口或进程名接管其他核心。
package engine

import (
	"context"
	"errors"
	"independent-panel/internal/i18n"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"independent-panel/internal/diagnostics"
)

type Candidate struct {
	Binary            string
	Version           string
	File              string
	WorkspaceRevision int64
	SettingsRevision  int64
	Inbounds          int
	StatsAddress      string
	PolicyFile        string
	Diagnostics       *diagnostics.Log `json:"-"`
}

type Status struct {
	Running           bool       `json:"running"`
	Version           string     `json:"version,omitempty"`
	WorkspaceRevision int64      `json:"workspaceRevision"`
	SettingsRevision  int64      `json:"settingsRevision"`
	Inbounds          int        `json:"inbounds"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	UnexpectedExit    bool       `json:"unexpectedExit,omitempty"`
	ExitedAt          *time.Time `json:"exitedAt,omitempty"`
}

type process struct {
	cmd      *exec.Cmd
	corePID  int32
	stopping atomic.Bool
	done     chan struct{}
	started  time.Time
	lifetime *os.File
	result   error
	exited   time.Time
}

type Engine struct {
	mu      sync.Mutex
	process *process
	applied *Candidate
	closed  bool
}

// Current只返回本控制器当前仍存活的配置，调用者不得把文件路径直接暴露给网页。
func (e *Engine) Current() (Candidate, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.applied == nil || e.process == nil {
		return Candidate{}, false
	}
	select {
	case <-e.process.done:
		return Candidate{}, false
	default:
		return *e.applied, true
	}
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{}
	if e.applied != nil {
		s.Version, s.WorkspaceRevision, s.SettingsRevision, s.Inbounds = e.applied.Version, e.applied.WorkspaceRevision, e.applied.SettingsRevision, e.applied.Inbounds
	}
	if e.process != nil {
		select {
		case <-e.process.done:
			s.UnexpectedExit = true
			exited := e.process.exited
			s.ExitedAt = &exited
		default:
			s.Running = true
			started := e.process.started
			s.StartedAt = &started
		}
	}
	return s
}

func command(c Candidate, args ...string) *exec.Cmd {
	cmd := exec.Command(c.Binary, args...)
	cmd.Dir = filepath.Dir(c.Binary)
	// 不继承外部XRAY配置/资源环境，也不回传可能包含凭据的核心输出。
	cmd.Env = []string{"PATH=/usr/bin:/bin", "XRAY_LOCATION_ASSET=" + filepath.Dir(c.Binary)}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd
}

func Preflight(ctx context.Context, c Candidate) error {
	c.Diagnostics.Add("xray.preflight")
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := startManaged(c, true)
	if err != nil {
		c.Diagnostics.Add("xray.preflight.failed")
		return i18n.New("官方核心预检无法启动，请检查所选版本", "Official core preflight could not start; check the selected version")
	}
	select {
	case <-p.done:
		if p.result != nil {
			c.Diagnostics.Add("xray.preflight.failed")
			return i18n.New("官方核心预检未通过；当前运行配置未改变，请检查入站参数与六类设置", "Official core preflight failed; running configuration unchanged, check inbound parameters and the six settings categories")
		}
		c.Diagnostics.Add("xray.preflight.ok")
		return nil
	case <-ctx.Done():
		_ = stop(p)
		c.Diagnostics.Add("xray.preflight.cancelled")
		return i18n.New("官方核心预检超时或已取消；当前运行配置未改变", "Official core preflight timed out or was cancelled; running configuration unchanged")
	}
}

func launch(c Candidate) (*process, error) {
	p, err := startManaged(c, false)
	if err != nil {
		c.Diagnostics.Add("xray.start.failed")
		return nil, err
	}
	// 同时核验自有子进程存活，不能只因同端口有其他服务便宣称启动成功。
	select {
	case <-p.done:
		c.Diagnostics.Add("xray.start.failed")
		return nil, i18n.New("核心在启动阶段退出", "Core exited during startup")
	case <-time.After(700 * time.Millisecond):
		c.Diagnostics.Add("xray.running")
		return p, nil
	}
}

func stop(p *process) error {
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	p.stopping.Store(true)
	_ = p.lifetime.Close()
	select {
	case <-p.done:
		return nil
	case <-time.After(5 * time.Second):
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return i18n.New("停止自有核心失败，未启动另一进程", "Failed to stop the core; no other process was started")
		}
		<-p.done
		return nil
	}
}

// Apply在旧核心停止前预检；启动失败时恢复同一个旧版本/配置文件。
func (e *Engine) Apply(ctx context.Context, c Candidate) error {
	return e.ApplyChecked(ctx, c, nil, nil)
}

// ApplyChecked在预检成功、停旧核心前保存检查点；失败不重启旧进程。
func (e *Engine) ApplyChecked(ctx context.Context, c Candidate, checkpoint func(Candidate, time.Time) error, validate func() error, onUnexpected ...func() error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return i18n.New("核心控制器已关闭", "Core controller is closed")
	}
	if err := Preflight(ctx, c); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return i18n.New("应用已取消，当前核心未改变", "Apply cancelled; current core unchanged")
	}
	old, oldProcess := e.applied, e.process
	wasRunning := false
	if oldProcess != nil {
		select {
		case <-oldProcess.done:
		default:
			wasRunning = true
		}
	}
	if wasRunning && checkpoint != nil {
		if err := checkpoint(*old, oldProcess.started); err != nil {
			return err
		}
	}
	if oldProcess != nil && !wasRunning && len(onUnexpected) > 0 {
		if err := onUnexpected[0](); err != nil {
			return err
		}
	}
	if validate != nil {
		if err := validate(); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return i18n.New("应用已取消，当前核心未改变", "Apply cancelled; current core unchanged")
	}
	if err := stop(oldProcess); err != nil {
		return err
	}
	e.process = nil
	if c.Inbounds == 0 {
		e.applied = &c
		return nil
	}
	p, err := launch(c)
	if err != nil {
		if wasRunning && old != nil {
			e.process, err = launch(*old)
			if err != nil {
				c.Diagnostics.Add("xray.rollback.failed")
				return i18n.New("新配置启动失败，旧配置恢复也失败；核心已停止，草稿和旧配置均保留", "New configuration failed to start and restoring the old one also failed; core stopped, draft and old configuration kept")
			}
			c.Diagnostics.Add("xray.rollback.ok")
			return i18n.New("新配置启动失败，已恢复原版本和配置；草稿保留，请检查端口占用", "New configuration failed to start; previous version and configuration restored, draft kept, check port usage")
		}
		return i18n.New("核心启动失败，未标记为运行；请检查端口占用与配置", "Core failed to start and is not marked running; check port usage and configuration")
	}
	e.process, e.applied = p, &c
	return nil
}

func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := stop(e.process); err != nil {
		return err
	}
	if e.process != nil && e.applied != nil {
		e.applied.Diagnostics.Add("xray.stopped")
	}
	e.process = nil
	return nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	err := stop(e.process)
	if err == nil {
		e.process = nil
	}
	return err
}
