package config

import (
	"independent-panel/internal/i18n"
	"net/netip"
	"strings"
)

// 只校验显式输入，不解析DNS或探测本机网络。
func ProxyEndpoint(listen, shareHost string) (string, string, error) {
	if listen == "" {
		listen = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(listen)
	if err != nil || ip.Zone() != "" || ip.Unmap().IsMulticast() || ip.Unmap().String() == "255.255.255.255" {
		return "", "", i18n.New("代理监听地址须为IPv4或IPv6，不含端口、方括号或区域标识", "Proxy listen address must be IPv4 or IPv6 without port, brackets or zone")
	}
	ip = ip.Unmap()
	if shareHost == "" {
		if !ip.IsLoopback() {
			return "", "", i18n.New("非回环代理监听必须明确填写分享主机（域名或IP）", "A non-loopback proxy listen address requires an explicit share host (domain or IP)")
		}
		shareHost = ip.String()
	}
	if !ValidShareHost(shareHost) {
		return "", "", i18n.New("分享主机须为有效域名或单播IP，不含协议、端口、路径、凭据或方括号", "Share host must be a valid domain or unicast IP without scheme, port, path, credentials or brackets")
	}
	return ip.String(), shareHost, nil
}

func ValidShareHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && !ip.Unmap().IsUnspecified() && !ip.Unmap().IsMulticast() && ip.Unmap().String() != "255.255.255.255"
	}
	if len(host) == 0 || len(host) > 253 || !hostname.MatchString(host) {
		return false
	}
	if strings.Trim(host, "0123456789.") == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}
