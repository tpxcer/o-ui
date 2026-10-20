package engine

import (
	"context"
	"independent-panel/internal/i18n"
	"strconv"
	"sync"
	"syscall"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
	gprocess "github.com/shirou/gopsutil/v4/process"
)

type Metrics struct {
	At        time.Time  `json:"at"`
	Running   bool       `json:"running"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	Memory    *string    `json:"memory"`
	Threads   *int32     `json:"threads"`
	TCP       *int       `json:"tcp"`
	UDP       *int       `json:"udp"`
	Issues    []string   `json:"issues"`
}

// MetricsProbe独立只读采样，不持有运行/流量锁，不记录连接地址或PID到网页。
type MetricsProbe struct {
	engine  *Engine
	mu      sync.RWMutex
	current Metrics
	cancel  context.CancelFunc
	done    chan struct{}
}

func (e *Engine) metricTarget() (*process, string) {
	if !e.mu.TryLock() {
		return nil, i18n.T("核心正在切换，指标暂不可用", "Core is switching; metrics temporarily unavailable")
	}
	defer e.mu.Unlock()
	if e.closed || e.process == nil {
		return nil, i18n.T("核心已停止", "Core is stopped")
	}
	select {
	case <-e.process.done:
		return nil, i18n.T("核心异常退出，指标已失效", "Core exited unexpectedly; metrics invalidated")
	default:
		return e.process, ""
	}
}

type metricsSource func(context.Context, int32, int32) Metrics

func NewMetricsProbe(e *Engine) *MetricsProbe {
	return startMetricsProbe(e, collectMetrics, 2*time.Second)
}

func startMetricsProbe(e *Engine, read metricsSource, interval time.Duration) *MetricsProbe {
	ctx, cancel := context.WithCancel(context.Background())
	p := &MetricsProbe{engine: e, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			m := p.Snapshot()
			if target, _ := e.metricTarget(); target != nil {
				sampleCtx, stop := context.WithTimeout(ctx, 1500*time.Millisecond)
				m = read(sampleCtx, target.corePID, int32(target.cmd.Process.Pid))
				stop()
				m.Running, m.StartedAt = true, &target.started
			}
			p.mu.Lock()
			p.current = m
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return p
}

func (p *MetricsProbe) Close() { p.cancel(); <-p.done }

func (p *MetricsProbe) Snapshot() Metrics {
	target, issue := p.engine.metricTarget()
	if target == nil {
		return Metrics{At: time.Now().UTC(), Issues: []string{issue}}
	}
	p.mu.RLock()
	m := p.current
	m.Issues = append([]string{}, m.Issues...)
	p.mu.RUnlock()
	if m.StartedAt == nil || !m.StartedAt.Equal(target.started) {
		return Metrics{Running: true, StartedAt: &target.started, Issues: []string{i18n.T("等待当前核心首次采样", "Waiting for the first sample of the current core")}}
	}
	if current, _ := p.engine.metricTarget(); current != target {
		return Metrics{At: time.Now().UTC(), Issues: []string{i18n.T("核心实例已改变，指标已失效", "Core instance changed; metrics invalidated")}}
	}
	if age := time.Since(m.At); age > 10*time.Second || age < -5*time.Second {
		return Metrics{At: m.At, Running: true, StartedAt: &target.started, Issues: []string{i18n.T("核心采样已过期，读数已撤下", "Core sample expired; readings withdrawn")}}
	}
	return m
}

func socketCounts(connections []gnet.ConnectionStat, pid int32) (int, int) {
	tcp, udp := 0, 0
	for _, c := range connections {
		if c.Pid != pid || (c.Family != syscall.AF_INET && c.Family != syscall.AF_INET6) {
			continue
		}
		if c.Type == syscall.SOCK_STREAM && c.Status == "ESTABLISHED" {
			tcp++
		}
		if c.Type == syscall.SOCK_DGRAM {
			udp++
		}
	}
	return tcp, udp
}

func collectMetrics(ctx context.Context, pid, parent int32) Metrics {
	m := Metrics{At: time.Now().UTC(), Issues: []string{}}
	p, err := gprocess.NewProcessWithContext(ctx, pid)
	if err != nil || !ownedProcess(ctx, p, parent) {
		m.Issues = append(m.Issues, i18n.T("无法确认自有核心进程，未采集其他进程", "Cannot confirm this panel's core process; other processes are not sampled"))
		return m
	}
	if memory, err := p.MemoryInfoWithContext(ctx); err == nil && memory != nil && memory.RSS > 0 {
		value := strconv.FormatUint(memory.RSS, 10)
		m.Memory = &value
	} else {
		m.Issues = append(m.Issues, i18n.T("核心内存采集不可用", "Core memory sampling unavailable"))
	}
	if threads, err := p.NumThreadsWithContext(ctx); err == nil && threads > 0 {
		m.Threads = &threads
	} else {
		m.Issues = append(m.Issues, i18n.T("核心系统线程采集不可用", "Core thread sampling unavailable"))
	}
	if tcp, udp, err := readSockets(ctx, pid); err == nil {
		m.TCP, m.UDP = &tcp, &udp
	} else {
		m.Issues = append(m.Issues, i18n.T("核心套接字采集不可用（权限或系统能力不足）", "Core socket sampling unavailable (insufficient permission or capability)"))
	}
	if ctx.Err() != nil || !ownedProcess(ctx, p, parent) {
		return Metrics{At: m.At, Issues: []string{i18n.T("核心实例已改变或采样超时，读数已撤下", "Core instance changed or sampling timed out; readings withdrawn")}}
	}
	return m
}

func ownedProcess(ctx context.Context, p *gprocess.Process, parent int32) bool {
	if p == nil || parent <= 0 {
		return false
	}
	got, err := p.PpidWithContext(ctx)
	return err == nil && got == parent
}
