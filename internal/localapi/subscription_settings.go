package localapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const subscriptionSettingsDocument = "subscription-settings"

// 订阅服务的全局设置（参照OUI订阅页）：总开关、客户端更新间隔、订阅名称、流量/到期信息与节点名称格式。
type subscriptionSettings struct {
	Enabled          bool   `json:"enabled"`
	UpdateHours      int    `json:"updateHours"`
	Title            string `json:"title"`
	ShowInfo         bool   `json:"showInfo"`
	RemarkClientName bool   `json:"remarkClientName"`
}

func defaultSubscriptionSettings() subscriptionSettings {
	return subscriptionSettings{Enabled: true, UpdateHours: 12, Title: "o-ui", ShowInfo: true, RemarkClientName: true}
}

// 未出现的字段取默认值，旧版保存的设置不会因新增字段改变行为。
func validateSubscriptionSettings(data json.RawMessage) (subscriptionSettings, error) {
	s := defaultSubscriptionSettings()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&s) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return s, i18n.New("订阅设置格式无效", "Invalid subscription settings")
	}
	if s.UpdateHours < 1 || s.UpdateHours > 168 {
		return s, i18n.New("订阅更新间隔须为1至168小时", "Subscription update interval must be 1–168 hours")
	}
	if !utf8.ValidString(s.Title) || utf8.RuneCountInString(s.Title) > 64 {
		return s, i18n.New("订阅名称最多64个字符", "Subscription title must be at most 64 characters")
	}
	for _, r := range s.Title {
		if unicode.IsControl(r) {
			return s, i18n.New("订阅名称不能包含控制字符", "Subscription title must not contain control characters")
		}
	}
	return s, nil
}

func readSubscriptionSettings(ctx context.Context, state *store.Store) (subscriptionSettings, int64, error) {
	doc, err := state.ReadDocument(ctx, subscriptionSettingsDocument)
	if err != nil {
		return subscriptionSettings{}, 0, err
	}
	if len(doc.Data) == 0 || bytes.Equal(doc.Data, []byte("null")) {
		return defaultSubscriptionSettings(), doc.Revision, nil
	}
	s, err := validateSubscriptionSettings(doc.Data)
	return s, doc.Revision, err
}

// subscriptionLinkName按设置把分享链接的节点名称改为“入站名-客户端名”或仅入站名；找不到入站时保留客户端名。
func subscriptionLinkName(link string, withClient bool, workspace store.Document, client clientDraft) string {
	var draft workspaceDraft
	inbound := ""
	if json.Unmarshal(workspace.Data, &draft) == nil {
		for _, in := range draft.Inbounds {
			if in.ID == client.InboundID {
				inbound = in.Name
			}
		}
	}
	name := client.Name
	if inbound != "" && withClient {
		name = inbound + "-" + client.Name
	} else if inbound != "" {
		name = inbound
	}
	base, _, _ := strings.Cut(link, "#")
	return base + (&url.URL{Fragment: name}).String()
}

// subscriptionHeaders写入常见客户端识别的更新间隔、名称与流量/到期信息。
func subscriptionHeaders(w http.ResponseWriter, s subscriptionSettings, limit clientLimit, upload, download uint64) {
	w.Header().Set("Profile-Update-Interval", strconv.Itoa(s.UpdateHours))
	if s.Title != "" {
		w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(s.Title)))
	}
	if s.ShowInfo {
		info := fmt.Sprintf("upload=%d; download=%d; total=%d", upload, download, limit.Quota)
		if limit.ExpiresAt != nil {
			info += fmt.Sprintf("; expire=%d", *limit.ExpiresAt/1000)
		}
		w.Header().Set("Subscription-Userinfo", info)
	}
}

func registerSubscriptionSettings(mux *http.ServeMux, state *store.Store) {
	mux.HandleFunc("GET /api/panel/subscription", func(w http.ResponseWriter, r *http.Request) {
		s, revision, err := readSubscriptionSettings(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取订阅设置失败", "Failed to read subscription settings")})
			return
		}
		respond(w, 200, map[string]any{"revision": revision, "data": s})
	})
	mux.HandleFunc("PUT /api/panel/subscription", func(w http.ResponseWriter, r *http.Request) {
		var request store.Document
		if !readJSON(w, r, &request, 4096) {
			return
		}
		s, err := validateSubscriptionSettings(request.Data)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		data, _ := json.Marshal(s)
		doc, err := state.SaveDocument(r.Context(), subscriptionSettingsDocument, request.Revision, data)
		if err != nil {
			code, message := 500, i18n.T("保存订阅设置失败", "Failed to save subscription settings")
			if errors.Is(err, store.ErrConflict) {
				code, message = 409, err.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		respond(w, 200, map[string]any{"revision": doc.Revision, "data": s})
	})
}
