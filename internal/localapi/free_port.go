package localapi

import (
	"encoding/json"
	"independent-panel/internal/config"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
)

// 与OUI一键创建一致：在50000–65000高位端口中随机挑选。
const quickPortMin, quickPortMax = 50000, 65000

// 与OUI一键创建一致：HY2端口跳跃在40000–59999中随机挑一段200个端口。
const hopRangeMin, hopRangeMax, hopRangeSize = 40000, 59999, 200

// freeHopRange挑一段不含已用端口、且本机这些UDP端口当前都没被占用的范围（转发会抢走这些端口的外来流量）。
func freeHopRange(used map[int]bool, pick func() int) (string, bool) {
	for attempt := 0; attempt < 80; attempt++ {
		start := pick()
		free := true
		for port := start; port < start+hopRangeSize && free; port++ {
			if used[port] {
				free = false
			} else if conn, err := net.ListenPacket("udp", ":"+strconv.Itoa(port)); err != nil {
				free = false
			} else {
				conn.Close()
			}
		}
		if free {
			return strconv.Itoa(start) + "-" + strconv.Itoa(start+hopRangeSize-1), true
		}
	}
	return "", false
}

// freePort避开已保存入站的端口，并实际尝试监听确认当前未被占用。
func freePort(used map[int]bool, udp bool, pick func() int) (int, bool) {
	for attempt := 0; attempt < 80; attempt++ {
		port := pick()
		if used[port] {
			continue
		}
		address := ":" + strconv.Itoa(port)
		if udp {
			if conn, err := net.ListenPacket("udp", address); err == nil {
				conn.Close()
				return port, true
			}
		} else if listener, err := net.Listen("tcp", address); err == nil {
			listener.Close()
			return port, true
		}
	}
	return 0, false
}

func registerFreePort(mux *http.ServeMux, state *store.Store) {
	mux.HandleFunc("GET /api/system/free-port", func(w http.ResponseWriter, r *http.Request) {
		used := map[int]bool{}
		if doc, err := state.ReadDocument(r.Context(), "workspace"); err == nil {
			var draft workspaceDraft
			if json.Unmarshal(doc.Data, &draft) == nil {
				for _, in := range draft.Inbounds {
					used[in.Port] = true
					if start, end, ok := config.ParsePortRange(in.PortHopping); ok {
						for port := start; port <= end; port++ {
							used[port] = true
						}
					}
				}
			}
		}
		port, ok := freePort(used, r.URL.Query().Get("transport") == "udp", func() int { return quickPortMin + rand.IntN(quickPortMax-quickPortMin+1) })
		if !ok {
			respond(w, 503, map[string]string{"error": i18n.T("暂时找不到空闲端口，请稍后重试或手动指定", "No free port found right now; retry or choose one manually")})
			return
		}
		if r.URL.Query().Get("hop") != "1" {
			respond(w, 200, map[string]int{"port": port})
			return
		}
		used[port] = true
		hop, ok := freeHopRange(used, func() int { return hopRangeMin + rand.IntN(hopRangeMax-hopRangeSize+2-hopRangeMin) })
		if !ok {
			respond(w, 503, map[string]string{"error": i18n.T("暂时找不到空闲的端口跳跃范围，请稍后重试", "No free port hopping range found right now; retry later")})
			return
		}
		respond(w, 200, map[string]any{"port": port, "portHopping": hop})
	})
}
