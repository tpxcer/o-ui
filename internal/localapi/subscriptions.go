package localapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"independent-panel/internal/store"
)

const subscriptionsDocument = "subscriptions"

var subscriptionPath = regexp.MustCompile(`^/sub/[A-Za-z0-9_-]{43}$`)

type subscriptionToken struct {
	Hash       string `json:"hash"`
	Generation uint64 `json:"generation"`
}
type subscriptionState struct {
	Version    int                          `json:"version"`
	Generation uint64                       `json:"generation"`
	Clients    map[string]subscriptionToken `json:"clients"`
}
type subscriptionRequest struct {
	ClientID          string `json:"clientId"`
	WorkspaceRevision int64  `json:"workspaceRevision"`
	Revision          int64  `json:"revision"`
	Confirm           bool   `json:"confirm"`
}
type subscriptionResult struct {
	Revision int64  `json:"revision"`
	Allowed  bool   `json:"allowed"`
	Active   bool   `json:"active"`
	Path     string `json:"path,omitempty"`
}

func readSubscriptions(doc store.Document) (subscriptionState, error) {
	if doc.Revision == 0 && string(doc.Data) == "null" {
		return subscriptionState{Version: 1, Generation: 1, Clients: map[string]subscriptionToken{}}, nil
	}
	var state subscriptionState
	if len(doc.Data) > 2<<20 || decodeStrict(doc.Data, &state) != nil || state.Version != 1 || state.Generation == 0 || state.Clients == nil || len(state.Clients) > 5000 {
		return state, i18n.New("订阅记录损坏，拒绝自动重置", "Subscription records are corrupted; refusing to reset automatically")
	}
	seen := map[string]bool{}
	for id, item := range state.Clients {
		decoded, err := hex.DecodeString(item.Hash)
		if !draftID.MatchString(id) || err != nil || len(decoded) != sha256.Size || item.Hash != strings.ToLower(item.Hash) || item.Generation > state.Generation || seen[item.Hash] {
			return state, i18n.New("订阅记录无效", "Invalid subscription records")
		}
		seen[item.Hash] = true
	}
	return state, nil
}

func subscriptionClient(doc store.Document, id string) (clientDraft, error) {
	var draft workspaceDraft
	if json.Unmarshal(doc.Data, &draft) != nil {
		return clientDraft{}, i18n.New("无法读取客户端草稿", "Cannot read client draft")
	}
	for _, client := range draft.Clients {
		if client.ID == id {
			return client, nil
		}
	}
	return clientDraft{}, i18n.New("客户端不存在", "Client does not exist")
}

// 与草稿同一事务撤销，之后重新开启选项或复用ID也不会复活旧链接。
func saveSubscriptionWorkspace(ctx context.Context, s *store.Store, old store.Document, data json.RawMessage) (store.Document, error) {
	doc, err := s.ReadDocument(ctx, subscriptionsDocument)
	if err != nil {
		return store.Document{}, err
	}
	state, err := readSubscriptions(doc)
	if err != nil {
		return store.Document{}, err
	}
	changed := false
	var draft workspaceDraft
	if json.Unmarshal(data, &draft) != nil {
		return store.Document{}, i18n.New("客户端草稿无效", "Invalid client draft")
	}
	allowed := map[string]bool{}
	for _, client := range draft.Clients {
		allowed[client.ID] = client.Subscription
	}
	for id, item := range state.Clients {
		if !allowed[id] && item.Generation == state.Generation {
			item.Generation = 0
			state.Clients[id] = item
			changed = true
		}
	}
	if !changed {
		return s.SaveDocument(ctx, "workspace", old.Revision, data)
	}
	next, _ := json.Marshal(state)
	err = s.ReplaceDocuments(ctx, map[string]store.Document{"workspace": old, subscriptionsDocument: doc}, map[string]json.RawMessage{"workspace": data, subscriptionsDocument: next})
	if err != nil {
		return store.Document{}, err
	}
	return store.Document{Revision: old.Revision + 1, Data: data}, nil
}

func invalidateSubscriptions(doc store.Document) (json.RawMessage, error) {
	state, err := readSubscriptions(doc)
	if err != nil || state.Generation == math.MaxUint64 {
		return nil, i18n.New("无法撤销原订阅链接，未恢复业务", "Cannot revoke the old subscription links; data not restored")
	}
	state.Generation++
	return json.Marshal(state)
}

