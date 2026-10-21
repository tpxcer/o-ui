package localapi

import (
	"encoding/json"
	"independent-panel/internal/i18n"
	"strings"
)

// reverseTags是入站客户端的反向代理标签：核心为每个桥接连接生成同名出站，路由可以引用，但不能与静态出站重名。
func compileSettings(data json.RawMessage, reverseTags ...string) (map[string]any, error) {
	if string(data) == "null" {
		data = json.RawMessage(`{"freedomStrategy":"AsIs","routingStrategy":"AsIs","probeURL":"https://www.google.com/generate_204","statistics":true,"logLevel":"warning","accessLog":false,"dnsLog":false,"rules":"[]","outbounds":"[{\"tag\":\"direct\",\"protocol\":\"freedom\",\"settings\":{}},{\"tag\":\"block\",\"protocol\":\"blackhole\",\"settings\":{}}]","balancers":"[]","dns":"{\"servers\":[\"localhost\"],\"queryStrategy\":\"UseIP\"}","advanced":"{}"}`)
	}
	if err := validateSettingsDraft(data); err != nil {
		return nil, err
	}
	var d settingsDraft
	_ = json.Unmarshal(data, &d)
	var rules, outbounds, balancers []map[string]any
	var dns, advanced map[string]any
	_ = json.Unmarshal([]byte(d.Rules), &rules)
	_ = json.Unmarshal([]byte(d.Outbounds), &outbounds)
	_ = json.Unmarshal([]byte(d.Balancers), &balancers)
	_ = json.Unmarshal([]byte(d.DNS), &dns)
	_ = json.Unmarshal([]byte(d.Advanced), &advanced)
	if len(outbounds) == 0 {
		return nil, i18n.New("至少保留一个明确出站", "Keep at least one explicit outbound")
	}
	tags := map[string]bool{}
	for _, outbound := range outbounds {
		tag, ok := outbound["tag"].(string)
		if !ok || strings.TrimSpace(tag) == "" || tags[tag] {
			return nil, i18n.New("出站tag缺失或重复", "Outbound tag missing or duplicated")
		}
		tags[tag] = true
		protocol, _ := outbound["protocol"].(string)
		switch protocol {
		case "freedom", "blackhole", "vless", "vmess", "trojan", "shadowsocks", "socks", "http", "hysteria", "dns", "loopback":
		case "wireguard":
			// 只接受用户态网络栈（如WARP出站），不创建系统网卡、不需要额外权限。
			if settings, _ := outbound["settings"].(map[string]any); settings == nil || settings["noKernelTun"] != true {
				return nil, i18n.New("WireGuard出站须设置noKernelTun为true（只用用户态网络栈，不创建系统网卡）", "WireGuard outbounds must set noKernelTun to true (userspace stack only, no system interface)")
			}
		default:
			return nil, i18n.New("当前本机应用不接受该出站协议，避免创建系统接口或扩大权限", "This outbound protocol is not accepted, to avoid creating system interfaces or extending privileges")
		}
		if protocol == "freedom" {
			settings, ok := outbound["settings"].(map[string]any)
			if outbound["settings"] != nil && !ok {
				return nil, i18n.New("Freedom settings必须为对象", "Freedom settings must be an object")
			}
			if settings == nil {
				settings = map[string]any{}
			}
			settings["targetStrategy"] = d.FreedomStrategy
			delete(settings, "domainStrategy")
			outbound["settings"] = settings
		}
	}
	routable := map[string]bool{}
	for tag := range tags {
		routable[tag] = true
	}
	for _, tag := range reverseTags {
		if tags[tag] {
			return nil, i18n.New("反向代理标签与出站标识重复", "A reverse tag duplicates an outbound tag")
		}
		routable[tag] = true
	}
	balancerTags := map[string]bool{}
	for _, balancer := range balancers {
		tag, ok := balancer["tag"].(string)
		if !ok || strings.TrimSpace(tag) == "" || balancerTags[tag] {
			return nil, i18n.New("负载均衡tag缺失或重复", "Balancer tag missing or duplicated")
		}
		balancerTags[tag] = true
		selectors, ok := balancer["selector"].([]any)
		if !ok || len(selectors) == 0 {
			return nil, i18n.New("负载均衡selector必须为非空数组", "Balancer selector must be a non-empty array")
		}
		for _, selector := range selectors {
			prefix, ok := selector.(string)
			matched := false
			if ok && prefix != "" {
				for tag := range tags {
					if strings.HasPrefix(tag, prefix) {
						matched = true
					}
				}
			}
			if !matched {
				return nil, i18n.New("负载均衡selector未匹配任何出站", "Balancer selector matches no outbound")
			}
		}
		if fallback, exists := balancer["fallbackTag"]; exists {
			tag, _ := fallback.(string)
			if !tags[tag] {
				return nil, i18n.New("负载均衡fallbackTag不存在", "Balancer fallbackTag does not exist")
			}
		}
	}
	for _, rule := range rules {
		out, hasOut := rule["outboundTag"]
		balancer, hasBalancer := rule["balancerTag"]
		if rule["type"] != "field" || hasOut == hasBalancer {
			return nil, i18n.New("路由必须为field，且只引用一个出站或负载均衡", "Routing rules must be type field and reference exactly one outbound or balancer")
		}
		if hasOut {
			tag, _ := out.(string)
			if !routable[tag] {
				return nil, i18n.New("路由引用不存在的出站", "Routing rule references a missing outbound")
			}
		}
		if hasBalancer {
			tag, _ := balancer.(string)
			if !balancerTags[tag] {
				return nil, i18n.New("路由引用不存在的负载均衡", "Routing rule references a missing balancer")
			}
		}
		conditions := 0
		for _, key := range []string{"domain", "ip", "source", "protocol", "inboundTag", "user", "process"} {
			if value, exists := rule[key]; exists {
				values, ok := value.([]any)
				if !ok || len(values) == 0 {
					return nil, i18n.New("路由匹配条件必须为非空字符串数组", "Routing match conditions must be non-empty string arrays")
				}
				for _, value := range values {
					text, ok := value.(string)
					if !ok || strings.TrimSpace(text) == "" {
						return nil, i18n.New("路由匹配条件必须为非空字符串数组", "Routing match conditions must be non-empty string arrays")
					}
				}
				conditions++
			}
		}
		for _, key := range []string{"port", "sourcePort", "network", "attrs"} {
			if value, exists := rule[key]; exists {
				text, ok := value.(string)
				if !ok || strings.TrimSpace(text) == "" {
					return nil, i18n.New("路由匹配条件必须为非空字符串", "Routing match conditions must be non-empty strings")
				}
				if key == "network" {
					for _, network := range strings.Split(text, ",") {
						if strings.TrimSpace(network) != "tcp" && strings.TrimSpace(network) != "udp" {
							return nil, i18n.New("路由network只接受tcp、udp或tcp,udp", "Routing network accepts only tcp, udp or tcp,udp")
						}
					}
				}
				conditions++
			}
		}
		if conditions == 0 {
			return nil, i18n.New("路由必须至少提供一个匹配条件", "Routing rules need at least one match condition")
		}
	}
	servers, ok := dns["servers"].([]any)
	if !ok || len(servers) == 0 {
		return nil, i18n.New("DNS必须有非空servers数组", "DNS needs a non-empty servers array")
	}
	for _, server := range servers {
		if text, ok := server.(string); ok {
			if strings.TrimSpace(text) != "" {
				continue
			}
		}
		if object, ok := server.(map[string]any); ok {
			if text, ok := object["address"].(string); ok && strings.TrimSpace(text) != "" {
				continue
			}
		}
		return nil, i18n.New("DNS服务器必须为非空地址或带address的对象", "DNS servers must be non-empty addresses or objects with address")
	}
	if strategy, exists := dns["queryStrategy"]; exists && strategy != "UseIP" && strategy != "UseIPv4" && strategy != "UseIPv6" {
		return nil, i18n.New("DNS查询策略无效", "Invalid DNS query strategy")
	}
	for key, value := range advanced {
		switch key {
		case "observatory", "burstObservatory", "transport":
			if _, ok := value.(map[string]any); !ok {
				return nil, i18n.New("高级配置结构无效", "Invalid advanced configuration structure")
			}
		case "fakedns":
			if _, ok := value.([]any); !ok {
				return nil, i18n.New("FakeDNS必须为数组", "FakeDNS must be an array")
			}
		default:
			return nil, i18n.New("高级配置不允许覆盖入站、控制接口、日志或其他分类", "Advanced configuration may not override inbounds, control API, logs or other categories")
		}
	}
	// 核心输出不回传、不落日志文件，避免连接信息和凭据进入浏览器或普通日志。
	access := "none"
	if *d.AccessLog {
		access = ""
	}
	advanced["log"] = map[string]any{"loglevel": d.LogLevel, "access": access, "dnsLog": *d.DNSLog}
	advanced["outbounds"] = outbounds
	advanced["routing"] = map[string]any{"domainStrategy": d.RoutingStrategy, "rules": rules, "balancers": balancers}
	advanced["dns"] = dns
	// 统计常开：流量额度与首页业务网速都依赖用户计数。
	advanced["stats"] = map[string]any{}
	advanced["policy"] = map[string]any{"levels": map[string]any{"0": map[string]bool{"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}}, "system": map[string]bool{"statsInboundUplink": true, "statsInboundDownlink": true, "statsOutboundUplink": true, "statsOutboundDownlink": true}}
	return advanced, nil
}
