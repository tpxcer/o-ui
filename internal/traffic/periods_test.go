package traffic

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestPeriodsBoundariesAndDST(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, at, start, end string }{
		{"day", "2026-03-08T12:00:00-04:00", "2026-03-08T00:00:00-05:00", "2026-03-09T00:00:00-04:00"},
		{"day", "2026-11-01T12:00:00-05:00", "2026-11-01T00:00:00-04:00", "2026-11-02T00:00:00-05:00"},
		{"week", "2027-01-01T12:00:00-05:00", "2026-12-28T00:00:00-05:00", "2027-01-04T00:00:00-05:00"},
		{"month", "2028-02-29T12:00:00-05:00", "2028-02-01T00:00:00-05:00", "2028-03-01T00:00:00-05:00"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		start, end := periodBounds(tc.kind, at.In(zone))
		if start.Format(time.RFC3339) != tc.start || end.Format(time.RFC3339) != tc.end {
			t.Fatalf("%s: %s %s", tc.kind, start, end)
		}
	}
}

func TestPeriodsMigrationResetRestartAndReadOnlyView(t *testing.T) {
	zone := time.FixedZone("server", 8*3600)
	at := time.Date(2026, 10, 31, 23, 59, 58, 0, zone)
	l := Empty()
	l.Totals["one"] = Count{Uplink: 1000}
	step := func(epoch string, up uint64, now time.Time) {
		t.Helper()
		before, _ := json.Marshal(l)
		next, err := Merge(l, epoch, map[string]Count{"one": {Uplink: up}}, now)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(l)
		if string(before) != string(after) {
			t.Fatal("input mutated")
		}
		l = next
		if !ValidPeriods(l) {
			t.Fatal("invalid periods")
		}
	}
	step("run1", 100, at)
	if l.Periods["day"].Totals["one"].Total() != 0 || l.Totals["one"].Total() != 1100 || !l.Periods["day"].Partial {
		t.Fatal("migration counted history")
	}
	step("run1", 110, at.Add(time.Second))
	step("run1", 130, at.Add(2*time.Second)) // 月1日零点，边界增量按采样入账
	if l.Periods["day"].Totals["one"].Total() != 20 || l.Periods["month"].Totals["one"].Total() != 20 || l.Periods["week"].Totals["one"].Total() != 30 {
		t.Fatal("wrong reset scope")
	}
	data, _ := json.Marshal(l)
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	step("run2", 5, at.Add(4*time.Second))
	if l.Totals["one"].Total() != 1135 || l.Periods["month"].Totals["one"].Total() != 25 {
		t.Fatal("restart duplicated/reset usage")
	}
	before, _ := json.Marshal(l)
	view := CurrentPeriods(l, at.AddDate(0, 0, 3))
	if view["day"].Totals["one"].Total() != 0 || view["week"].Totals["one"].Total() != 0 || view["month"].Totals["one"].Total() != 25 {
		t.Fatal("stopped view kept expired bucket")
	}
	after, _ := json.Marshal(l)
	if string(before) != string(after) {
		t.Fatal("read mutated ledger")
	}
	// 回拨后不重复重置；恢复时间前的增量不伪装为新周期用量。
	step("run2", 8, at)
	if !l.Periods["day"].Partial || l.Periods["day"].Totals["one"].Total() != 25 {
		t.Fatal("clock rollback reset")
	}
	step("run2", 10, at.Add(6*time.Second))
	if l.Periods["day"].Totals["one"].Total() != 27 || l.Totals["one"].Total() != 1140 {
		t.Fatal("rollback double count")
	}
}

func TestPeriodValidationAndGap(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	l, _ := Merge(Empty(), "one", map[string]Count{"a": {Uplink: 10}}, at)
	next, err := Merge(l, "one", map[string]Count{"a": {Uplink: 20}}, at.Add(24*time.Hour))
	if err != nil || !next.Periods["day"].Partial || next.Periods["day"].Totals["a"].Total() != 0 || next.Totals["a"].Total() != 20 {
		t.Fatal("gap not labelled", err)
	}
	if !reflect.DeepEqual(CurrentPeriods(Empty(), at), map[string]Period(nil)) {
		t.Fatal("missing data became zero")
	}
	for _, mutate := range []func(*Ledger){
		func(l *Ledger) { p := l.Periods["day"]; p.Totals["a"] = Count{Uplink: 999}; l.Periods["day"] = p },
		func(l *Ledger) { p := l.Periods["day"]; p.End = p.Start; l.Periods["day"] = p },
		func(l *Ledger) { l.Periods["year"] = l.Periods["day"] },
	} {
		data, _ := json.Marshal(next)
		var bad Ledger
		_ = json.Unmarshal(data, &bad)
		mutate(&bad)
		if ValidPeriods(bad) {
			t.Fatal("invalid period accepted")
		}
	}
}

func TestEveryPeriodResetsOnlyItsBucketAtBoundary(t *testing.T) {
	zone := time.FixedZone("server", -7*3600)
	for _, kind := range periodKinds {
		t.Run(kind, func(t *testing.T) {
			_, boundary := periodBounds(kind, time.Date(2026, 10, 25, 12, 0, 0, 0, zone))
			l, err := Merge(Empty(), "fixture", map[string]Count{"a": {Uplink: 100}}, boundary.Add(-2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			l, err = Merge(l, "fixture", map[string]Count{"a": {Uplink: 110}}, boundary.Add(-time.Second))
			if err != nil {
				t.Fatal(err)
			}
			l, err = Merge(l, "fixture", map[string]Count{"a": {Uplink: 112}}, boundary)
			if err != nil || l.Periods[kind].Totals["a"].Total() != 2 || l.Totals["a"].Total() != 112 {
				t.Fatal("boundary reset lifetime or kept old bucket", err)
			}
		})
	}
}
