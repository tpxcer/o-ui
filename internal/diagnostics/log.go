// Package diagnostics只保存固定中文摘要，不接受任意原文作为日志消息。
package diagnostics

import (
	"fmt"
	"independent-panel/internal/i18n"
	"sync"
	"time"
)

const Limit = 256

type Entry struct {
	Sequence uint64    `json:"sequence"`
	At       time.Time `json:"at"`
	Source   string    `json:"source"`
	Level    string    `json:"level"`
	Code     string    `json:"code"`
	Message  string    `json:"message"`
}
type Snapshot struct {
	Since   time.Time `json:"since"`
	Limit   int       `json:"limit"`
	Dropped uint64    `json:"dropped"`
	Entries []Entry   `json:"entries"`
}
type Log struct {
	mu      sync.Mutex
	since   time.Time
	next    uint64
	dropped uint64
	entries []Entry
}
type definition struct{ source, level, text string }

var definitions = map[string]definition{
	"panel.started":            {"panel", "info", i18n.T("o-ui 本轮已启动；诊断仅在内存保留，仅上次退出时核心在运行才自动恢复上次应用", "o-ui started; diagnostics stay in memory only, and the last applied config auto-resumes only if the core was running at last exit")},
	"xray.autoresume.ok":       {"xray", "info", i18n.T("已按上次退出时运行的已应用配置自动恢复；未使用较新草稿", "Auto-resumed the configuration running at last exit; newer drafts were not used")},
	"xray.autoresume.failed":   {"xray", "error", i18n.T("面板启动时自动恢复上次应用失败，核心保持停止；请检查后手动处理", "Auto-resume at startup failed; the core stays stopped, please check and handle it manually")},
	"xray.preflight":           {"xray", "info", i18n.T("开始官方核心预检；不会替换当前运行配置", "Official core preflight started; the running configuration is not replaced")},
	"xray.preflight.ok":        {"xray", "info", i18n.T("官方核心预检通过", "Official core preflight passed")},
	"xray.preflight.failed":    {"xray", "error", i18n.T("官方核心预检失败；当前运行配置未改变", "Official core preflight failed; running configuration unchanged")},
	"xray.preflight.cancelled": {"xray", "warning", i18n.T("官方预检超时或取消；当前运行配置未改变", "Official preflight timed out or was cancelled; running configuration unchanged")},
	"xray.running":             {"xray", "info", i18n.T("本次独立核心已启动并通过初始存活检查", "Core started and passed the initial liveness check")},
	"xray.start.failed":        {"xray", "error", i18n.T("独立核心启动失败；检查下方诊断摘要", "Core failed to start; see the diagnostics below")},
	"xray.stopped":             {"xray", "info", i18n.T("本面板自有核心已停止", "This panel's core has stopped")},
	"xray.unexpected":          {"xray", "error", i18n.T("自有核心或监督进程异常退出；未自动重启，请检查后重新预检", "The core or its supervisor exited unexpectedly; not restarted automatically, check and run preflight again")},
	"xray.rollback.ok":         {"xray", "warning", i18n.T("新核心启动失败，已重新启动原版本和配置", "New core failed to start; the previous version and configuration were restarted")},
	"xray.rollback.failed":     {"xray", "error", i18n.T("新核心启动失败，原配置恢复也失败；核心已停止", "New core failed to start and restoring the previous configuration also failed; the core is stopped")},
	"xray.applied":             {"xray", "info", i18n.T("配置应用及运行历史保存成功", "Configuration applied and run history saved")},
	"xray.empty":               {"xray", "info", i18n.T("已应用空有效配置；旧监听已停止，不启动新核心", "Applied an empty valid configuration; old listeners stopped, no core started")},
	"xray.limits":              {"xray", "warning", i18n.T("已应用到期或用尽过滤；同核心其他连接可能短暂重连", "Applied expiry/quota filtering; other connections on this core may briefly reconnect")},
	"xray.guard":               {"xray", "error", i18n.T("采样、持久化或限制执行失败，独立核心已安全停止；未知尾流不能补算", "Sampling, persistence or limit enforcement failed; the core was stopped safely and unmeasured traffic cannot be recovered")},
	"core.port_busy":           {"xray", "error", i18n.T("核心报告地址或端口已被占用；未显示地址原文", "Core reports the address or port is in use (address hidden)")},
	"core.permission":          {"xray", "error", i18n.T("核心报告权限不足；检查运行目录和证书权限", "Core reports insufficient permissions; check run directory and certificate permissions")},
	"core.file_missing":        {"xray", "error", i18n.T("核心报告所需文件不存在；文件路径已隐藏", "Core reports a required file is missing (path hidden)")},
	"core.certificate":         {"xray", "error", i18n.T("核心报告TLS/证书错误；检查证书配对、有效期及服务器名称", "Core reports a TLS/certificate error; check key pairing, validity and server name")},
	"core.config":              {"xray", "error", i18n.T("核心报告配置解析或字段错误；检查已保存的入站及六类设置", "Core reports a config parse or field error; check saved inbounds and the six settings categories")},
	"core.dns":                 {"xray", "warning", i18n.T("核心报告名称解析失败；域名原文已隐藏", "Core reports a name resolution failure (domain hidden)")},
	"core.network":             {"xray", "warning", i18n.T("核心报告连接拒绝、不可达或超时；目标地址已隐藏", "Core reports connection refused, unreachable or timeout (target hidden)")},
	"core.hidden":              {"xray", "warning", i18n.T("核心产生其他警告或错误；未识别原文已隐藏，不能据此确定根因", "Core produced another warning or error; unrecognized text hidden, root cause not determinable from it")},
	"core.long_line":           {"xray", "warning", i18n.T("核心产生超长诊断行，整行已丢弃", "Core produced an overlong diagnostic line; it was dropped")},
}

