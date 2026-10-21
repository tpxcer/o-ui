package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	calls, clock := 0, time.Unix(1_800_000_000, 0)
	latest := "v0.2.0"
	p := &panelUpdates{current: "v0.1.0", now: func() time.Time { return clock }, fetch: func(context.Context) (string, string, error) {
		calls++
		return latest, "https://github.com/tpxcer/o-ui/releases/tag/" + latest, nil
	}}
	if v := p.get(context.Background(), false); calls != 0 || v.UpdateAvailable || v.Current != "v0.1.0" {
		t.Fatal("loading the page must not contact GitHub", v)
	}
	if v := p.get(context.Background(), true); calls != 1 || !v.UpdateAvailable || v.Latest != "v0.2.0" {
		t.Fatal("check must report the newer release", v)
	}
	clock = clock.Add(59 * time.Second)
	if p.get(context.Background(), true); calls != 1 {
		t.Fatal("repeated checks within a minute must be cached")
	}
	// 刚发布的新版本：距上次检查满1分钟后手动点击即可看到。
	latest, clock = "2026-10-21-v1", clock.Add(time.Second)
	if v := p.get(context.Background(), true); calls != 2 || v.Latest != "2026-10-21-v1" || !v.UpdateAvailable {
		t.Fatal("a check after a minute must fetch again", v)
	}
	failing := &panelUpdates{current: "v0.1.0", fetch: func(context.Context) (string, string, error) { return "", "", errors.New("offline") }}
	if v := failing.get(context.Background(), true); v.Issue == "" || v.UpdateAvailable {
		t.Fatal("failure must be reported, not treated as up to date", v)
	}
}

func TestPanelWebUpgrade(t *testing.T) {
	ready, started := true, 0
	latest := "2026-10-21-v1"
	status := filepath.Join(t.TempDir(), "status.json")
	previousReady, previousRun, previousStatus := upgradeReady, runUpgrade, upgradeStatusFile
	t.Cleanup(func() { upgradeReady, runUpgrade, upgradeStatusFile = previousReady, previousRun, previousStatus })
	upgradeReady = func() bool { return ready }
	runUpgrade = func(context.Context) (string, error) { started++; return "已开始升级", nil }
	upgradeStatusFile = status
	mux := http.NewServeMux()
	registerPanelUpdates(mux, &panelUpdates{current: "2026-10-21", fetch: func(context.Context) (string, string, error) {
		return latest, "https://github.com/tpxcer/o-ui/releases/tag/" + latest, nil
	}})
	call := func(method, path, body string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var result map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	if v := call("GET", "/api/panel/version", "", 200); v["canUpgrade"] != true || v["update"] != nil {
		t.Fatal("version view", v)
	}
	call("POST", "/api/panel/update", `{}`, 400)
	ready = false
	call("POST", "/api/panel/update", `{"confirm":true}`, 409)
	ready = true
	if result := call("POST", "/api/panel/update", `{"confirm":true}`, 202); result["target"] != "2026-10-21-v1" || started != 1 {
		t.Fatal("update should start towards the latest release", result, started)
	}
	latest = "2026-10-21"
	call("POST", "/api/panel/update", `{"confirm":true}`, 409)
	if started != 1 {
		t.Fatal("must not start when already on the latest version")
	}
	if err := os.WriteFile(status, []byte(`{"state":"failed","from":"2026-10-21","to":"2026-10-21","code":3,"at":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	update := call("GET", "/api/panel/version", "", 200)["update"].(map[string]any)
	if update["state"] != "failed" || update["code"] != float64(3) {
		t.Fatal("status file", update)
	}
	_ = os.WriteFile(status, []byte(`{"state":"weird"}`), 0600)
	if call("GET", "/api/panel/version", "", 200)["update"] != nil {
		t.Fatal("invalid status must be ignored")
	}
}
