package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"independent-panel/internal/backup"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Telegram通知（参照OUI的Telegram设置）：登录提醒、每日报告、CPU超阈值提醒、客户端上线/离线，
// 以及随每日报告发送加密业务备份。消息按HTML排版，用户可控的文字一律转义；管理菜单见telegram_bot.go。
const telegramDocument = "telegram"

var (
	telegramAPIBase = "https://api.telegram.org"
	telegramHTTP    = &http.Client{Timeout: 30 * time.Second}
	telegramToken   = regexp.MustCompile(`^[0-9]{5,16}:[A-Za-z0-9_-]{30,64}$`)
	telegramChat    = regexp.MustCompile(`^-?[0-9]{1,20}$`)
	telegramClock   = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	telegramMu      sync.Mutex
)

type telegramSettings struct {
	Enabled        bool   `json:"enabled"`
	Token          string `json:"token"`
	ChatIDs        string `json:"chatIds"`
	ReportTime     string `json:"reportTime"`
	NotifyLogin    bool   `json:"notifyLogin"`
	CPUThreshold   int    `json:"cpuThreshold"`
	BackupEnabled  bool   `json:"backupEnabled"`
	BackupPassword string `json:"backupPassword"`
}

// telegramView是返回网页的设置：机器人令牌与备份口令只写不读，只显示是否已设置。
type telegramView struct {
	Enabled           bool   `json:"enabled"`
	ChatIDs           string `json:"chatIds"`
	ReportTime        string `json:"reportTime"`
	NotifyLogin       bool   `json:"notifyLogin"`
	CPUThreshold      int    `json:"cpuThreshold"`
	BackupEnabled     bool   `json:"backupEnabled"`
	TokenSet          bool   `json:"tokenSet"`
	BackupPasswordSet bool   `json:"backupPasswordSet"`
}

func (s telegramSettings) view() telegramView {
	return telegramView{Enabled: s.Enabled, ChatIDs: s.ChatIDs, ReportTime: s.ReportTime, NotifyLogin: s.NotifyLogin, CPUThreshold: s.CPUThreshold, BackupEnabled: s.BackupEnabled, TokenSet: s.Token != "", BackupPasswordSet: s.BackupPassword != ""}
}

func (s telegramSettings) chats() []string {
	chats := []string{}
	for _, item := range strings.Split(s.ChatIDs, ",") {
		if item = strings.TrimSpace(item); item != "" {
			chats = append(chats, item)
		}
	}
	return chats
}

func readTelegram(ctx context.Context, state *store.Store) (telegramSettings, int64, error) {
	doc, err := state.ReadDocument(ctx, telegramDocument)
	if err != nil {
		return telegramSettings{}, 0, err
	}
	var s telegramSettings
	if string(doc.Data) != "null" && len(doc.Data) > 0 && json.Unmarshal(doc.Data, &s) != nil {
		return telegramSettings{}, 0, i18n.New("Telegram设置损坏", "Telegram settings are corrupted")
	}
	return s, doc.Revision, nil
}

// 令牌或备份口令留空表示保留已保存的值。
func validateTelegram(next, current telegramSettings) (telegramSettings, error) {
	if next.Token == "" {
		next.Token = current.Token
	} else if !telegramToken.MatchString(next.Token) {
		return next, i18n.New("Bot Token格式无效（应为“数字:字母数字串”，从@BotFather获取）", "Invalid bot token (it looks like digits:letters, from @BotFather)")
	}
	if next.BackupPassword == "" {
		next.BackupPassword = current.BackupPassword
	} else if err := backup.ValidatePassword(next.BackupPassword); err != nil {
		return next, err
	}
	chats := next.chats()
	if len(chats) > 10 {
		return next, i18n.New("管理员Chat ID最多10个", "At most 10 admin chat IDs")
	}
	for _, chat := range chats {
		if !telegramChat.MatchString(chat) {
			return next, i18n.New("Chat ID只能是数字（群组以-开头），多个用英文逗号分隔", "Chat IDs must be numbers (groups start with -), separated by commas")
		}
	}
	next.ChatIDs = strings.Join(chats, ",")
	if next.ReportTime != "" && !telegramClock.MatchString(next.ReportTime) {
		return next, i18n.New("每日报告时间格式应为HH:MM，留空表示不发送", "Daily report time must be HH:MM, or empty to disable")
	}
	if next.CPUThreshold < 0 || next.CPUThreshold > 100 {
		return next, i18n.New("CPU提醒阈值须为0至100（0表示关闭）", "CPU alert threshold must be 0–100 (0 turns it off)")
	}
	if next.Enabled && (next.Token == "" || len(chats) == 0) {
		return next, i18n.New("启用机器人须填写Bot Token和至少一个Chat ID", "Enabling the bot needs a bot token and at least one chat ID")
	}
	if next.BackupEnabled && (next.BackupPassword == "" || next.ReportTime == "") {
		return next, i18n.New("发送备份须设置备份加密口令和每日报告时间", "Sending backups needs a backup password and a daily report time")
	}
	return next, nil
}

