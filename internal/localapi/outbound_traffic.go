package localapi

import (
	"context"
	"encoding/json"
	"independent-panel/internal/i18n"
	"independent-panel/internal/traffic"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 出站流量（参照OUI出站表的“流量”列）：读取当前核心本次运行以来各出站的上下行计数，不持久化。
func parseOutboundStats(data []byte) map[string]traffic.Count {
	var response struct {
		Stat []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	result := map[string]traffic.Count{}
	if json.Unmarshal(data, &response) != nil {
		return result
	}
	for _, stat := range response.Stat {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 || parts[0] != "outbound" || parts[1] == "" || parts[2] != "traffic" {
			continue
		}
		n, err := strconv.ParseUint(strings.Trim(string(stat.Value), `"`), 10, 63)
		if err != nil && len(stat.Value) != 0 {
			continue
		}
		c := result[parts[1]]
		switch parts[3] {
		case "uplink":
			c.Uplink = n
		case "downlink":
			c.Downlink = n
		default:
			continue
		}
		result[parts[1]] = c
	}
	return result
}

func registerOutboundTraffic(mux *http.ServeMux, rt *Runtime) {
	mux.HandleFunc("GET /api/runtime/outbound-traffic", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		c, running := rt.engine.Current()
		if !running || !strings.HasPrefix(c.StatsAddress, "unix:///") {
			respond(w, 200, map[string]any{"running": false, "outbounds": map[string]traffic.Count{}})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, c.Binary, "api", "statsquery", "--server="+c.StatsAddress, "-timeout=2", "-pattern=outbound>>>")
		command.Dir = filepath.Dir(c.Binary)
		command.Env = []string{"PATH=/usr/bin:/bin"}
		var output boundedOutput
		command.Stdout = &output
		if command.Run() != nil {
			respond(w, 200, map[string]any{"running": true, "outbounds": map[string]traffic.Count{}})
			return
		}
		respond(w, 200, map[string]any{"running": true, "outbounds": parseOutboundStats(output.Bytes())})
	})
}
