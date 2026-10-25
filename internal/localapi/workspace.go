package localapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"independent-panel/internal/i18n"
	"io"
	"math"
	"regexp"
	"strings"

	"independent-panel/internal/config"
)

type inboundDraft struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Preset       string           `json:"preset"`
	Port         int              `json:"port"`
	Listen       string           `json:"listen,omitempty"`
	ShareHost    string           `json:"shareHost,omitempty"`
	PortHopping  string           `json:"portHopping,omitempty"`  // 仅HY2：该UDP端口范围由防火墙助手转发到监听端口（端口跳跃）
	TrafficReset string           `json:"trafficReset,omitempty"` // 仅展示统计周期，不重置累计限额
	Enabled      bool             `json:"enabled"`
	Connection   *connectionDraft `json:"connection,omitempty"`
	Sniffing     struct {
		Enabled         bool     `json:"enabled"`
		HTTP            bool     `json:"http"`
		TLS             bool     `json:"tls"`
		QUIC            bool     `json:"quic"`
		RouteOnly       bool     `json:"routeOnly"`
		FakeDNS         bool     `json:"fakeDns"`
		MetadataOnly    bool     `json:"metadataOnly"`
		IPsExcluded     []string `json:"ipsExcluded,omitempty"`
		DomainsExcluded []string `json:"domainsExcluded,omitempty"`
	} `json:"sniffing"`
}
type connectionDraft struct {
	CertificateFile     string `json:"certificateFile"`
	KeyFile             string `json:"keyFile"`
	Target              string `json:"target"`
	ServerName          string `json:"serverName"`
	Path                string `json:"path"`
	Mode                string `json:"mode"`
	UsePanelCertificate bool   `json:"usePanelCertificate,omitempty"`
	MihomoCompatibility bool   `json:"mihomoCompatibility,omitempty"`
	NoVision            bool   `json:"noVision,omitempty"`
}
type clientDraft struct {
	ID           string  `json:"id"`
	InboundID    string  `json:"inboundId"`
	Name         string  `json:"name"`
	Enabled      bool    `json:"enabled"`
	QuotaGB      float64 `json:"quotaGB"`
	UsedGB       float64 `json:"usedGB"`
	ExpiresAt    *int64  `json:"expiresAt"`
	IPLimit      int     `json:"ipLimit"`
	Subscription bool    `json:"subscription"`
	ReverseTag   string  `json:"reverseTag,omitempty"`
}
type workspaceDraft struct {
	Inbounds []inboundDraft `json:"inbounds"`
	Clients  []clientDraft  `json:"clients"`
}

var draftID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// validatePortHopping：跳跃范围只用于HY2，不能包含任何UDP监听端口，也不能与其他入站的范围重叠，
// 否则转发会抢走那些端口的流量。
func validatePortHopping(inbounds []inboundDraft) error {
	type span struct{ start, end int }
	spans := []span{}
	for _, in := range inbounds {
		if in.PortHopping == "" {
			continue
		}
		start, end, ok := config.ParsePortRange(in.PortHopping)
		if in.Preset != "hy2" || !ok {
			return i18n.New("端口跳跃只用于HY2，范围格式为“起始-结束”（1024至65535，起始小于结束），如48000-48199", "Port hopping applies to HY2 only; the range is start-end (1024–65535, start below end), e.g. 48000-48199")
		}
		for _, other := range inbounds {
			if other.Preset == "hy2" && other.Port >= start && other.Port <= end {
				return i18n.Errorf("端口跳跃范围%s包含UDP监听端口%d，请换一个范围", "Port hopping range %s includes UDP listen port %d; choose another range", in.PortHopping, other.Port)
			}
		}
		for _, other := range spans {
			if start <= other.end && other.start <= end {
				return i18n.Errorf("端口跳跃范围%s与其他入站的范围重叠", "Port hopping range %s overlaps another inbound's range", in.PortHopping)
			}
		}
		spans = append(spans, span{start, end})
	}
	return nil
}

