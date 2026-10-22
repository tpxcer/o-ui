package traffic

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestLedgerCheckpointsAndEpoch(t *testing.T) {
	now := time.Now()
	old := Empty()
	first, err := Merge(old, "process1", map[string]Count{"one": {100, 200}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Totals) != 0 {
		t.Fatal("input mutated")
	}
	second, err := Merge(first, "process1", map[string]Count{"one": {100, 200}}, now)
	if err != nil || !reflect.DeepEqual(second.Totals, first.Totals) {
		t.Fatal("duplicate sample counted")
	}
	third, err := Merge(second, "process1", map[string]Count{"one": {110, 230}}, now)
	if err != nil || third.Totals["one"].Total() != 340 {
		t.Fatal("increment wrong")
	}
	data, _ := json.Marshal(third)
	var persisted Ledger
	if json.Unmarshal(data, &persisted) != nil {
		t.Fatal("persistence format")
	}
	afterRestart, err := Merge(persisted, "process2", map[string]Count{"one": {10, 20}}, now)
	if err != nil || afterRestart.Totals["one"].Total() != 370 {
		t.Fatal("restart lost/duplicated totals")
	}
	if _, err = Merge(third, "process1", map[string]Count{"one": {0, 0}}, now); err == nil {
		t.Fatal("silent reset accepted")
	}
	missing, err := Merge(third, "process1", map[string]Count{}, now)
	if err != nil || missing.Last["one"] != third.Last["one"] {
		t.Fatal("missing counters destroyed checkpoint")
	}
	third.Totals["one"] = Count{math.MaxUint64, 0}
	if _, err = Merge(third, "process1", map[string]Count{"one": {111, 230}}, now); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestParseOfficialStats(t *testing.T) {
	data := []byte(`{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink","value":"9007199254740993"},{"name":"user>>>client-one>>>traffic>>>downlink","value":"7"},{"name":"inbound>>>api>>>traffic>>>uplink","value":"9"}]}`)
	counts, err := Parse(data)
	if err != nil || len(counts) != 1 || counts["one"].Uplink != 9007199254740993 {
		t.Fatal("int64 precision or filtering")
	}
	for _, value := range []string{`no-json`, `null`, `[]`, `{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink","value":"-1"}]}`, `{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink","value":"1"}]}`, `{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink","value":"1"},{"name":"user>>>client-one>>>traffic>>>uplink","value":"1"}]}`} {
		if _, err := Parse([]byte(value)); err == nil {
			t.Fatal("invalid counters accepted")
		}
	}
	if counts, err := Parse([]byte(`{}`)); err != nil || len(counts) != 0 {
		t.Fatal("idle counters rejected")
	}
}

func TestParseOfficialOmittedZero(t *testing.T) {
	for _, check := range []struct {
		up, down string
		want     Count
	}{
		{"", `,"value":"37"`, Count{0, 37}},
		{`,"value":"37"`, "", Count{37, 0}},
		{"", "", Count{}},
	} {
		data := []byte(`{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink"` + check.up + `},{"name":"user>>>client-one>>>traffic>>>downlink"` + check.down + `}]}`)
		counts, err := Parse(data)
		if err != nil || len(counts) != 1 || counts["one"] != check.want {
			t.Fatal("protobuf default zero rejected", err)
		}
	}
	for _, value := range []string{`null`, `""`, `true`, `-1`, `1.5`, `"9223372036854775808"`, `{}`} {
		data := []byte(`{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink","value":` + value + `},{"name":"user>>>client-one>>>traffic>>>downlink"}]}`)
		if _, err := Parse(data); err == nil {
			t.Fatal("explicit invalid value accepted")
		}
	}
	for _, data := range []string{
		`{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink"}]}`,
		`{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink"},{"name":"user>>>client-one>>>traffic>>>uplink"}]}`,
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatal("missing or duplicate direction accepted")
		}
	}
	counts, err := Parse([]byte(`{"stat":[{"name":"user>>>client-one>>>traffic>>>uplink"},{"name":"user>>>client-one>>>traffic>>>downlink"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	old := Empty()
	old.Epoch, old.Last["one"] = "same-process", Count{1, 0}
	if _, err := Merge(old, "same-process", counts, time.Now()); err == nil {
		t.Fatal("omitted zero bypassed same-process counter regression")
	}
}

// 最近上线只在客户端确有新流量时更新；无变化的采样保留上次时刻。
func TestMergeTracksLastOnline(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	first, err := Merge(Empty(), "p1", map[string]Count{"a": {0, 0}, "b": {10, 0}}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, seen := first.LastOnline["a"]; seen || !first.LastOnline["b"].Equal(t0) {
		t.Fatal("only clients with traffic are marked online", first.LastOnline)
	}
	second, err := Merge(first, "p1", map[string]Count{"a": {0, 5}, "b": {10, 0}}, t0.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !second.LastOnline["a"].Equal(t0.Add(2*time.Second)) || !second.LastOnline["b"].Equal(t0) {
		t.Fatal("idle sample must keep the previous time", second.LastOnline)
	}
}
