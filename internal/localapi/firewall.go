package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 安装器提供的root防火墙助手：只接受放行/撤销1024以上单个端口，并只删除自己加过的规则。
const firewallHelper = "/usr/local/libexec/o-ui-firewall"

const legacyPortHoppingLabel = "端口跳跃"

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
	return syncFirewallReport(ctx, before, after).Message
}

type firewallEndpoint struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	CheckedAt string `json:"checkedAt"`
	Operation string `json:"operation,omitempty"`
}

type firewallReport struct {
	Revision  int64                       `json:"workspaceRevision"`
	Endpoints map[string]firewallEndpoint `json:"endpoints"`
	Message   string                      `json:"message,omitempty"`
}

func syncFirewallReport(ctx context.Context, before, after json.RawMessage) firewallReport {
	report := firewallReport{Endpoints: map[string]firewallEndpoint{}}
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
		status := firewallEndpoint{State: "allowed", Message: text, CheckedAt: time.Now().UTC().Format(time.RFC3339), Operation: action}
		if action == "delete" {
			status.State = "removed"
		}
		if err != nil {
			status.State = "failed"
			if status.Message == "" {
				status.Message = err.Error()
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 4 {
				status.State = "unverified"
			}
		}
		if strings.Contains(text, "no firewall, nothing changed") {
			status.State = "unverified"
		}
		report.Endpoints[key] = status
		switch {
		case err == nil && text != "":
			results = append(results, text)
		case err == nil:
			results = append(results, key)
		case action == "allow":
			manual = append(manual, key)
		case err != nil:
			results = append(results, i18n.T("未能撤销旧端口：", "Could not remove the old port: ")+key)
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
			if err != nil {
				report.Endpoints[port+"/udp"] = firewallEndpoint{State: "failed", Message: i18n.T("端口跳跃转发操作失败：", "Port hopping update failed: ") + hop + "→" + port, CheckedAt: time.Now().UTC().Format(time.RFC3339), Operation: "redirect-" + action}
			}
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
	report.Message = strings.Join(results, "; ")
	return report
}

// 保存结果独立于业务草稿，刷新页面仍能看到上次放行失败；不把记录当作公网连通性检查。
func saveFirewallReport(ctx context.Context, state *store.Store, before, after json.RawMessage, revision int64) (firewallReport, error) {
	old, err := state.ReadDocument(ctx, "firewall-status")
	if err != nil {
		return firewallReport{}, err
	}
	report := syncFirewallReport(ctx, before, after)
	var previous firewallReport
	_ = json.Unmarshal(old.Data, &previous)
	for key, status := range previous.Endpoints {
		if _, changed := report.Endpoints[key]; !changed {
			report.Endpoints[key] = status
		}
	}
	for key, status := range report.Endpoints {
		if status.State == "removed" {
			delete(report.Endpoints, key)
		}
	}
	report.Revision = revision
	data, err := json.Marshal(report)
	if err == nil {
		_, err = state.SaveDocument(ctx, "firewall-status", old.Revision, data)
	}
	return report, err
}

func registerFirewall(mux *http.ServeMux, rt *Runtime, state *store.Store) {
	mux.HandleFunc("POST /api/runtime/firewall/cleanup", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		var request struct {
			Endpoint string `json:"endpoint"`
		}
		if !readJSON(w, r, &request, 256) {
			return
		}
		port, protocol, ok := strings.Cut(request.Endpoint, "/")
		number, err := strconv.Atoi(port)
		if !ok || err != nil || number < 1024 || number > 65535 || port != strconv.Itoa(number) || (protocol != "tcp" && protocol != "udp") {
			respond(w, 400, map[string]string{"error": i18n.T("旧端口格式无效", "Invalid old-port endpoint")})
			return
		}
		// 与草稿保存共用运行时锁，检查与撤销之间不允许新入站占用该端口。
		rt.mu.Lock()
		defer rt.mu.Unlock()
		doc, err := state.ReadDocument(r.Context(), "workspace")
		var draft workspaceDraft
		if err != nil || json.Unmarshal(doc.Data, &draft) != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取入站草稿失败", "Failed to read inbound draft")})
			return
		}
		// 即使协议不同或入站停用也保守保留，避免撤销用户仍在配置的端口。
		for _, inbound := range draft.Inbounds {
			if inbound.Port == number {
				respond(w, 409, map[string]string{"error": i18n.T("该端口仍被当前入站使用，不能撤销", "This port is still used by a current inbound; removal refused")})
				return
			}
		}
		if current := rt.engine.Status(); current.Running && current.WorkspaceRevision != doc.Revision {
			respond(w, 409, map[string]string{"error": i18n.T("核心仍在运行旧修订，请先核对并应用当前入站，再撤销旧端口", "The core is running an older revision; verify and apply current inbounds before removing old-port rules")})
			return
		}
		stored, err := state.ReadDocument(r.Context(), "firewall-status")
		var report firewallReport
		if err != nil || json.Unmarshal(stored.Data, &report) != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取防火墙状态失败", "Failed to read firewall status")})
			return
		}
		status, exists := report.Endpoints[request.Endpoint]
		if !exists || (status.State != "failed" && status.State != "unverified") {
			respond(w, 404, map[string]string{"error": i18n.T("没有此端口的待处理记录", "No pending record for this port")})
			return
		}
		if (status.Operation != "" && status.Operation != "delete") || strings.Contains(status.Message, legacyPortHoppingLabel) || strings.Contains(status.Message, "Port hopping") {
			respond(w, 409, map[string]string{"error": i18n.T("该记录不属于旧端口放行规则撤销，请在服务器核查", "This record is not an old allow-rule removal; inspect it on the server")})
			return
		}
		before, _ := json.Marshal(workspaceDraft{Inbounds: []inboundDraft{{Port: number, Preset: "reality"}}})
		if protocol == "udp" {
			before, _ = json.Marshal(workspaceDraft{Inbounds: []inboundDraft{{Port: number, Preset: "hy2"}}})
		}
		report, err = saveFirewallReport(context.WithoutCancel(r.Context()), state, before, nil, doc.Revision)
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("防火墙结果保存失败，请重新读取核对", "Failed to save firewall result; reload and check")})
			return
		}
		respond(w, 200, report)
	})
	mux.HandleFunc("GET /api/runtime/firewall", func(w http.ResponseWriter, r *http.Request) {
		doc, err := state.ReadDocument(r.Context(), "firewall-status")
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取防火墙状态失败", "Failed to read firewall status")})
			return
		}
		respond(w, 200, doc)
	})
	mux.HandleFunc("POST /api/runtime/firewall/retry", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		var request struct {
			InboundID string `json:"inboundId"`
		}
		if !readJSON(w, r, &request, 256) {
			return
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		doc, err := state.ReadDocument(r.Context(), "workspace")
		var draft workspaceDraft
		if err != nil || json.Unmarshal(doc.Data, &draft) != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取入站草稿失败", "Failed to read inbound draft")})
			return
		}
		for _, inbound := range draft.Inbounds {
			if inbound.ID != request.InboundID {
				continue
			}
			single, _ := json.Marshal(workspaceDraft{Inbounds: []inboundDraft{inbound}})
			report, err := saveFirewallReport(context.WithoutCancel(r.Context()), state, nil, single, doc.Revision)
			if err != nil {
				respond(w, 500, map[string]string{"error": i18n.T("防火墙状态保存失败，请重新核对", "Failed to save firewall status; check again")})
				return
			}
			respond(w, 200, report)
			return
		}
		respond(w, 404, map[string]string{"error": i18n.T("入站不存在", "Inbound does not exist")})
	})
}
