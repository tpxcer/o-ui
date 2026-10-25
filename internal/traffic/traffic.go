// Package traffic实现独立累计账本，不清零Xray计数器，不将运行计数冒充永久用量。
package traffic

import (
	"bytes"
	"encoding/json"
	"independent-panel/internal/i18n"
	"math"
	"strconv"
	"strings"
	"time"
)

type Count struct {
	Uplink   uint64 `json:"uplink,string"`
	Downlink uint64 `json:"downlink,string"`
}

func (c Count) Total() uint64 {
	if math.MaxUint64-c.Uplink < c.Downlink {
		return math.MaxUint64
	}
	return c.Uplink + c.Downlink
}

type Ledger struct {
	Totals map[string]Count `json:"totals"`
	Last   map[string]Count `json:"last"`
	// LastOnline记录各客户端最近一次有流量的时刻（参照OUI的“最近上线”与2分钟在线判定）。
	LastOnline map[string]time.Time `json:"lastOnline,omitempty"`
	Epoch      string               `json:"epoch"`
	SampledAt  *time.Time           `json:"sampledAt,omitempty"`
	Active     bool                 `json:"active"`
	Incomplete bool                 `json:"incomplete"`
	Periods    map[string]Period    `json:"periods,omitempty"`
}

func Empty() Ledger { return Ledger{Totals: map[string]Count{}, Last: map[string]Count{}} }

// Parse处理protobuf JSON的十进制int64和省略的默认0值，拒绝无效或不完整流量项。
func Parse(data []byte) (map[string]Count, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil, i18n.New("统计响应不是JSON对象", "Statistics response is not a JSON object")
	}
	var response struct {
		Stat []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	if json.Unmarshal(data, &response) != nil {
		return nil, i18n.New("统计响应格式无效", "Invalid statistics response format")
	}
	result, seen := map[string]Count{}, map[string]bool{}
	for _, stat := range response.Stat {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || !strings.HasPrefix(parts[1], "client-") || parts[2] != "traffic" || (parts[3] != "uplink" && parts[3] != "downlink") {
			continue
		}
		if seen[stat.Name] {
			return nil, i18n.New("统计计数重复", "Duplicate statistics counter")
		}
		seen[stat.Name] = true
		s := "0"
		if len(stat.Value) != 0 {
			s = strings.Trim(string(stat.Value), `"`)
		}
		n, err := strconv.ParseUint(s, 10, 63)
		if err != nil {
			return nil, i18n.New("统计计数无效", "Invalid statistics counter")
		}
		id := strings.TrimPrefix(parts[1], "client-")
		if id == "" {
			return nil, i18n.New("统计客户端标识无效", "Invalid statistics client ID")
		}
		c := result[id]
		if parts[3] == "uplink" {
			c.Uplink = n
		} else {
			c.Downlink = n
		}
		result[id] = c
	}
	for id := range result {
		if !seen["user>>>client-"+id+">>>traffic>>>uplink"] || !seen["user>>>client-"+id+">>>traffic>>>downlink"] {
			return nil, i18n.New("统计计数不完整", "Incomplete statistics counters")
		}
	}
	return result, nil
}

// Merge不会修改输入。新进程从0累加，同进程回退视为采集缺口，拒绝猜测补数。
func Merge(old Ledger, epoch string, counts map[string]Count, now time.Time) (Ledger, error) {
	if epoch == "" || now.IsZero() || old.Totals == nil || old.Last == nil || !ValidPeriods(old) {
		return Ledger{}, i18n.New("流量账本或运行实例无效", "Invalid traffic ledger or run instance")
	}
	next := Empty()
	next.Epoch = epoch
	next.Active, next.Incomplete = true, old.Incomplete
	sampled := now.UTC()
	next.SampledAt = &sampled
	for id, c := range old.Totals {
		next.Totals[id] = c
	}
	for id, at := range old.LastOnline {
		if next.LastOnline == nil {
			next.LastOnline = map[string]time.Time{}
		}
		next.LastOnline[id] = at
	}
	if old.Epoch == epoch {
		for id, c := range old.Last {
			next.Last[id] = c
		}
	}
	for id, c := range counts {
		previous := next.Last[id]
		if c.Uplink < previous.Uplink || c.Downlink < previous.Downlink {
			return Ledger{}, i18n.New("同一核心实例计数回退，存在采集缺口", "Counters went backwards on the same core instance; there is a sampling gap")
		}
		total := next.Totals[id]
		up, down := c.Uplink-previous.Uplink, c.Downlink-previous.Downlink
		if math.MaxUint64-total.Uplink < up || math.MaxUint64-total.Downlink < down {
			return Ledger{}, i18n.New("累计流量超出存储范围", "Traffic total exceeds the storable range")
		}
		total.Uplink += up
		total.Downlink += down
		next.Totals[id] = total
		if up+down > 0 {
			if next.LastOnline == nil {
				next.LastOnline = map[string]time.Time{}
			}
			next.LastOnline[id] = sampled
		}
		next.Last[id] = c
	}
	next.Periods = mergePeriods(old, next, now)
	return next, nil
}
