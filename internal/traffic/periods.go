package traffic

import "time"

// Period仅供展示；配额始终使用Ledger.Totals。按采样入账，不分摊边界增量。
// 只保留当前三种周期，空间不随时间增长。
type Period struct {
	Start   time.Time        `json:"start"`
	End     time.Time        `json:"end"`
	Since   time.Time        `json:"since"`
	Partial bool             `json:"partial"`
	Totals  map[string]Count `json:"totals"`
}

var periodKinds = [...]string{"day", "week", "month"}

func periodBounds(kind string, now time.Time) (time.Time, time.Time) {
	y, m, d := now.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch kind {
	case "week":
		start = start.AddDate(0, 0, -(int(start.Weekday())+6)%7)
		return start, start.AddDate(0, 0, 7)
	case "month":
		start = time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
		return start, start.AddDate(0, 1, 0)
	default:
		return start, start.AddDate(0, 0, 1)
	}
}

func mergePeriods(old, next Ledger, now time.Time) map[string]Period {
	result := make(map[string]Period, 3)
	for _, kind := range periodKinds {
		start, end := periodBounds(kind, now)
		previous, exists := old.Periods[kind]
		p := Period{Start: start, End: end, Since: start, Totals: map[string]Count{}}
		if exists && start.Before(previous.Start) {
			// 时钟回拨不倒退桶，也不把倒退期间增量计入未来周期。
			previous.Partial = true
			result[kind] = previous
			continue
		}
		if exists && start.Equal(previous.Start) && end.Equal(previous.End) {
			p.Since, p.Partial = previous.Since, previous.Partial
			for id, count := range previous.Totals {
				p.Totals[id] = count
			}
		}
		if !exists {
			// 老账本没有分段历史，首个样本只建基线。
			p.Since, p.Partial = now, true
		} else {
			if old.SampledAt == nil || now.Before(*old.SampledAt) || now.Sub(*old.SampledAt) > 10*time.Second {
				p.Partial = true
			}
			if old.SampledAt == nil || (old.SampledAt.Before(start) && now.Sub(*old.SampledAt) > 10*time.Second) {
				// 跨周期的长缺口无法分段，保留永久累计，不把整段计入新周期。
				p.Since = now
				result[kind] = p
				continue
			}
			for id, count := range next.Totals {
				before, current := old.Totals[id], p.Totals[id]
				current.Uplink += count.Uplink - before.Uplink
				current.Downlink += count.Downlink - before.Downlink
				p.Totals[id] = current
			}
		}
		result[kind] = p
	}
	return result
}

// ValidPeriods兼容无周期字段的旧账本；拒绝超过累计的周期数据。
func ValidPeriods(ledger Ledger) bool {
	if ledger.Inbounds != nil {
		in := ledger.Inbounds
		if in.Inbounds != nil || in.Totals == nil || in.Last == nil || !ValidPeriods(*in) || (in.Active && (in.Epoch == "" || in.SampledAt == nil)) {
			return false
		}
	}
	if len(ledger.Periods) > len(periodKinds) {
		return false
	}
	for kind, p := range ledger.Periods {
		if kind != "day" && kind != "week" && kind != "month" {
			return false
		}
		if p.Start.IsZero() || !p.End.After(p.Start) || p.Since.Before(p.Start) || !p.Since.Before(p.End) || p.Totals == nil {
			return false
		}
		for id, c := range p.Totals {
			total, exists := ledger.Totals[id]
			if !exists || c.Uplink > total.Uplink || c.Downlink > total.Downlink {
				return false
			}
		}
	}
	return true
}

// CurrentPeriods在停核期间也展示新周期零值，无需写库。
func CurrentPeriods(ledger Ledger, now time.Time) map[string]Period {
	if len(ledger.Periods) == 0 {
		return nil
	}
	result := make(map[string]Period, 3)
	for _, kind := range periodKinds {
		start, end := periodBounds(kind, now)
		p, exists := ledger.Periods[kind]
		if !exists || !start.Equal(p.Start) || !end.Equal(p.End) {
			p = Period{Start: start, End: end, Since: start, Partial: true, Totals: map[string]Count{}}
		}
		result[kind] = p
	}
	return result
}
