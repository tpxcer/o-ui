package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 安装器提供的root防火墙助手：只接受放行/撤销1024以上单个端口，并只删除自己加过的规则。
const firewallHelper = "/usr/local/libexec/o-ui-firewall"

var runFirewall = func(ctx context.Context, action, port, protocol string) (string, error) {
	if _, err := os.Stat(firewallHelper); err != nil {
		return "", err
	}
	return firewallCommand(ctx, action, port, protocol)
}

func firewallCommand(ctx context.Context, args ...string) (string, error) {
	return helperCommand(ctx, firewallHelper, args...)
}

// helperCommand经sudo -n运行安装器部署的root助手。
func helperCommand(ctx context.Context, helper string, args ...string) (string, error) {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// 只把助手的标准输出给页面；sudo的主机名解析等警告走标准错误，仅在失败时显示。
	var output, warnings bytes.Buffer
	command := exec.CommandContext(ctx, sudo, append([]string{"-n", helper}, args...)...)
	command.Stdout, command.Stderr = &output, &warnings
	err = command.Run()
	text := strings.TrimSpace(output.String())
	if err != nil && text == "" {
		text = strings.TrimSpace(warnings.String())
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text, err
}

var errPortHoppingHelperOld = i18n.New("防火墙助手是旧版本，不支持端口跳跃；请在服务器上再运行一次 o-ui update", "The firewall helper is an old version without port hopping; run o-ui update once more on the server")

// 调用root防火墙助手的端口跳跃转发（redirect add|delete|flush）；测试中替换。
var runPortHopping = func(ctx context.Context, args ...string) (string, error) {
	data, err := os.ReadFile(firewallHelper)
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(data), "HOP=o-ui-hop") {
		return "", errPortHoppingHelperOld
	}
	return firewallCommand(ctx, append([]string{"redirect"}, args...)...)
}

// portHoppingRules返回草稿中HY2入站的端口跳跃转发，键为“范围 监听端口”。
func portHoppingRules(data json.RawMessage) map[string]bool {
	rules := map[string]bool{}
	var draft workspaceDraft
	if json.Unmarshal(data, &draft) != nil {
		return rules
	}
	for _, in := range draft.Inbounds {
		if in.Preset == "hy2" && in.PortHopping != "" {
			rules[in.PortHopping+" "+strconv.Itoa(in.Port)] = true
		}
	}
	return rules
}

// RestorePortHopping在面板启动时清空并按入站记录重建端口跳跃转发：规则只在内核内存，服务器重启后已清空。
func (rt *Runtime) RestorePortHopping(ctx context.Context) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	doc, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		return err
	}
	rules := portHoppingRules(doc.Data)
	if _, err := runPortHopping(ctx, "flush"); err != nil {
		if len(rules) == 0 {
			return nil
		}
		return err
	}
	var failed error
	for key := range rules {
		hop, port, _ := strings.Cut(key, " ")
		if _, err := runPortHopping(ctx, "add", hop, port); err != nil && failed == nil {
			failed = err
		}
	}
	return failed
}

// inboundEndpoints返回草稿中每个入站的“端口/协议”；HY2为UDP，其余为TCP。
func inboundEndpoints(data json.RawMessage) map[string]bool {
	endpoints := map[string]bool{}
	var draft workspaceDraft
	if json.Unmarshal(data, &draft) != nil {
		return endpoints
	}
	for _, in := range draft.Inbounds {
		protocol := "tcp"
		if in.Preset == "hy2" {
			protocol = "udp"
		}
		endpoints[strconv.Itoa(in.Port)+"/"+protocol] = true
	}
	return endpoints
}

// syncFirewall为新增端口放行、为已删除端口撤销（参照OUI），返回给页面显示的结果；失败不影响已保存的配置。
func syncFirewall(ctx context.Context, before, after json.RawMessage) string {
	old, next := inboundEndpoints(before), inboundEndpoints(after)
	var results, manual []string
	keys := make([]string, 0, len(old)+len(next))
	for key := range next {
		keys = append(keys, key)
	}
	for key := range old {
		if !next[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if old[key] && next[key] {
			continue
		}
		port, protocol, _ := strings.Cut(key, "/")
		action := "allow"
		if old[key] {
			action = "delete"
		}
		text, err := runFirewall(ctx, action, port, protocol)
		switch {
		case err == nil && text != "":
			results = append(results, text)
		case err == nil:
			results = append(results, key)
		case action == "allow":
			manual = append(manual, key)
		}
	}
	if len(manual) > 0 {
		results = append(results, i18n.T("未能自动放行，如服务器开了防火墙请手动放行：", "Could not allow automatically; if the server has a firewall, allow manually: ")+strings.Join(manual, ", "))
	}
	// HY2端口跳跃：先撤销不再需要的转发，再添加新的。
	oldHops, nextHops := portHoppingRules(before), portHoppingRules(after)
	var hopFailed []string
	var hopErr error
	for _, action := range []string{"delete", "add"} {
		from, other := oldHops, nextHops
		if action == "add" {
			from, other = nextHops, oldHops
		}
		keys := make([]string, 0, len(from))
		for key := range from {
			if !other[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			hop, port, _ := strings.Cut(key, " ")
			text, err := runPortHopping(ctx, action, hop, port)
			switch {
			case err == nil && text != "":
				results = append(results, text)
			case err == nil:
			case action == "add":
				hopFailed, hopErr = append(hopFailed, hop+"→"+port), err
			}
		}
	}
	if len(hopFailed) > 0 {
		reason := ""
		if errors.Is(hopErr, errPortHoppingHelperOld) {
			reason = i18n.T("（", " (") + hopErr.Error() + i18n.T("）", ")")
		}
		results = append(results, i18n.T("未能设置端口跳跃转发：", "Could not set up port hopping: ")+strings.Join(hopFailed, ", ")+reason)
	}
	return strings.Join(results, "; ")
}