func validateWorkspaceDraft(data, previous json.RawMessage) error {
	var draft, old workspaceDraft
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&draft) != nil || decoder.Decode(&struct{}{}) != io.EOF || draft.Inbounds == nil || draft.Clients == nil {
		return i18n.New("入站和客户端草稿格式无效", "Invalid inbound and client draft format")
	}
	if len(draft.Inbounds) > 500 || len(draft.Clients) > 5000 {
		return i18n.New("本机测试草稿超出数量上限", "Draft exceeds the item limit")
	}
	ids, ports, names := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, in := range draft.Inbounds {
		if in.TrafficReset != "" && in.TrafficReset != "never" && in.TrafficReset != "daily" && in.TrafficReset != "weekly" && in.TrafficReset != "monthly" {
			return i18n.New("节点统计周期无效", "Invalid node statistics period")
		}
		if !draftID.MatchString(in.ID) || ids[in.ID] || strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 128 || in.Port < 1024 || in.Port > 65535 {
			return i18n.New("入站标识、名称或测试端口无效（仅1024至65535）", "Invalid inbound ID, name or port (1024–65535 only)")
		}
		if in.Preset != "reality" && in.Preset != "hy2" && in.Preset != "xhttp-tls" && in.Preset != "xhttp-reality" {
			return i18n.New("入站预设无效", "Invalid inbound preset")
		}
		if _, _, err := config.ProxyEndpoint(in.Listen, in.ShareHost); err != nil {
			return err
		}
		if in.Connection != nil {
			if in.Connection.MihomoCompatibility && in.Preset != "reality" && in.Preset != "xhttp-reality" {
				return i18n.New("Mihomo兼容模式仅用于REALITY", "Mihomo compatibility mode applies to REALITY only")
			}
			if in.Connection.NoVision && in.Preset != "reality" {
				return i18n.New("关闭Vision流控仅用于VLESS Reality（TCP）", "Turning off Vision flow applies to VLESS Reality (TCP) only")
			}
			if in.Connection.UsePanelCertificate && in.Preset != "hy2" && in.Preset != "xhttp-tls" {
				return i18n.New("REALITY不使用面板TLS证书", "REALITY does not use the panel TLS certificate")
			}
			for _, value := range []string{in.Connection.CertificateFile, in.Connection.KeyFile, in.Connection.Target, in.Connection.ServerName, in.Connection.Path, in.Connection.Mode} {
				if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
					return i18n.New("连接参数过长或包含控制字符", "Connection parameters too long or contain control characters")
				}
			}
		}
		if err := config.ValidateSniffing(config.Sniffing{IPsExcluded: in.Sniffing.IPsExcluded, DomainsExcluded: in.Sniffing.DomainsExcluded}); err != nil {
			return err
		}
		transport := "tcp"
		if in.Preset == "hy2" {
			transport = "udp"
		}
		port := fmt.Sprintf("%s:%d", transport, in.Port)
		if ports[port] {
			return i18n.New("草稿监听端口重复", "Duplicate listen port in draft")
		}
		ports[port] = true
		ids[in.ID] = true
	}
	if err := validatePortHopping(draft.Inbounds); err != nil {
		return err
	}
	clientIDs, reverseTags, presets := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, in := range draft.Inbounds {
		presets[in.ID] = in.Preset
	}
	for _, client := range draft.Clients {
		if !draftID.MatchString(client.ID) || clientIDs[client.ID] || !ids[client.InboundID] || strings.TrimSpace(client.Name) == "" || len([]rune(client.Name)) > 64 || names[client.Name] {
			return i18n.New("客户端标识、名称或关联入站无效", "Invalid client ID, name or linked inbound")
		}
		if math.IsNaN(client.QuotaGB) || math.IsInf(client.QuotaGB, 0) || client.QuotaGB < 0 || client.QuotaGB > 1_000_000 || (client.QuotaGB > 0 && client.QuotaGB*1e9 < 1) || client.UsedGB != 0 || client.IPLimit < 0 || client.IPLimit > 10000 {
			return i18n.New("流量草稿无效、IP上限超出0至10000，或设置了用量", "Invalid traffic draft, an IP limit outside 0–10000, or a usage value was set")
		}
		if client.ExpiresAt != nil && (*client.ExpiresAt < 0 || *client.ExpiresAt > 8640000000000000) {
			return i18n.New("客户端到期时间无效", "Invalid client expiry time")
		}
		if client.ReverseTag != "" {
			if !config.ReverseTagPattern.MatchString(client.ReverseTag) || presets[client.InboundID] == "hy2" || reverseTags[client.ReverseTag] {
				return i18n.New("反向代理标签只用于VLESS入站，须唯一，且只能含字母、数字、点、下划线和连字符", "Reverse tags apply only to VLESS inbounds, must be unique and may contain only letters, digits, dots, underscores and hyphens")
			}
			reverseTags[client.ReverseTag] = true
		}
		clientIDs[client.ID] = true
		names[client.Name] = true
	}
	// 允许批量删除，删除入站时其客户端一并删除（用户2026-10-22决定，页面先列出将删除的项目并确认）；
	// 剩余客户端不能指向已删除的入站，已由上面的关联检查保证。已有草稿损坏时仍拒绝覆盖。
	if len(previous) > 0 && string(previous) != "null" && json.Unmarshal(previous, &old) != nil {
		return i18n.New("已有草稿损坏，拒绝覆盖", "Existing draft is corrupted; refusing to overwrite")
	}
	return nil
}
