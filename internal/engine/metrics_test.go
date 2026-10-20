package engine

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func TestSocketCounts(t *testing.T) {
	rows := []gnet.ConnectionStat{
		{Pid: 12, Family: syscall.AF_INET, Type: syscall.SOCK_STREAM, Status: "ESTABLISHED"},
		{Pid: 12, Family: syscall.AF_INET6, Type: syscall.SOCK_STREAM, Status: "ESTABLISHED"},
		{Pid: 12, Family: syscall.AF_INET, Type: syscall.SOCK_STREAM, Status: "LISTEN"},
		{Pid: 12, Family: syscall.AF_UNIX, Type: syscall.SOCK_STREAM, Status: "ESTABLISHED"},
		{Pid: 13, Family: syscall.AF_INET, Type: syscall.SOCK_STREAM, Status: "ESTABLISHED"},
		{Pid: 12, Family: syscall.AF_INET, Type: syscall.SOCK_DGRAM},
		{Pid: 12, Family: syscall.AF_INET6, Type: syscall.SOCK_DGRAM},
	}
	if tcp, udp := socketCounts(rows, 12); tcp != 2 || udp != 2 {
		t.Fatal(tcp, udp)
	}
	if tcp, udp := socketCounts(nil, 12); tcp != 0 || udp != 0 {
		t.Fatal("真实空集合不是零")
	}
}

func TestCollectMetricsOwnershipAndFailure(t *testing.T) {
	ctx := context.Background()
	current := int32(os.Getpid())
	m := collectMetrics(ctx, current, int32(os.Getppid()))
	if m.Memory == nil || m.Threads == nil || *m.Threads <= 0 {
		t.Fatal("无法采样测试自身RSS/线程", m.Issues)
	}
	for _, m := range []Metrics{collectMetrics(ctx, current, current), collectMetrics(ctx, -1, current)} {
		if m.Memory != nil || m.Threads != nil || m.TCP != nil || m.UDP != nil || len(m.Issues) == 0 {
			t.Fatal("所有权失败仍有读数")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	m = collectMetrics(cancelled, current, int32(os.Getppid()))
	if m.Memory != nil || m.Threads != nil || m.TCP != nil || m.UDP != nil {
		t.Fatal("取消后保留读数")
	}
}

func waitMetrics(t *testing.T, p *MetricsProbe, ready func(Metrics) bool) Metrics {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m := p.Snapshot()
		if ready(m) {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatal("核心指标未就绪", m.Issues)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMetricsProbeOwnedLifecycle(t *testing.T) {
	e := &Engine{}
	defer e.Close()
	p := startMetricsProbe(e, collectMetrics, 50*time.Millisecond)
	defer p.Close()
	if m := p.Snapshot(); m.Running || m.Memory != nil {
		t.Fatal("空引擎假读数")
	}
	foreign, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	c := fakeCandidate(t, freeAddress(t), false, false)
	data, _ := os.ReadFile(c.File)
	var fixture map[string]any
	_ = json.Unmarshal(data, &fixture)
	fixture["UDP"] = true
	data, _ = json.Marshal(fixture)
	if err := os.WriteFile(c.File, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if e.process.corePID <= 0 || e.process.corePID == int32(e.process.cmd.Process.Pid) || e.process.corePID == int32(os.Getpid()) {
		t.Fatal("未取得实际子核心PID")
	}
	m := waitMetrics(t, p, func(m Metrics) bool { return m.Memory != nil && m.TCP != nil })
	if *m.TCP != 0 || *m.UDP != 1 || m.Threads == nil {
		t.Fatal("混入监听或外部套接字", m)
	}
	conn, err := net.Dial("tcp", fixture["Address"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitMetrics(t, p, func(m Metrics) bool { return m.TCP != nil && *m.TCP == 1 && m.UDP != nil && *m.UDP == 1 })
	data, _ = json.Marshal(m)
	if strings.Contains(string(data), "pid") || strings.Contains(string(data), c.File) {
		t.Fatal("指标包含私有进程信息")
	}
	// 采集故障不得借上一份成功数据继续显示；过期数据在API层也撤下。
	cache := &MetricsProbe{engine: e, current: Metrics{At: time.Now().Add(-11 * time.Second), Running: true, StartedAt: m.StartedAt, Memory: m.Memory, TCP: m.TCP}}
	if stale := cache.Snapshot(); stale.Memory != nil || stale.TCP != nil {
		t.Fatal("过期读数仍有效")
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	m = p.Snapshot()
	if m.Running || m.Memory != nil || m.TCP != nil || m.StartedAt != nil {
		t.Fatal("停止后保留旧读数")
	}
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	waitMetrics(t, p, func(m Metrics) bool { return m.Memory != nil })
	if err := e.process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-e.process.done
	m = p.Snapshot()
	if m.Running || m.Threads != nil || m.UDP != nil {
		t.Fatal("异常退出后保留指标")
	}
}

func TestMetricsProbeFailureReplacesSuccessfulReadings(t *testing.T) {
	e := &Engine{}
	defer e.Close()
	if err := e.Apply(context.Background(), fakeCandidate(t, freeAddress(t), false, false)); err != nil {
		t.Fatal(err)
	}
	next := make(chan Metrics, 2)
	value := "1234"
	next <- Metrics{At: time.Now(), Memory: &value, Issues: []string{}}
	p := startMetricsProbe(e, func(ctx context.Context, _, _ int32) Metrics {
		select {
		case m := <-next:
			return m
		case <-ctx.Done():
			return Metrics{}
		}
	}, 10*time.Millisecond)
	defer p.Close()
	waitMetrics(t, p, func(m Metrics) bool { return m.Memory != nil })
	next <- Metrics{At: time.Now(), Issues: []string{"模拟采集权限不足"}}
	m := waitMetrics(t, p, func(m Metrics) bool { return len(m.Issues) == 1 && m.Issues[0] == "模拟采集权限不足" })
	if m.Memory != nil || m.Threads != nil || m.TCP != nil || m.UDP != nil {
		t.Fatal("失败保留上次成功指标")
	}
}

func TestMetricsProbeDoesNotBlockEngineOrPublishOldSample(t *testing.T) {
	e := &Engine{}
	defer e.Close()
	c := fakeCandidate(t, freeAddress(t), false, false)
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseRead := func() { releaseOnce.Do(func() { close(release) }) }
	p := startMetricsProbe(e, func(_ context.Context, _, _ int32) Metrics {
		close(entered)
		<-release
		value := "1234"
		return Metrics{At: time.Now(), Memory: &value}
	}, time.Hour)
	defer p.Close()
	defer releaseRead()
	<-entered
	e.mu.Lock()
	start := time.Now()
	m := p.Snapshot()
	e.mu.Unlock()
	if time.Since(start) > 100*time.Millisecond || m.Memory != nil {
		t.Fatal("采样等待引擎锁或返回旧值")
	}
	// 即使采集一直未返回，核心仍可正常停止（race子进程退出本身有额外延迟）。
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	releaseRead()
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.RLock()
		stored := p.current.Memory != nil
		p.mu.RUnlock()
		if stored {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("夹具未发布延迟结果")
		}
		time.Sleep(time.Millisecond)
	}
	if m := p.Snapshot(); m.Memory != nil || !m.Running {
		t.Fatal("旧实例结果冒充新实例")
	}
}
