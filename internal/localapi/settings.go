package localapi

import (
	"bytes"
	"encoding/json"
	"independent-panel/internal/i18n"
	"io"
	"net/url"
	"slices"
)

// 这里只校验可保存草稿结构；应用前仍必须执行官方核心预检。
type settingsDraft struct {
	FreedomStrategy string `json:"freedomStrategy"`
	RoutingStrategy string `json:"routingStrategy"`
	ProbeURL        string `json:"probeURL"`
	Statistics      *bool  `json:"statistics"`
	LogLevel        string `json:"logLevel"`
	AccessLog       *bool  `json:"accessLog"`
	DNSLog          *bool  `json:"dnsLog"`
	Rules           string `json:"rules"`
	Outbounds       string `json:"outbounds"`
	Balancers       string `json:"balancers"`
	DNS             string `json:"dns"`
	Advanced        string `json:"advanced"`
}

func validateSettingsDraft(data json.RawMessage) error {
	var draft settingsDraft
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&draft) != nil || decoder.Decode(&struct{}{}) != io.EOF || draft.Statistics == nil || draft.AccessLog == nil || draft.DNSLog == nil {
		return i18n.New("设置草稿字段不完整或格式无效", "Settings draft is incomplete or invalid")
	}
	if !slices.Contains([]string{"AsIs", "UseIP", "UseIPv4", "UseIPv6", "UseIPv4v6", "UseIPv6v4", "ForceIP", "ForceIPv4", "ForceIPv6", "ForceIPv4v6", "ForceIPv6v4"}, draft.FreedomStrategy) || !slices.Contains([]string{"AsIs", "IPIfNonMatch", "IPOnDemand"}, draft.RoutingStrategy) || !slices.Contains([]string{"none", "error", "warning", "info", "debug"}, draft.LogLevel) {
		return i18n.New("策略或日志级别无效", "Invalid strategy or log level")
	}
	u, err := url.Parse(draft.ProbeURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return i18n.New("测试URL必须是无账号密码的HTTP/HTTPS地址", "Test URL must be an HTTP/HTTPS address without credentials")
	}
	for _, text := range []string{draft.Rules, draft.Outbounds, draft.Balancers} {
		var array []map[string]json.RawMessage
		if json.Unmarshal([]byte(text), &array) != nil || array == nil {
			return i18n.New("规则、出站及负载均衡必须为JSON对象数组", "Rules, outbounds and balancers must be JSON arrays of objects")
		}
		for _, item := range array {
			if item == nil {
				return i18n.New("数组成员必须是JSON对象", "Array members must be JSON objects")
			}
		}
	}
	for _, text := range []string{draft.DNS, draft.Advanced} {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(text), &object) != nil || object == nil {
			return i18n.New("DNS及高级配置必须为JSON对象", "DNS and advanced configuration must be JSON objects")
		}
	}
	return nil
}
