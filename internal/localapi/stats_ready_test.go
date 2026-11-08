package localapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

// 开机冷启动时统计接口晚于核心就绪：核心仍运行就重试，接口始终不可用才停核心。
func TestApplyWaitsForStatsAPI(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readyAt  int
		timeout  time.Duration
		wantRun  bool
		minTries int
	}{
		{"ready after retries", 3, 20 * time.Second, true, 3},
		{"never ready", 1 << 30, 1500 * time.Millisecond, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := statsReadyTimeout
			statsReadyTimeout = tc.timeout
			t.Cleanup(func() { statsReadyTimeout = previous })
			ctx := context.Background()
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			rt, err := NewRuntime(nil, s, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			dir, err := os.MkdirTemp(rt.root, "candidate-")
			if err != nil {
				t.Fatal(err)
			}
			// 假核心：统计查询前readyAt-1次失败，之后返回空统计；预检直接通过，正式运行常驻。
			count, binary := filepath.Join(t.TempDir(), "count"), filepath.Join(t.TempDir(), "xray")
			script := "#!/bin/sh\nif [ \"$1\" = api ]; then\n  n=$(($(cat '" + count + "' 2>/dev/null || echo 0)+1)); echo $n > '" + count + "'\n" +
				"  [ $n -ge " + strconv.Itoa(tc.readyAt) + " ] && echo '{\"stat\":[]}' && exit 0\n  exit 1\nfi\n[ \"$2\" = -test ] && exit 0\nexec sleep 600\n"
			if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			c := engine.Candidate{Binary: binary, Version: "26.7.28", WorkspaceRevision: 1, SettingsRevision: 2, Inbounds: 1, File: filepath.Join(dir, "config.json"), PolicyFile: filepath.Join(dir, "policy.json"), StatsAddress: "unix:///" + filepath.Join(dir, "api.sock")}
			data, _ := json.Marshal(map[string]any{"inbounds": []config.CompiledInbound{{Tag: "in-one", Listen: "127.0.0.1", Settings: config.ProtocolSettings{Clients: []config.User{{Email: "client-a"}}}}}})
			if err = writePrivate(c.File, data); err != nil {
				t.Fatal(err)
			}
			data, _ = json.Marshal(runtimePolicy{Clients: map[string]clientLimit{"a": {}}})
			if err = writePrivate(c.PolicyFile, data); err != nil {
				t.Fatal(err)
			}
			rt.mu.Lock()
			err = rt.apply(ctx, c)
			rt.mu.Unlock()
			raw, _ := os.ReadFile(count)
			tries, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			if running := rt.engine.Status().Running; running != tc.wantRun || (err == nil) != tc.wantRun || tries < tc.minTries {
				t.Fatal("unexpected start result", running, err, tries)
			}
		})
	}
}
