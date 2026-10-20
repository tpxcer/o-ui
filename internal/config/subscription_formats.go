package config

import (
	"encoding/json"
	"independent-panel/internal/i18n"
	"strconv"
	"strings"
)

// 订阅的Clash/Mihomo与Xray JSON格式都从ClientConnection生成的官方客户端出站转换，与分享链接同源。
type clientOutbound struct {
	Protocol string `json:"protocol"`
	Settings struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
		Vnext   []struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
			Users   []struct {
				ID   string `json:"id"`
				Flow string `json:"flow"`
			} `json:"users"`
		} `json:"vnext"`
	} `json:"settings"`
	StreamSettings struct {
		Network          string `json:"network"`
		Security         string `json:"security"`
		HysteriaSettings struct {
			Auth string `json:"auth"`
		} `json:"hysteriaSettings"`
		RealitySettings struct {
			Fingerprint string `json:"fingerprint"`
			ServerName  string `json:"serverName"`
			Password    string `json:"password"`
			ShortID     string `json:"shortId"`
		} `json:"realitySettings"`
		TLSSettings struct {
			ServerName string   `json:"serverName"`
			ALPN       []string `json:"alpn"`
		} `json:"tlsSettings"`
		XHTTPSettings struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		} `json:"xhttpSettings"`
	} `json:"streamSettings"`
}

// YAMLField是有序的YAML键值；值只用string、int、bool、[]string或[]YAMLField。
type YAMLField struct {
	Key   string
	Value any
}

func parseClientOutbound(outbound map[string]any) (clientOutbound, error) {
	var out clientOutbound
	data, err := json.Marshal(outbound)
	if err != nil || json.Unmarshal(data, &out) != nil {
		return out, i18n.New("客户端出站无效", "Invalid client outbound")
	}
	return out, nil
}

// ClashProxy生成一个Clash/Mihomo代理条目；只校验证书，不输出skip-cert-verify。
func ClashProxy(outbound map[string]any, name string) ([]YAMLField, error) {
	out, err := parseClientOutbound(outbound)
	if err != nil {
		return nil, err
	}
	stream := out.StreamSettings
	switch {
	case out.Protocol == "hysteria" && stream.HysteriaSettings.Auth != "" && stream.Security == "tls":
		proxy := []YAMLField{{"name", name}, {"type", "hysteria2"}, {"server", out.Settings.Address}, {"port", out.Settings.Port}, {"password", stream.HysteriaSettings.Auth}, {"udp", true}, {"sni", stream.TLSSettings.ServerName}}
		if len(stream.TLSSettings.ALPN) > 0 {
			proxy = append(proxy, YAMLField{"alpn", stream.TLSSettings.ALPN})
		}
		return proxy, nil
	case out.Protocol == "vless" && len(out.Settings.Vnext) == 1 && len(out.Settings.Vnext[0].Users) == 1:
		server, user := out.Settings.Vnext[0], out.Settings.Vnext[0].Users[0]
		proxy := []YAMLField{{"name", name}, {"type", "vless"}, {"server", server.Address}, {"port", server.Port}, {"uuid", user.ID}, {"udp", true}, {"network", stream.Network}, {"tls", true}}
		if user.Flow != "" {
			proxy = append(proxy, YAMLField{"flow", user.Flow})
		}
		switch stream.Security {
		case "reality":
			r := stream.RealitySettings
			proxy = append(proxy, YAMLField{"servername", r.ServerName}, YAMLField{"client-fingerprint", r.Fingerprint}, YAMLField{"reality-opts", []YAMLField{{"public-key", r.Password}, {"short-id", r.ShortID}}})
		case "tls":
			proxy = append(proxy, YAMLField{"servername", stream.TLSSettings.ServerName}, YAMLField{"client-fingerprint", "chrome"})
			if len(stream.TLSSettings.ALPN) > 0 {
				proxy = append(proxy, YAMLField{"alpn", stream.TLSSettings.ALPN})
			}
		default:
			return nil, i18n.New("客户端安全参数无效", "Invalid client security parameters")
		}
		if stream.Network == "xhttp" {
			proxy = append(proxy, YAMLField{"xhttp-opts", []YAMLField{{"path", stream.XHTTPSettings.Path}, {"mode", stream.XHTTPSettings.Mode}}})
		} else if stream.Network != "tcp" {
			return nil, i18n.New("客户端传输方式无效", "Invalid client transport")
		}
		return proxy, nil
	}
	return nil, i18n.New("客户端协议或认证参数无效", "Invalid client protocol or authentication parameters")
}