// 请求地址含令牌，网络错误原文可能带出令牌，只返回固定说明。
func telegramCall(ctx context.Context, token, method, contentType string, body io.Reader) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPIBase+"/bot"+token+"/"+method, body)
	if err != nil {
		return i18n.New("Telegram请求无效", "Invalid Telegram request")
	}
	request.Header.Set("Content-Type", contentType)
	response, err := telegramHTTP.Do(request)
	if err != nil {
		return i18n.New("无法连接Telegram，请确认服务器能访问api.telegram.org", "Cannot reach Telegram; check that the server can reach api.telegram.org")
	}
	defer response.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result)
	if !result.OK {
		description := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, strings.ReplaceAll(result.Description, token, "***"))
		if len(description) > 200 {
			description = description[:200]
		}
		return i18n.Errorf("Telegram拒绝了请求（%d）%s", "Telegram rejected the request (%d) %s", response.StatusCode, description)
	}
	return nil
}

func telegramMessage(ctx context.Context, token, chat, text string) error {
	data, _ := json.Marshal(map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true})
	return telegramCall(ctx, token, "sendMessage", "application/json", bytes.NewReader(data))
}

func telegramDocumentSend(ctx context.Context, token, chat, name string, file []byte, caption string, keyboard [][]tgButton) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("chat_id", chat)
	_ = form.WriteField("caption", caption)
	_ = form.WriteField("parse_mode", "HTML")
	if len(keyboard) > 0 {
		markup, _ := json.Marshal(map[string]any{"inline_keyboard": keyboard})
		_ = form.WriteField("reply_markup", string(markup))
	}
	part, err := form.CreateFormFile("document", name)
	if err != nil {
		return err
	}
	_, _ = part.Write(file)
	_ = form.Close()
	return telegramCall(ctx, token, "sendDocument", form.FormDataContentType(), &body)
}

// telegramBroadcast发给全部管理员Chat；只在机器人启用时发送，返回成功条数与第一个错误。
func telegramBroadcast(ctx context.Context, s telegramSettings, text string) (int, error) {
	sent := 0
	var first error
	for _, chat := range s.chats() {
		if err := telegramMessage(ctx, s.Token, chat, text); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		sent++
	}
	return sent, first
}

func telegramLang() string {
	if os.Getenv("O_UI_DEFAULT_LANG") == "en" {
		return "en"
	}
	return "zh"
}

// notifyLogin在后台发送登录提醒，不拖慢登录；用户名可能来自攻击者，去掉控制字符并截断。
func notifyLogin(state *store.Store, username, remote string, err error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, _, readErr := readTelegram(ctx, state)
		if readErr != nil || !s.Enabled || !s.NotifyLogin {
			return
		}
		lang := telegramLang()
		name := []rune(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, username))
		if len(name) > 64 {
			name = name[:64]
		}
		ip, _, splitErr := net.SplitHostPort(remote)
		if splitErr != nil {
			ip = remote
		}
		icon, result := "✅", i18n.Pick(lang, "成功", "succeeded")
		switch {
		case errors.Is(err, store.ErrLimited):
			icon, result = "⛔", i18n.Pick(lang, "失败次数过多，已限速", "too many failures, rate limited")
		case err != nil:
			icon, result = "❌", i18n.Pick(lang, "失败", "failed")
		}
		text := fmt.Sprintf(i18n.Pick(lang, "%s <b>o-ui 登录%s</b>\n👤 用户名：<code>%s</code>\n🌐 IP：<code>%s</code>\n⏰ 时间：%s", "%s <b>o-ui sign-in %s</b>\n👤 Username: <code>%s</code>\n🌐 IP: <code>%s</code>\n⏰ Time: %s"),
			icon, result, html.EscapeString(string(name)), html.EscapeString(ip), time.Now().Format("2006-01-02 15:04:05"))
		_, _ = telegramBroadcast(ctx, s, text)
	}()
}

func percent(value *float64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatFloat(*value, 'f', 0, 64) + "%"
}

