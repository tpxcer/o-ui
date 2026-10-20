package monitor

import (
	"context"
	"independent-panel/internal/i18n"
	"net"
	"runtime"
	"strconv"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
)

func collect(ctx context.Context, path string) raw {
	r := raw{}
	s := &r.snapshot
	s.Issues = []string{}
	if values, err := cpu.TimesWithContext(ctx, false); err == nil && len(values) == 1 {
		c := values[0]
		r.total, r.busy = cpuTimes(c.User, c.System, c.Idle, c.Nice, c.Iowait, c.Irq, c.Softirq, c.Steal)
	} else {
		s.Issues = append(s.Issues, i18n.T("CPU采集不可用", "CPU sampling unavailable"))
	}
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil && v.Available <= v.Total {
		used := v.Total - v.Available
		s.Memory = capacity(used, v.Total, float64(used)/float64(v.Total)*100)
	} else {
		s.Issues = append(s.Issues, i18n.T("内存采集不可用", "Memory sampling unavailable"))
	}
	if v, err := mem.SwapMemoryWithContext(ctx); err == nil {
		s.Swap = capacity(v.Used, v.Total, v.UsedPercent)
	} else {
		s.Issues = append(s.Issues, i18n.T("交换分区采集不可用", "Swap sampling unavailable"))
	}
	if v, err := disk.UsageWithContext(ctx, path); err == nil {
		s.Disk = capacity(v.Used, v.Total, v.UsedPercent)
	} else {
		s.Issues = append(s.Issues, i18n.T("数据目录所在文件系统采集不可用", "Sampling of the data directory's file system unavailable"))
	}
	if v, err := host.InfoWithContext(ctx); err == nil {
		s.Host = &Host{Name: v.Hostname, System: systemName(v.Platform, v.PlatformVersion, v.KernelVersion), Architecture: runtime.GOARCH, CPUs: runtime.NumCPU(), Uptime: v.Uptime}
		r.boot = v.BootTime
		if info, err := cpu.InfoWithContext(ctx); err == nil && len(info) > 0 {
			s.Host.CPUModel = info[0].ModelName
		}
	} else {
		s.Issues = append(s.Issues, i18n.T("主机信息采集不可用", "Host info sampling unavailable"))
	}
	if v, err := load.AvgWithContext(ctx); err == nil {
		s.Load = &[3]float64{v.Load1, v.Load5, v.Load15}
	} else {
		s.Issues = append(s.Issues, i18n.T("系统负载采集不可用", "Load average sampling unavailable"))
	}
	interfaces, interfaceErr := net.Interfaces()
	counters, counterErr := psnet.IOCountersWithContext(ctx, true)
	if interfaceErr == nil && counterErr == nil {
		selected := map[string]string{}
		for _, i := range interfaces {
			if i.Flags&net.FlagUp != 0 && i.Flags&net.FlagLoopback == 0 {
				selected[i.Name] = i.Name + ":" + strconv.Itoa(i.Index)
			}
		}
		for _, c := range counters {
			if id, ok := selected[c.Name]; ok {
				r.counters = append(r.counters, Counter{Name: c.Name, ID: id, Sent: c.BytesSent, Received: c.BytesRecv})
			}
		}
		if len(r.counters) == len(selected) {
			s.Network = network(r.counters)
		}
	}
	if s.Network == nil {
		r.counters = nil
		s.Issues = append(s.Issues, i18n.T("无活动非回环网卡或网卡计数不可用", "No active non-loopback interface, or interface counters unavailable"))
	}
	s.At = time.Now().UTC()
	return r
}