func registerSubscriptions(mux *http.ServeMux, rt *Runtime) {
	for _, action := range []string{"status", "rotate", "revoke"} {
		mux.HandleFunc("POST /api/subscription/"+action, func(w http.ResponseWriter, r *http.Request) {
			if rt == nil {
				respond(w, 503, map[string]string{"error": i18n.T("订阅服务未启用", "Subscription service is not enabled")})
				return
			}
			var request subscriptionRequest
			if !readJSON(w, r, &request, 1024) {
				return
			}
			rt.mu.Lock()
			defer rt.mu.Unlock()
			workspace, err := rt.state.ReadDocument(r.Context(), "workspace")
			if err != nil {
				respond(w, 500, map[string]string{"error": i18n.T("无法读取客户端记录", "Cannot read client records")})
				return
			}
			if workspace.Revision != request.WorkspaceRevision {
				respond(w, 409, map[string]string{"error": store.ErrConflict.Error()})
				return
			}
			client, err := subscriptionClient(workspace, request.ClientID)
			if err != nil {
				respond(w, 422, map[string]string{"error": i18n.T("客户端不存在或记录无效", "Client does not exist or its record is invalid")})
				return
			}
			doc, err := rt.state.ReadDocument(r.Context(), subscriptionsDocument)
			if err != nil {
				respond(w, 500, map[string]string{"error": i18n.T("无法读取订阅记录", "Cannot read subscription records")})
				return
			}
			state, err := readSubscriptions(doc)
			if err != nil {
				respond(w, 500, map[string]string{"error": err.Error()})
				return
			}
			item, exists := state.Clients[client.ID]
			result := subscriptionResult{Revision: doc.Revision, Allowed: client.Subscription, Active: exists && item.Generation == state.Generation}
			if action == "status" {
				respond(w, 200, result)
				return
			}
			if doc.Revision != request.Revision || !request.Confirm {
				respond(w, 409, map[string]string{"error": i18n.T("请刷新订阅状态并明确确认本次操作", "Refresh the subscription status and confirm this operation explicitly")})
				return
			}
			if action == "rotate" {
				if !client.Subscription || (!exists && len(state.Clients) >= 5000) {
					respond(w, 422, map[string]string{"error": i18n.T("请先保存开启此客户端的订阅选项，且订阅记录不能超过5000项", "Save the client with subscriptions enabled first (at most 5000 subscription records)")})
					return
				}
				secret := make([]byte, 32)
				if _, err = rand.Read(secret); err != nil {
					respond(w, 500, map[string]string{"error": i18n.T("无法生成随机订阅链接", "Cannot generate a random subscription link")})
					return
				}
				token := base64.RawURLEncoding.EncodeToString(secret)
				hash := sha256.Sum256([]byte(token))
				state.Clients[client.ID] = subscriptionToken{Hash: hex.EncodeToString(hash[:]), Generation: state.Generation}
				result.Path, result.Active = "/sub/"+token, true
			} else if result.Active {
				item.Generation = 0
				state.Clients[client.ID] = item
				result.Active = false
			} else {
				respond(w, 200, result)
				return
			}
			data, _ := json.Marshal(state)
			doc, err = rt.state.SaveDocument(r.Context(), subscriptionsDocument, doc.Revision, data)
			if err != nil {
				code := 500
				if errors.Is(err, store.ErrConflict) {
					code = 409
				}
				respond(w, code, map[string]string{"error": i18n.T("订阅操作未保存，请刷新状态后重试", "Subscription change not saved; refresh the status and retry")})
				return
			}
			result.Revision = doc.Revision
			respond(w, 200, result)
		})
	}
	mux.HandleFunc("/sub/", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("订阅暂不可用", "Subscription temporarily unavailable")})
			return
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		settings, _, err := readSubscriptionSettings(r.Context(), rt.state)
		if err != nil || !settings.Enabled {
			respond(w, 404, map[string]string{"error": i18n.T("订阅不存在或已失效", "Subscription does not exist or has expired")})
			return
		}
		doc, err := rt.state.ReadDocument(r.Context(), subscriptionsDocument)
		if err != nil {
			respond(w, 503, map[string]string{"error": i18n.T("订阅暂不可用", "Subscription temporarily unavailable")})
			return
		}
		state, err := readSubscriptions(doc)
		if err != nil {
			respond(w, 503, map[string]string{"error": i18n.T("订阅暂不可用", "Subscription temporarily unavailable")})
			return
		}
		hash := sha256.Sum256([]byte(strings.TrimPrefix(r.URL.Path, "/sub/")))
		wanted, id := hex.EncodeToString(hash[:]), ""
		for clientID, item := range state.Clients {
			if item.Generation == state.Generation && subtle.ConstantTimeCompare([]byte(item.Hash), []byte(wanted)) == 1 {
				id = clientID
			}
		}
		if id == "" {
			respond(w, 404, map[string]string{"error": i18n.T("订阅不存在或已失效", "Subscription does not exist or has expired")})
			return
		}
		workspace, err := rt.state.ReadDocument(r.Context(), "workspace")
		if err != nil {
			respond(w, 503, map[string]string{"error": i18n.T("订阅暂不可用", "Subscription temporarily unavailable")})
			return
		}
		client, err := subscriptionClient(workspace, id)
		if err != nil || !client.Subscription || !client.Enabled {
			respond(w, 404, map[string]string{"error": i18n.T("订阅不存在或已失效", "Subscription does not exist or has expired")})
			return
		}
		ledger, _, err := rt.ledger(r.Context())
		if err != nil {
			respond(w, 503, map[string]string{"error": i18n.T("订阅暂不可用", "Subscription temporarily unavailable")})
			return
		}
		limit := clientLimit{Quota: quotaBytes(client.QuotaGB), ExpiresAt: client.ExpiresAt}
		if blocked(limit, ledger.Totals[id], time.Now()) {
			respond(w, 404, map[string]string{"error": i18n.T("订阅不存在或已失效", "Subscription does not exist or has expired")})
			return
		}
		result, err := rt.connection(r.Context(), connectionRequest{ClientID: id, WorkspaceRevision: workspace.Revision})
		if err != nil {
			respond(w, 503, map[string]string{"error": i18n.T("订阅暂不可用，请管理员确认配置已应用且核心正在运行", "Subscription temporarily unavailable; the administrator should check that the configuration is applied and the core is running")})
			return
		}
		if blocked(limit, ledger.Totals[id], time.Now()) {
			respond(w, 404, map[string]string{"error": i18n.T("订阅不存在或已失效", "Subscription does not exist or has expired")})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		used := ledger.Totals[id]
		subscriptionHeaders(w, settings, limit, used.Uplink, used.Downlink)
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(subscriptionLinkName(result.Link, settings.RemarkClientName, workspace, client) + "\n"))))
	})
}