// telegramReport汇总Xray状态、系统资源、业务流量和客户端到期情况；调用方须持有rt.mu。
func (rt *Runtime) telegramReport(ctx context.Context, now time.Time) string {
	lang := telegramLang()
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	lines := []string{say("📊 <b>o-ui 每日报告</b>", "📊 <b>o-ui daily report</b>"), say("⏰ 时间：", "⏰ Time: ") + now.Format("2006-01-02 15:04")}
	snapshot := rt.monitor.Snapshot()
	if snapshot.Host != nil && snapshot.Host.Name != "" {
		lines = append(lines, say("💻 主机：", "💻 Host: ")+html.EscapeString(snapshot.Host.Name))
	}
	status := rt.engine.Status()
	if status.Running {
		lines = append(lines, say("📡 Xray：🟢 运行中 ", "📡 Xray: 🟢 running ")+html.EscapeString(status.Version))
	} else {
		lines = append(lines, say("📡 Xray：🔴 已停止", "📡 Xray: 🔴 stopped"))
	}
	memory, disk := (*float64)(nil), (*float64)(nil)
	if snapshot.Memory != nil {
		memory = snapshot.Memory.Percent
	}
	if snapshot.Disk != nil {
		disk = snapshot.Disk.Percent
	}
	lines = append(lines, fmt.Sprintf(say("🖥 CPU：%s · 📋 内存：%s · 💾 磁盘：%s", "🖥 CPU: %s · 📋 Memory: %s · 💾 Disk: %s"), percent(snapshot.CPU), percent(memory), percent(disk)))
	_ = rt.checkpointCurrent(ctx)
	ledger, _, err := rt.ledger(ctx)
	workspace, workspaceErr := rt.state.ReadDocument(ctx, "workspace")
	var draft workspaceDraft
	if err != nil || workspaceErr != nil || json.Unmarshal(workspace.Data, &draft) != nil {
		return strings.Join(append(lines, say("⚠️ 流量与客户端信息暂不可用", "⚠️ Traffic and client details are unavailable")), "\n")
	}
	var upload, download uint64
	for _, count := range ledger.Totals {
		upload += count.Uplink
		download += count.Downlink
	}
	lines = append(lines, fmt.Sprintf(say("🚦 业务流量累计：%s（🔼 %s · 🔽 %s）", "🚦 Proxy traffic total: %s (🔼 %s · 🔽 %s)"), humanBytes(upload+download), humanBytes(upload), humanBytes(download)))
	enabled, limited, soon := 0, 0, []string{}
	for _, client := range draft.Clients {
		if !client.Enabled {
			continue
		}
		enabled++
		limit := clientLimit{Quota: quotaBytes(client.QuotaGB), ExpiresAt: client.ExpiresAt}
		if blocked(limit, ledger.Totals[client.ID], now) {
			limited++
		} else if client.ExpiresAt != nil && time.UnixMilli(*client.ExpiresAt).Before(now.Add(72*time.Hour)) {
			soon = append(soon, fmt.Sprintf("%s（%s）", html.EscapeString(client.Name), time.UnixMilli(*client.ExpiresAt).Format("2006-01-02")))
		}
	}
	sort.Strings(soon)
	lines = append(lines, fmt.Sprintf(say("👥 客户端：共 %d 个 · 启用 %d 个 · 到期或超额 %d 个", "👥 Clients: %d total · %d enabled · %d expired or over quota"), len(draft.Clients), enabled, limited))
	if len(soon) > 0 {
		lines = append(lines, say("⏳ 3 天内到期：", "⏳ Expiring within 3 days: ")+strings.Join(soon, ", "))
	}
	return strings.Join(lines, "\n")
}

// RunTelegram按设置定时发送每日报告/备份与CPU提醒，直到ctx结束；每30秒检查一次。
func (rt *Runtime) RunTelegram(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	lastReport, lastCPU, sessions := "", time.Time{}, map[string]onlineSession{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s, _, err := readTelegram(ctx, rt.state)
			if err != nil || !s.Enabled {
				clear(sessions)
				continue
			}
			rt.notifyOnline(ctx, s, sessions, now)
			if s.CPUThreshold > 0 && now.Sub(lastCPU) >= 30*time.Minute {
				if cpu := rt.monitor.Snapshot().CPU; cpu != nil && *cpu >= float64(s.CPUThreshold) {
					lastCPU = now
					lang := telegramLang()
					_, _ = telegramBroadcast(ctx, s, fmt.Sprintf(i18n.Pick(lang, "🔥 <b>o-ui CPU 提醒</b>\n🖥 当前 CPU：%s\n⚠️ 阈值：%d%%\n⏰ 30 分钟内不再重复提醒", "🔥 <b>o-ui CPU alert</b>\n🖥 CPU now: %s\n⚠️ Threshold: %d%%\n⏰ No repeat within 30 minutes"), percent(cpu), s.CPUThreshold))
				}
			}
			if s.ReportTime != "" && now.Format("15:04") == s.ReportTime && lastReport != now.Format("2006-01-02") {
				lastReport = now.Format("2006-01-02")
				rt.sendTelegramReport(ctx, s, now)
			}
		}
	}
}

