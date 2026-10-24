package localapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
)

// 显示顺序独立保存，不改变 workspace 修订，也不触发核心应用或防火墙操作。
func registerListOrder(mux *http.ServeMux, state *store.Store) {
	for _, kind := range []string{"inbounds", "clients"} {
		name := "list-order-" + kind
		mux.HandleFunc("GET /api/panel/list-order/"+kind, func(w http.ResponseWriter, r *http.Request) {
			doc, err := state.ReadDocument(r.Context(), name)
			if err != nil {
				respond(w, 500, map[string]string{"error": i18n.T("读取列表顺序失败", "Failed to read list order")})
				return
			}
			if string(doc.Data) == "null" {
				doc.Data = json.RawMessage(`[]`)
			}
			respond(w, 200, doc)
		})
		mux.HandleFunc("PUT /api/panel/list-order/"+kind, func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Revision int64    `json:"revision"`
				Data     []string `json:"data"`
			}
			if !readJSON(w, r, &request, 1024*1024) {
				return
			}
			valid := request.Revision >= 0 && request.Data != nil && len(request.Data) <= 5000
			seen := map[string]bool{}
			for _, id := range request.Data {
				if strings.TrimSpace(id) == "" || len(id) > 128 || seen[id] {
					valid = false
				}
				seen[id] = true
			}
			if !valid {
				respond(w, 400, map[string]string{"error": i18n.T("列表顺序无效", "Invalid list order")})
				return
			}
			data, _ := json.Marshal(request.Data)
			doc, err := state.SaveDocument(r.Context(), name, request.Revision, data)
			if err != nil {
				code, message := 500, i18n.T("保存列表顺序失败", "Failed to save list order")
				if errors.Is(err, store.ErrConflict) {
					code, message = 409, err.Error()
				}
				respond(w, code, map[string]string{"error": message})
				return
			}
			respond(w, 200, doc)
		})
	}
}
