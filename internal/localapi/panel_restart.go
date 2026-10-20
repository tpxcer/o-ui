package localapi

import (
	"net/http"
	"time"
)

var restartRequests = make(chan struct{}, 1)

// RestartRequests在管理员点击“重启面板”后收到通知；由主程序正常关闭后重新执行自身。
func RestartRequests() <-chan struct{} { return restartRequests }

func registerPanelRestart(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/panel/restart", func(w http.ResponseWriter, r *http.Request) {
		var request struct{}
		if !readJSON(w, r, &request, 64) {
			return
		}
		respond(w, 202, map[string]bool{"restarting": true})
		// 先让响应发出，再通知主程序关闭并重启。
		go func() {
			time.Sleep(300 * time.Millisecond)
			select {
			case restartRequests <- struct{}{}:
			default:
			}
		}()
	})
}
