package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
)

// 面板设置“常规”页：访问（监听/域名/端口/路径）、面板证书与会话时长一起保存，避免改域名和换证书互相卡住。
// 访问与证书仍在下次启动生效；会话时长对之后的登录立即生效。
type generalStatus struct {
	Access struct {
		Revision int64             `json:"revision"`
		Data     *PanelAccessDraft `json:"data"`
		Active   PanelAccessDraft  `json:"active"`
		Pending  bool              `json:"pending"`
	} `json:"access"`
	TLS struct {
		Revision    int64          `json:"revision"`
		Data        *panelTLSDraft `json:"data"`
		Active      bool           `json:"active"`
		ServerNames []string       `json:"serverNames"`
		Pending     bool           `json:"pending"`
	} `json:"tls"`
	Session struct {
		Revision int64 `json:"revision"`
		Minutes  int   `json:"minutes"`
	} `json:"session"`
}

type generalRequest struct {
	AccessRevision  int64            `json:"accessRevision"`
	TLSRevision     int64            `json:"tlsRevision"`
	SessionRevision int64            `json:"sessionRevision"`
	Access          PanelAccessDraft `json:"access"`
	TLS             panelTLSDraft    `json:"tls"`
	SessionMinutes  int              `json:"sessionMinutes"`
	Confirmation    string           `json:"confirmation"`
}

var generalDocuments = []string{"panel-access", "panel-tls", store.SessionDocument}

func readGeneral(ctx context.Context, rt *Runtime, active *PanelAccess) (generalStatus, map[string]store.Document, error) {
	var status generalStatus
	docs, err := rt.state.ReadDocuments(ctx, generalDocuments)
	if err != nil {
		return status, nil, err
	}
	status.Access.Revision, status.Access.Active = docs["panel-access"].Revision, active.Data
	status.Access.Pending = docs["panel-access"].Revision != active.Revision
	if raw := docs["panel-access"].Data; string(raw) != "null" {
		var data PanelAccessDraft
		if decodeStrict(raw, &data) == nil {
			status.Access.Data = &data
		}
	}
	status.TLS.Revision, status.TLS.ServerNames = docs["panel-tls"].Revision, []string{}
	if rt.panelTLS != nil {
		status.TLS.Active, status.TLS.ServerNames = true, append(status.TLS.ServerNames, rt.panelTLS.names...)
		status.TLS.Pending = docs["panel-tls"].Revision != rt.panelTLS.revision
	} else {
		status.TLS.Pending = docs["panel-tls"].Revision != 0 && string(docs["panel-tls"].Data) != "null"
	}
	if raw := docs["panel-tls"].Data; string(raw) != "null" {
		var data panelTLSDraft
		if json.Unmarshal(raw, &data) == nil {
			status.TLS.Data = &data
		}
	}
	status.Session.Revision, status.Session.Minutes = docs[store.SessionDocument].Revision, store.DefaultSessionMinutes
	var session struct {
		Minutes int `json:"minutes"`
	}
	if json.Unmarshal(docs[store.SessionDocument].Data, &session) == nil && session.Minutes >= store.MinSessionMinutes && session.Minutes <= store.MaxSessionMinutes {
		status.Session.Minutes = session.Minutes
	}
	return status, docs, nil
}

func registerPanelGeneral(mux *http.ServeMux, rt *Runtime, active *PanelAccess) {
	mux.HandleFunc("GET /api/panel/general", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("面板设置尚未启用", "Panel settings are not available")})
			return
		}
		status, _, err := readGeneral(r.Context(), rt, active)
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取面板设置失败", "Failed to read panel settings")})
			return
		}
		respond(w, 200, status)
	})
	mux.HandleFunc("PUT /api/panel/general", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("面板设置尚未启用", "Panel settings are not available")})
			return
		}
		var request generalRequest
		if !readJSON(w, r, &request, 16384) {
			return
		}
		if request.SessionMinutes < store.MinSessionMinutes || request.SessionMinutes > store.MaxSessionMinutes {
			respond(w, 400, map[string]string{"error": i18n.Errorf("会话时长须为%d至%d分钟", "Session duration must be %d–%d minutes", store.MinSessionMinutes, store.MaxSessionMinutes).Error()})
			return
		}
		rt.panelMu.Lock()
		defer rt.panelMu.Unlock()
		var certificate *PanelTLS
		tlsData := json.RawMessage("null")
		if request.TLS.CertificateFile != "" || request.TLS.KeyFile != "" {
			pair, err := loadPanelPair(request.TLS)
			if err != nil {
				respond(w, 400, map[string]string{"error": err.Error()})
				return
			}
			certificate = pair
			tlsData, _ = json.Marshal(request.TLS)
		}
		if err := validatePanelAccess(request.Access, certificate); err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if request.Access.Public && request.Confirmation != httpsConfirmation {
			respond(w, 409, map[string]string{"error": i18n.T("请明确确认非本机管理/订阅将增加暴露面；持有订阅链接者可取得代理凭据", "Confirm that remote management and subscriptions increase exposure; anyone with a subscription link can obtain proxy credentials")})
			return
		}
		status, docs, err := readGeneral(r.Context(), rt, active)
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取面板设置失败", "Failed to read panel settings")})
			return
		}
		accessData, _ := json.Marshal(request.Access)
		sessionData, _ := json.Marshal(map[string]int{"minutes": request.SessionMinutes})
		currentAccess := docs["panel-access"].Data
		if string(currentAccess) == "null" {
			currentAccess, _ = json.Marshal(active.Data)
		}
		currentSession, _ := json.Marshal(map[string]int{"minutes": status.Session.Minutes})
		expected, next := map[string]store.Document{}, map[string]json.RawMessage{}
		for _, item := range []struct {
			name     string
			revision int64
			current  []byte
			data     json.RawMessage
		}{
			{"panel-access", request.AccessRevision, currentAccess, accessData},
			{"panel-tls", request.TLSRevision, docs["panel-tls"].Data, tlsData},
			{store.SessionDocument, request.SessionRevision, currentSession, sessionData},
		} {
			if item.revision != docs[item.name].Revision {
				respond(w, 409, map[string]string{"error": store.ErrConflict.Error()})
				return
			}
			if !bytes.Equal(item.current, item.data) {
				expected[item.name], next[item.name] = store.Document{Revision: item.revision}, item.data
			}
		}
		if len(next) > 0 {
			if err := rt.state.ReplaceDocuments(r.Context(), expected, next); err != nil {
				code, message := 500, i18n.T("保存面板设置失败，原设置未改变；请重新读取确认结果", "Failed to save panel settings; previous settings unchanged, reload to confirm")
				if errors.Is(err, store.ErrConflict) {
					code, message = 409, store.ErrConflict.Error()
				}
				respond(w, code, map[string]string{"error": message})
				return
			}
		}
		status, _, err = readGeneral(r.Context(), rt, active)
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取面板设置失败", "Failed to read panel settings")})
			return
		}
		respond(w, 200, status)
	})
}