func (rt *Runtime) sendTelegramReport(ctx context.Context, s telegramSettings, now time.Time) {
	rt.mu.Lock()
	report := rt.telegramReport(ctx, now)
	rt.mu.Unlock()
	_, _ = telegramBroadcast(ctx, s, report)
	if !s.BackupEnabled || s.BackupPassword == "" {
		return
	}
	// 与网页备份、核心操作串行；正在忙时跳过本次，不排队占用内存。
	if !rt.backupMu.TryLock() {
		return
	}
	rt.mu.Lock()
	name, file, err := rt.exportBackup(ctx, s.BackupPassword)
	rt.mu.Unlock()
	rt.backupMu.Unlock()
	lang := telegramLang()
	if err != nil {
		_, _ = telegramBroadcast(ctx, s, i18n.Pick(lang, "❌ <b>o-ui 备份失败</b>\n", "❌ <b>o-ui backup failed</b>\n")+html.EscapeString(i18n.Translate(err.Error(), lang)))
		return
	}
	for _, chat := range s.chats() {
		if err := telegramDocumentSend(ctx, s.Token, chat, name, file, backupCaption(now, lang), nil); err != nil {
			_ = telegramMessage(ctx, s.Token, chat, i18n.Pick(lang, "❌ 备份文件发送失败：", "❌ Failed to send the backup file: ")+html.EscapeString(i18n.Translate(err.Error(), lang)))
		}
	}
}

func backupCaption(at time.Time, lang string) string {
	return i18n.Pick(lang, "🗄 <b>o-ui 加密备份</b>\n⏰ 备份时间：", "🗄 <b>o-ui encrypted backup</b>\n⏰ Backup time: ") + at.Format("2006-01-02 15:04:05") +
		i18n.Pick(lang, "\n🔑 用设置的备份口令在面板“备份和恢复”中恢复", "\n🔑 Restore it under Backup and restore with the configured password")
}

func registerTelegram(mux *http.ServeMux, rt *Runtime, state *store.Store) {
	mux.HandleFunc("GET /api/panel/telegram", func(w http.ResponseWriter, r *http.Request) {
		s, revision, err := readTelegram(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]any{"revision": revision, "data": s.view()})
	})
	mux.HandleFunc("PUT /api/panel/telegram", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Revision int64            `json:"revision"`
			Data     telegramSettings `json:"data"`
		}
		if !readJSON(w, r, &request, 4096) {
			return
		}
		telegramMu.Lock()
		defer telegramMu.Unlock()
		current, _, err := readTelegram(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		next, err := validateTelegram(request.Data, current)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		data, _ := json.Marshal(next)
		doc, err := state.SaveDocument(r.Context(), telegramDocument, request.Revision, data)
		if err != nil {
			code, message := 500, i18n.T("保存Telegram设置失败", "Failed to save Telegram settings")
			if errors.Is(err, store.ErrConflict) {
				code, message = 409, err.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		respond(w, 200, map[string]any{"revision": doc.Revision, "data": next.view()})
	})
	// 测试按已保存的令牌与Chat ID发送，即使机器人未启用；不发送备份。
	mux.HandleFunc("POST /api/panel/telegram/test", func(w http.ResponseWriter, r *http.Request) {
		s, _, err := readTelegram(r.Context(), state)
		if err != nil || s.Token == "" || len(s.chats()) == 0 {
			respond(w, 409, map[string]string{"error": i18n.T("请先保存Bot Token和Chat ID", "Save the bot token and chat ID first")})
			return
		}
		text := i18n.Pick(telegramLang(), "✅ <b>o-ui 测试消息</b>\n🤖 机器人配置正确。", "✅ <b>o-ui test message</b>\n🤖 The bot is configured correctly.")
		if rt != nil && r.URL.Query().Get("report") == "1" {
			rt.mu.Lock()
			text = rt.telegramReport(r.Context(), time.Now())
			rt.mu.Unlock()
		}
		sent, err := telegramBroadcast(r.Context(), s, text)
		if err != nil && sent == 0 {
			respond(w, 502, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]any{"sent": sent, "total": len(s.chats())})
	})
	registerTelegramNotify(mux, state)
}
