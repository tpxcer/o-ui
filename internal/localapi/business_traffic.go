package localapi

import (
	"independent-panel/internal/monitor"
	"independent-panel/internal/traffic"
	"math"
	"strconv"
	"sync"
	"time"
)

// 首页网速与累计只统计本面板客户端经Xray产生的业务流量（统计接口user计数），不读网卡。
type businessTraffic struct {
	mu     sync.Mutex
	epoch  string
	last   traffic.Count
	lastAt time.Time
	totals *traffic.Count
	points []monitor.Point
}

func sumCounts(counts map[string]traffic.Count) (traffic.Count, bool) {
	var total traffic.Count
	for _, c := range counts {
		if math.MaxUint64-total.Uplink < c.Uplink || math.MaxUint64-total.Downlink < c.Downlink {
			return traffic.Count{}, false
		}
		total.Uplink += c.Uplink
		total.Downlink += c.Downlink
	}
	return total, true
}

// observe记录一次采样：epoch为空表示核心未运行，业务速度为0；同一实例连续采样才计算速率。
func (b *businessTraffic) observe(epoch string, raw, totals map[string]traffic.Count, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := monitor.Point{At: now}
	current, ok := sumCounts(raw)
	gap := now.Sub(b.lastAt)
	switch {
	case epoch == "":
		zero := 0.0
		p.Upload, p.Download = &zero, &zero
	case ok && epoch == b.epoch && gap > 0 && gap <= 3*trafficInterval && current.Uplink >= b.last.Uplink && current.Downlink >= b.last.Downlink:
		up, down := float64(current.Uplink-b.last.Uplink)/gap.Seconds(), float64(current.Downlink-b.last.Downlink)/gap.Seconds()
		p.Upload, p.Download = &up, &down
	}
	if ok {
		b.epoch, b.last, b.lastAt = epoch, current, now
	} else {
		b.epoch, b.lastAt = "", time.Time{}
	}
	if t, ok := sumCounts(totals); ok {
		b.totals = &t
	}
	b.points = append(b.points, p)
	if len(b.points) > 90 {
		b.points = append([]monitor.Point(nil), b.points[len(b.points)-90:]...)
	}
}

// network返回当前业务速度、累计上传/下载与最近约3分钟历史；尚无采样时为nil。
func (b *businessTraffic) network() (*monitor.Network, []monitor.Point) {
	b.mu.Lock()
	defer b.mu.Unlock()
	history := append([]monitor.Point{}, b.points...)
	if b.totals == nil {
		return nil, history
	}
	n := &monitor.Network{Interfaces: []string{}, Sent: strconv.FormatUint(b.totals.Uplink, 10), Received: strconv.FormatUint(b.totals.Downlink, 10)}
	if len(b.points) > 0 && time.Since(b.points[len(b.points)-1].At) <= 3*trafficInterval {
		last := b.points[len(b.points)-1]
		n.Upload, n.Download = last.Upload, last.Download
	}
	return n, history
}
