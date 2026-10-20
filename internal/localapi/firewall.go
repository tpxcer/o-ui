package localapi

import (
	"bytes"
	"context"
	"encoding/json"
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
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// 只把助手的标准输出给页面；sudo的主机名解析等警告走标准错误，仅在失败时显示。
	var output, warnings bytes.Buffer
	command := exec.CommandContext(ctx, sudo, "-n", firewallHelper, action, port, protocol)
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
	return strings.Join(results, "; ")
}