// ClashConfig输出只含代理、选择组与兜底规则的YAML，其余由客户端自身配置决定。
func ClashConfig(proxy []YAMLField) string {
	name, _ := proxy[0].Value.(string)
	config := []YAMLField{
		{"proxies", [][]YAMLField{proxy}},
		{"proxy-groups", [][]YAMLField{{{"name", "PROXY"}, {"type", "select"}, {"proxies", []string{name, "DIRECT"}}}}},
		{"rules", []string{"MATCH,PROXY"}},
	}
	var b strings.Builder
	writeYAML(&b, config, "")
	return b.String()
}

func writeYAML(b *strings.Builder, fields []YAMLField, indent string) {
	for _, field := range fields {
		b.WriteString(indent + field.Key + ":")
		switch value := field.Value.(type) {
		case []YAMLField:
			b.WriteString("\n")
			writeYAML(b, value, indent+"  ")
		case []string:
			b.WriteString("\n")
			for _, item := range value {
				b.WriteString(indent + "  - " + strconv.Quote(item) + "\n")
			}
		case [][]YAMLField:
			b.WriteString("\n")
			for _, item := range value {
				var inner strings.Builder
				writeYAML(&inner, item, indent+"    ")
				b.WriteString(indent + "  - " + strings.TrimPrefix(inner.String(), indent+"    "))
			}
		default:
			b.WriteString(" " + yamlScalar(value) + "\n")
		}
	}
}

// 字符串一律用双引号（Go转义是YAML双引号转义的子集），避免yes/no、冒号等被误解析。
func yamlScalar(value any) string {
	switch v := value.(type) {
	case string:
		return strconv.Quote(v)
	case int:
		return strconv.Itoa(v)
	case bool:
		return strconv.FormatBool(v)
	}
	return `""`
}

// JSONOptions是Xray JSON订阅的附加选项（参照OUI）：TLS握手分片与直连规则。
type JSONOptions struct {
	Fragment         bool
	FragmentPackets  string
	FragmentLength   string
	FragmentInterval string
	DirectIP         []string
	DirectDomain     []string
}

// XrayClientConfig生成可直接导入的完整Xray客户端配置；本地代理只监听回环。
func XrayClientConfig(outbound map[string]any, name string, options JSONOptions) (map[string]any, error) {
	if _, err := parseClientOutbound(outbound); err != nil {
		return nil, err
	}
	proxy := map[string]any{}
	for key, value := range outbound {
		proxy[key] = value
	}
	proxy["tag"] = "proxy"
	outbounds := []any{proxy, map[string]any{"tag": "direct", "protocol": "freedom"}, map[string]any{"tag": "block", "protocol": "blackhole"}}
	if stream, ok := outbound["streamSettings"].(map[string]any); ok && options.Fragment && outbound["protocol"] != "hysteria" {
		copied := map[string]any{}
		for key, value := range stream {
			copied[key] = value
		}
		copied["sockopt"] = map[string]any{"dialerProxy": "fragment", "tcpKeepAliveIdle": 100}
		proxy["streamSettings"] = copied
		outbounds = append(outbounds, map[string]any{"tag": "fragment", "protocol": "freedom", "settings": map[string]any{"fragment": map[string]any{"packets": options.FragmentPackets, "length": options.FragmentLength, "interval": options.FragmentInterval}}})
	}
	rules := []any{}
	if len(options.DirectIP) > 0 {
		rules = append(rules, map[string]any{"type": "field", "ip": options.DirectIP, "outboundTag": "direct"})
	}
	if len(options.DirectDomain) > 0 {
		rules = append(rules, map[string]any{"type": "field", "domain": options.DirectDomain, "outboundTag": "direct"})
	}
	rules = append(rules, map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"})
	sniffing := map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}
	return map[string]any{
		"remarks": name,
		"log":     map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{"tag": "socks", "listen": "127.0.0.1", "port": 10808, "protocol": "socks", "settings": map[string]any{"udp": true}, "sniffing": sniffing},
			map[string]any{"tag": "http", "listen": "127.0.0.1", "port": 10809, "protocol": "http", "sniffing": sniffing},
		},
		"outbounds": outbounds,
		"routing":   map[string]any{"domainStrategy": "AsIs", "rules": rules},
	}, nil
}