func New() *Log { return &Log{since: time.Now().UTC(), entries: []Entry{}} }

func (l *Log) Add(code string) {
	if l == nil {
		return
	}
	d, ok := definitions[code]
	if !ok {
		return
	}
	l.append(code, d)
}

func (l *Log) append(code string, d definition) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UTC()
	if n := len(l.entries); n > 0 && l.entries[n-1].Code == code && l.entries[n-1].Level == d.level && l.entries[n-1].Message == d.text && now.Sub(l.entries[n-1].At) < time.Second {
		return
	}
	l.next++
	entry := Entry{l.next, now, d.source, d.level, code, d.text}
	if len(l.entries) == Limit {
		copy(l.entries, l.entries[1:])
		l.entries[len(l.entries)-1] = entry
		l.dropped++
	} else {
		l.entries = append(l.entries, entry)
	}
}

var operations = map[string]definition{
	"POST /api/auth/setup":                {"panel", "", i18n.T("初始化管理员", "Set up administrator")},
	"POST /api/auth/login":                {"panel", "", i18n.T("管理员登录", "Administrator sign-in")},
	"POST /api/auth/logout":               {"panel", "", i18n.T("退出登录", "Sign out")},
	"POST /api/auth/password":             {"panel", "", i18n.T("修改管理员密码", "Change administrator password")},
	"PUT /api/workspace":                  {"panel", "", i18n.T("保存入站与客户端草稿", "Save inbound and client draft")},
	"PUT /api/settings":                   {"panel", "", i18n.T("保存Xray设置草稿", "Save Xray settings draft")},
	"PUT /api/panel/tls":                  {"panel", "", i18n.T("保存面板HTTPS设置", "Save panel HTTPS settings")},
	"POST /api/core/install":              {"panel", "", i18n.T("安装并选择官方核心", "Install and select official core")},
	"POST /api/runtime/preflight":         {"xray", "", i18n.T("请求官方预检", "Request official preflight")},
	"POST /api/runtime/apply":             {"xray", "", i18n.T("请求应用已保存配置", "Request applying saved configuration")},
	"POST /api/runtime/stop":              {"xray", "", i18n.T("请求停止自有核心", "Request stopping the core")},
	"POST /api/runtime/restart-preflight": {"xray", "", i18n.T("请求预检当前运行配置", "Request preflight of running configuration")},
	"POST /api/runtime/restart":           {"xray", "", i18n.T("请求重启当前运行配置", "Request restart of running configuration")},
	"POST /api/runtime/resume-preflight":  {"xray", "", i18n.T("请求预检上次应用历史", "Request preflight of last applied history")},
	"POST /api/runtime/resume":            {"xray", "", i18n.T("请求恢复上次应用历史", "Request resume of last applied history")},
	"POST /api/backup/export":             {"panel", "", i18n.T("导出加密业务备份", "Export encrypted backup")},
	"POST /api/backup/preview":            {"panel", "", i18n.T("校验业务恢复预览", "Verify restore preview")},
	"POST /api/backup/restore":            {"panel", "", i18n.T("确认恢复业务备份", "Confirm restoring backup")},
}

func (l *Log) Operation(key string, status int) {
	if l == nil {
		return
	}
	d, ok := operations[key]
	if !ok || status < 100 || status > 599 {
		return
	}
	result, suffix := i18n.T("成功", "succeeded"), ".ok"
	d.level = "info"
	if status >= 400 {
		result, suffix, d.level = i18n.T("未完成", "not completed"), ".rejected", "warning"
	}
	if status >= 500 {
		d.level = "error"
	}
	d.text = i18n.T(fmt.Sprintf("%s：%s（HTTP %d）", d.text, result, status), fmt.Sprintf("%s: %s (HTTP %d)", i18n.Translate(d.text, "en"), i18n.Translate(result, "en"), status))
	l.append(key+suffix, d)
}

func (l *Log) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Snapshot{l.since, Limit, l.dropped, append([]Entry{}, l.entries...)}
}
