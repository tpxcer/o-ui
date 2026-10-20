package localapi

import (
	"context"
	"errors"
	"testing"
)

func TestPanelVersionComparesReleasesOnly(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		newer           bool
	}{{"v0.2.0", "v0.1.0", true}, {"v0.1.0", "v0.1.0", false}, {"v0.1.0", "v0.2.0", false}, {"v1.0.0", "v0.9.9", true}, {"v0.1.0", "v0.1.0-rc.1", true}, {"v0.2.0-rc.1", "v0.1.0", false}, {"v0.1.0", "dev", true}, {"", "v0.1.0", false},
		{"2026-10-21", "v0.3.1", true}, {"v9.9.9", "2026-10-21", false}, {"2026-10-21-v1", "2026-10-21", true}, {"2026-10-21", "2026-10-21-v1", false},
		{"2026-10-21-v10", "2026-10-21-v9", true}, {"2026-10-22", "2026-10-21-v3", true}, {"2026-10-21", "2026-10-21", false}, {"2026-02-30", "v0.1.0", false},
		{"2026-10-21-v0", "v0.1.0", false}, {"2026-1-21", "v0.1.0", false}, {"2026-10-21", "dev", true}} {
		if newerVersion(c.latest, c.current) != c.newer {
			t.Fatal(c)
		}
	}
}

func TestPanelVersionChecksOnlyOnRequestAndCaches(t *testing.T) {
	calls := 0
	p := &panelUpdates{current: "v0.1.0", fetch: func(context.Context) (string, string, error) {
		calls++
		return "v0.2.0", "https://github.com/tpxcer/o-ui/releases/tag/v0.2.0", nil
	}}
	if v := p.get(context.Background(), false); calls != 0 || v.UpdateAvailable || v.Current != "v0.1.0" {
		t.Fatal("loading the page must not contact GitHub", v)
	}
	if v := p.get(context.Background(), true); calls != 1 || !v.UpdateAvailable || v.Latest != "v0.2.0" {
		t.Fatal("check must report the newer release", v)
	}
	if p.get(context.Background(), true); calls != 1 {
		t.Fatal("repeated checks must be cached")
	}
	failing := &panelUpdates{current: "v0.1.0", fetch: func(context.Context) (string, string, error) { return "", "", errors.New("offline") }}
	if v := failing.get(context.Background(), true); v.Issue == "" || v.UpdateAvailable {
		t.Fatal("failure must be reported, not treated as up to date", v)
	}
}
