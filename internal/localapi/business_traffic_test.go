package localapi

import (
	"independent-panel/internal/traffic"
	"testing"
	"time"
)

func TestBusinessTrafficCountsOnlyClientTraffic(t *testing.T) {
	var b businessTraffic
	if n, history := b.network(); n != nil || len(history) != 0 {
		t.Fatal("no sample must not report traffic")
	}
	now := time.Now()
	totals := map[string]traffic.Count{"a": {Uplink: 1000, Downlink: 5000}, "b": {Uplink: 10, Downlink: 20}}
	b.observe("one", map[string]traffic.Count{"a": {Uplink: 100, Downlink: 200}}, totals, now)
	n, history := b.network()
	if n == nil || n.Upload != nil || len(history) != 1 || n.Sent != "1010" || n.Received != "5020" {
		t.Fatal("first sample of an instance must only set totals", n, history)
	}
	b.observe("one", map[string]traffic.Count{"a": {Uplink: 300, Downlink: 1200}, "b": {Uplink: 100}}, totals, now.Add(2*time.Second))
	n, _ = b.network()
	if n.Upload == nil || *n.Upload != 150 || *n.Download != 500 {
		t.Fatal("rate must be the summed client delta per second", n.Upload, n.Download)
	}
	b.observe("two", map[string]traffic.Count{"a": {Uplink: 1}}, totals, now.Add(4*time.Second))
	if n, _ = b.network(); n.Upload != nil {
		t.Fatal("a new core instance must not reuse the old baseline")
	}
	b.observe("", nil, totals, now.Add(6*time.Second))
	if n, _ = b.network(); n.Upload == nil || *n.Upload != 0 || *n.Download != 0 {
		t.Fatal("stopped core means zero business speed")
	}
	for i := 0; i < 100; i++ {
		b.observe("", nil, totals, now.Add(time.Duration(8+i)*time.Second))
	}
	if _, history = b.network(); len(history) != 90 {
		t.Fatal("history must keep about three minutes", len(history))
	}
}
