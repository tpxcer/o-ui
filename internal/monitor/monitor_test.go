package monitor

import (
	"context"
	"math"
	"testing"
	"time"
)

func sample(at time.Time, total, busy float64, values []Counter) raw {
	return raw{snapshot: Snapshot{At: at, Network: network(values)}, total: number(total), busy: number(busy), counters: values}
}
func TestSamplingBoundaries(t *testing.T) {
	at := time.Now()
	base := []Counter{{Name: "eth0", ID: "eth0:1", Sent: 100, Received: 200}}
	m := &Monitor{}
	m.update(sample(at, 100, 10, base))
	if s := m.Snapshot(); s.CPU != nil || s.Network.Upload != nil || len(s.History) != 1 {
		t.Fatal("首样本不产生速率", s)
	}
	next := []Counter{{Name: "eth0", ID: "eth0:1", Sent: 300, Received: 600}}
	m.update(sample(at.Add(2*time.Second), 300, 60, next))
	s := m.Snapshot()
	if *s.CPU != 25 || *s.Network.Upload != 100 || *s.Network.Download != 200 {
		t.Fatal(s)
	}
	s.History[0].Upload = number(999)
	if m.Snapshot().History[0].Upload != nil {
		t.Fatal("快照数组别名")
	}
	for i := 2; i < 110; i++ {
		m.update(sample(at.Add(time.Duration(i)*2*time.Second), float64(i*200), float64(i*50), next))
	}
	if len(m.Snapshot().History) != historyLimit {
		t.Fatal("历史未设上界")
	}
	m.update(sample(at.Add(time.Hour), 40000, 8000, next))
	if s = m.Snapshot(); s.CPU != nil || s.Network.Upload != nil {
		t.Fatal("长间隔不能连线", s)
	}
}
func TestNetworkChangesAndReset(t *testing.T) {
	base := []Counter{{Name: "eth0", ID: "eth0:1", Sent: 100, Received: 100}}
	for _, v := range []struct {
		name   string
		values []Counter
	}{
		{"重置", []Counter{{Name: "eth0", ID: "eth0:1", Sent: 1, Received: 200}}},
		{"同名重建", []Counter{{Name: "eth0", ID: "eth0:2", Sent: 200, Received: 200}}},
		{"增加", []Counter{{Name: "eth0", ID: "eth0:1", Sent: 200, Received: 200}, {Name: "eth1", ID: "eth1:2"}}},
		{"移除", nil},
	} {
		t.Run(v.name, func(t *testing.T) {
			if u, d := rates(base, v.values, 2); u != nil || d != nil {
				t.Fatal("不连续不应有速率")
			}
		})
	}
	if u, _ := rates(base, base, 0); u != nil {
		t.Fatal("零间隔")
	}
	if u, d := rates(base, base, 2); u == nil || *u != 0 || *d != 0 {
		t.Fatal("有效静止网卡应为0")
	}
	if network([]Counter{{Sent: math.MaxUint64}, {Sent: 1}}) != nil {
		t.Fatal("累计溢出")
	}
	before := []Counter{{ID: "1"}, {ID: "2"}}
	after := []Counter{{ID: "1", Sent: math.MaxUint64}, {ID: "2", Sent: 1}}
	if u, _ := rates(before, after, 2); u != nil {
		t.Fatal("差值溢出")
	}
}
func TestUnavailableDoesNotReusePreviousValues(t *testing.T) {
	at := time.Now()
	m := &Monitor{}
	m.update(sample(at, 100, 20, []Counter{{ID: "1", Sent: 200}}))
	m.update(raw{snapshot: Snapshot{At: at.Add(2 * time.Second), Issues: []string{"采集失败"}}})
	if s := m.Snapshot(); s.CPU != nil || s.Network != nil || s.Memory != nil || len(s.Issues) != 1 {
		t.Fatal(s)
	}
	m.update(sample(at.Add(4*time.Second), 200, 40, []Counter{{ID: "1", Sent: 600}}))
	if s := m.Snapshot(); s.CPU != nil || s.Network.Upload != nil {
		t.Fatal("失败后的首样本只建基线")
	}
	if capacity(0, 0, 0).Percent != nil || capacity(2, 1, 200) != nil || number(math.NaN()) != nil {
		t.Fatal("容量有效性")
	}
}
func TestCloseCancelsCollectorAndConcurrentSnapshots(t *testing.T) {
	entered := make(chan struct{})
	m := start(func(ctx context.Context) raw { close(entered); <-ctx.Done(); return raw{} }, time.Hour)
	<-entered
	for i := 0; i < 20; i++ {
		go m.Snapshot()
	}
	m.Close()
	select {
	case <-m.done:
	default:
		t.Fatal("采集器未退出")
	}
}
func TestLocalSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := collect(ctx, t.TempDir())
	if r.snapshot.At.IsZero() || r.total == nil || r.busy == nil || r.snapshot.Memory == nil || r.snapshot.Disk == nil || r.snapshot.Host == nil {
		t.Fatalf("本机基础资源采集失败：%v", r.snapshot.Issues)
	}
	if r.snapshot.Memory.Percent == nil || r.snapshot.Disk.Percent == nil {
		t.Fatal("缺失容量百分比")
	}
}

// 网卡总速度参照OUI只算物理网卡；网卡名单仍列出全部，供服务器套餐选择。
func TestNetworkSpeedSkipsVirtualInterfaces(t *testing.T) {
	at := time.Now()
	counters := func(eth, virtual uint64) []Counter {
		list := []Counter{{Name: "eth0", ID: "eth0:2", Sent: eth, Received: eth}}
		for i, name := range []string{"lo", "docker0", "br-1a2b", "veth9f", "tun0", "wg0", "tailscale0", "Virbr0"} {
			list = append(list, Counter{Name: name, ID: name + ":" + string(rune('a'+i)), Sent: virtual, Received: virtual})
		}
		return list
	}
	m := &Monitor{}
	m.update(sample(at, 100, 10, counters(1000, 1000)))
	m.update(sample(at.Add(2*time.Second), 200, 20, counters(3000, 999000)))
	s := m.Snapshot()
	if s.Network.Upload == nil || *s.Network.Upload != 1000 || *s.Network.Download != 1000 {
		t.Fatal("虚拟网卡流量不应计入网卡总速度", s.Network)
	}
	if len(s.Network.Interfaces) != 9 {
		t.Fatal("网卡名单应保留全部网卡", s.Network.Interfaces)
	}
}
