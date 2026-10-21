// Package monitor只读采集本机系统资源，不扫描或接管代理进程。
package monitor

import (
	"context"
	"independent-panel/internal/i18n"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Interval = 2 * time.Second
const historyLimit = 90

type Capacity struct {
	Used    string   `json:"used"`
	Total   string   `json:"total"`
	Percent *float64 `json:"percent"`
}
type Host struct {
	Name         string `json:"name"`
	System       string `json:"system"`
	Architecture string `json:"architecture"`
	CPUs         int    `json:"cpus"`
	CPUModel     string `json:"cpuModel"`
	Uptime       uint64 `json:"uptime"`
}
type Network struct {
	Interfaces []string `json:"interfaces"`
	Sent       string   `json:"sent"`
	Received   string   `json:"received"`
	Upload     *float64 `json:"upload"`
	Download   *float64 `json:"download"`
}
type Point struct {
	At       time.Time `json:"at"`
	Upload   *float64  `json:"upload"`
	Download *float64  `json:"download"`
}
type Snapshot struct {
	At      time.Time   `json:"at"`
	CPU     *float64    `json:"cpu"`
	Memory  *Capacity   `json:"memory"`
	Swap    *Capacity   `json:"swap"`
	Disk    *Capacity   `json:"disk"`
	Host    *Host       `json:"host"`
	Load    *[3]float64 `json:"load"`
	Network *Network    `json:"network"`
	Issues  []string    `json:"issues"`
	History []Point     `json:"history"`
}

type Counter struct {
	Name, ID       string
	Sent, Received uint64
}
type raw struct {
	snapshot    Snapshot
	total, busy *float64
	counters    []Counter
	boot        uint64
}
type source func(context.Context) raw
type Monitor struct {
	mu       sync.RWMutex
	current  Snapshot
	previous raw
	read     source
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(path string) *Monitor {
	return start(func(ctx context.Context) raw { return collect(ctx, path) }, Interval)
}
func start(read source, interval time.Duration) *Monitor {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Monitor{read: read, cancel: cancel, done: make(chan struct{}), current: Snapshot{Issues: []string{i18n.T("等待首次系统采样", "Waiting for the first system sample")}, History: []Point{}}}
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			sampleCtx, stop := context.WithTimeout(ctx, 1500*time.Millisecond)
			r := m.read(sampleCtx)
			stop()
			if ctx.Err() != nil {
				return
			}
			m.update(r)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return m
}
func (m *Monitor) Close() { m.cancel(); <-m.done }
func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.current
	s.History = append([]Point{}, s.History...)
	s.Issues = append([]string{}, s.Issues...)
	return s
}

// Counters返回最近一次网卡原始累计及开机时刻，供月用量按差值累计；不可用时boot为0。
func (m *Monitor) Counters() (time.Time, uint64, []Counter) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.previous.snapshot.At, m.previous.boot, append([]Counter(nil), m.previous.counters...)
}

func (m *Monitor) update(r raw) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := r.snapshot
	gap := s.At.Sub(m.previous.snapshot.At)
	continuous := !m.previous.snapshot.At.IsZero() && gap > 0 && gap <= 3*Interval
	if continuous && r.total != nil && r.busy != nil && m.previous.total != nil && m.previous.busy != nil {
		total, busy := *r.total-*m.previous.total, *r.busy-*m.previous.busy
		if total > 0 && busy >= 0 && busy <= total {
			s.CPU = number(100 * busy / total)
		}
	}
	if s.Network != nil && continuous {
		s.Network.Upload, s.Network.Download = rates(physical(m.previous.counters), physical(r.counters), gap.Seconds())
	}
	history := append([]Point{}, m.current.History...)
	p := Point{At: s.At}
	if s.Network != nil {
		p.Upload, p.Download = s.Network.Upload, s.Network.Download
	}
	history = append(history, p)
	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}
	s.History = history
	if s.Issues == nil {
		s.Issues = []string{}
	}
	m.current, m.previous = s, r
}

// 网卡总速度参照OUI只算物理网卡：排除回环与docker、veth、tun、wg等虚拟网卡，避免容器/隧道流量重复计入。
// 网卡名单与原始累计仍包含全部活动网卡，供服务器套餐选择计量网卡。
var virtualPrefixes = []string{"loopback", "docker", "br-", "veth", "virbr", "tun", "tap", "wg", "tailscale", "zt"}

func physical(counters []Counter) []Counter {
	result := []Counter{}
	for _, c := range counters {
		name := strings.ToLower(c.Name)
		virtual := name == "lo" || name == "lo0"
		for _, prefix := range virtualPrefixes {
			virtual = virtual || strings.HasPrefix(name, prefix)
		}
		if !virtual {
			result = append(result, c)
		}
	}
	return result
}

func number(n float64) *float64 {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}
func capacity(used, total uint64, usage float64) *Capacity {
	if used > total {
		return nil
	}
	c := &Capacity{Used: strconv.FormatUint(used, 10), Total: strconv.FormatUint(total, 10)}
	if total > 0 && usage >= 0 && usage <= 100 {
		c.Percent = number(usage)
	}
	return c
}
func rates(before, after []Counter, seconds float64) (*float64, *float64) {
	if seconds <= 0 || len(before) == 0 || len(before) != len(after) {
		return nil, nil
	}
	old := map[string]Counter{}
	for _, c := range before {
		old[c.ID] = c
	}
	var sent, received uint64
	for _, c := range after {
		previous, ok := old[c.ID]
		if !ok || c.Sent < previous.Sent || c.Received < previous.Received {
			return nil, nil
		}
		s, r := c.Sent-previous.Sent, c.Received-previous.Received
		if math.MaxUint64-sent < s || math.MaxUint64-received < r {
			return nil, nil
		}
		sent, received = sent+s, received+r
	}
	return number(float64(sent) / seconds), number(float64(received) / seconds)
}
func network(counters []Counter) *Network {
	if len(counters) == 0 {
		return nil
	}
	n := &Network{Interfaces: []string{}}
	var sent, received uint64
	for _, c := range counters {
		if math.MaxUint64-sent < c.Sent || math.MaxUint64-received < c.Received {
			return nil
		}
		sent, received = sent+c.Sent, received+c.Received
		n.Interfaces = append(n.Interfaces, c.Name)
	}
	sort.Strings(n.Interfaces)
	n.Sent, n.Received = strconv.FormatUint(sent, 10), strconv.FormatUint(received, 10)
	return n
}

func cpuTimes(user, system, idle, nice, iowait, irq, softirq, steal float64) (*float64, *float64) {
	total := user + system + idle + nice + iowait + irq + softirq + steal
	return number(total), number(total - idle - iowait)
}

func systemName(platform, version, kernel string) string {
	return strings.TrimSpace(platform + " " + version + " · " + kernel)
}
