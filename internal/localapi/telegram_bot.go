package localapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"independent-panel/internal/config"
	"independent-panel/internal/i18n"
	"io"
	"math/big"
	mathrand "math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Telegram机器人交互（参照OUI的管理菜单）：长轮询getUpdates，只响应设置里的管理员Chat ID。
// /start显示按钮菜单；查询类按钮只读，启停客户端、一键创建入站、重启Xray、升级面板会保存并生效。

type tgChat struct {
	ID int64 `json:"id"`
}

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Chat tgChat `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
	Callback *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message *struct {
			Chat tgChat `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

type tgButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// telegramRequest调用Bot API并解析result；与telegramCall一样不回传含令牌的网络错误原文。
func telegramRequest(ctx context.Context, token, method string, payload any, result any) error {
	data, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPIBase+"/bot"+token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return i18n.New("Telegram请求无效", "Invalid Telegram request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := telegramHTTP.Do(request)
	if err != nil {
		return i18n.New("无法连接Telegram，请确认服务器能访问api.telegram.org", "Cannot reach Telegram; check that the server can reach api.telegram.org")
	}
	defer response.Body.Close()
	var body struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&body) != nil || !body.OK {
		return i18n.Errorf("Telegram拒绝了请求（%d）", "Telegram rejected the request (%d)", response.StatusCode)
	}
	if result != nil {
		return json.Unmarshal(body.Result, result)
	}
	return nil
}

func telegramReply(ctx context.Context, token, chat, text string, keyboard [][]tgButton) {
	payload := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if len(keyboard) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": keyboard}
	}
	_ = telegramRequest(ctx, token, "sendMessage", payload, nil)
}

// RunTelegramBot在机器人启用时长轮询消息，令牌改变时重新开始；启动时跳过积压的旧消息，避免重放旧操作。
func (rt *Runtime) RunTelegramBot(ctx context.Context) {
	var offset int64
	token := ""
	wait := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	for ctx.Err() == nil {
		s, _, err := readTelegram(ctx, rt.state)
		if err != nil || !s.Enabled || s.Token == "" {
			token = ""
			if !wait(30 * time.Second) {
				return
			}
			continue
		}
		if s.Token != token {
			var backlog []tgUpdate
			if telegramRequest(ctx, s.Token, "getUpdates", map[string]any{"offset": -1, "timeout": 0}, &backlog) != nil {
				if !wait(15 * time.Second) {
					return
				}
				continue
			}
			offset = 0
			if len(backlog) > 0 {
				offset = backlog[len(backlog)-1].UpdateID + 1
			}
			token = s.Token
			lang := telegramLang()
			_ = telegramRequest(ctx, token, "setMyCommands", map[string]any{"commands": []map[string]string{
				{"command": "start", "description": i18n.Pick(lang, "打开管理菜单", "Open the admin menu")},
				{"command": "status", "description": i18n.Pick(lang, "服务器与 Xray 状态", "Server and Xray status")},
				{"command": "id", "description": i18n.Pick(lang, "查看本聊天的 Chat ID", "Show this chat's ID")},
			}}, nil)
		}
		var updates []tgUpdate
		poll, cancel := context.WithTimeout(ctx, 40*time.Second)
		err = telegramRequest(poll, token, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		cancel()
		if err != nil {
			if !wait(10 * time.Second) {
				return
			}
			continue
		}
		for _, update := range updates {
			offset = update.UpdateID + 1
			rt.handleTelegram(ctx, s, update)
		}
	}
}

func (rt *Runtime) handleTelegram(ctx context.Context, s telegramSettings, update tgUpdate) {
	lang := telegramLang()
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	var chat, text, data string
	switch {
	case update.Message != nil:
		chat, text = strconv.FormatInt(update.Message.Chat.ID, 10), strings.TrimSpace(update.Message.Text)
	case update.Callback != nil && update.Callback.Message != nil:
		chat, data = strconv.FormatInt(update.Callback.Message.Chat.ID, 10), update.Callback.Data
		_ = telegramRequest(ctx, s.Token, "answerCallbackQuery", map[string]any{"callback_query_id": update.Callback.ID}, nil)
	default:
		return
	}
	command := ""
	if fields := strings.Fields(text); len(fields) > 0 {
		command = strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	}
	if command == "/id" {
		telegramReply(ctx, s.Token, chat, say("🆔 本聊天的 Chat ID：", "🆔 This chat's ID: ")+"<code>"+chat+"</code>", nil)
		return
	}
	if !slices.Contains(s.chats(), chat) {
		if command == "/start" {
			telegramReply(ctx, s.Token, chat, say("⛔ 未授权。请把 Chat ID ", "⛔ Not authorized. Add Chat ID ")+"<code>"+chat+"</code>"+say(" 加到面板“面板设置 → Telegram”的管理员 Chat ID 里。", " to the admin chat IDs under Panel settings → Telegram."), nil)
		}
		return
	}
	switch {
	case command == "/start" || command == "/help" || command == "/menu" || data == "m":
		telegramReply(ctx, s.Token, chat, say("🤖 <b>o-ui 管理菜单</b>\n👇 请选择：", "🤖 <b>o-ui admin menu</b>\n👇 Choose an action:"), rt.telegramMenu(lang))
	case command == "/status" || data == "st":
		telegramReply(ctx, s.Token, chat, rt.telegramStatus(ctx, lang), backButton(lang))
	case data != "":
		rt.telegramAction(ctx, s, chat, data, lang)
	}
}

func (rt *Runtime) telegramMenu(lang string) [][]tgButton {
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	return [][]tgButton{
		{{say("📊 服务器状态", "📊 Status"), "st"}, {say("📈 流量排行", "📈 Traffic ranking"), "rk"}},
		{{say("🟢 在线客户端", "🟢 Online clients"), "on"}, {say("⏳ 即将到期/耗尽", "⏳ Expiring / low quota"), "ds"}},
		{{say("📋 入站列表", "📋 Inbounds"), "ib"}, {say("👤 客户端", "👤 Clients"), "cl:0"}},
		{{say("⚡ 一键创建入站", "⚡ Quick create inbound"), "qc"}, {say("💾 立即备份", "💾 Back up now"), "bk"}},
		{{say("🔄 重启 Xray", "🔄 Restart Xray"), "rx"}, {say("⬆️ 检测更新", "⬆️ Check for updates"), "up"}},
	}
}

func backButton(lang string) [][]tgButton {
	return [][]tgButton{{{i18n.Pick(lang, "🏠 返回菜单", "🏠 Menu"), "m"}}}
}

// telegramData读取入站草稿与流量账本；只读，不需要rt.mu。
func (rt *Runtime) telegramData(ctx context.Context) (workspaceDraft, int64, map[string]trafficSnapshot, error) {
	doc, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		return workspaceDraft{}, 0, nil, err
	}
	var draft workspaceDraft
	if string(doc.Data) != "null" && len(doc.Data) > 0 && json.Unmarshal(doc.Data, &draft) != nil {
		return draft, 0, nil, i18n.New("入站草稿损坏", "Inbound draft is corrupted")
	}
	ledger, _, err := rt.ledger(ctx)
	if err != nil {
		return draft, doc.Revision, nil, err
	}
	running := rt.engine.Status().Running
	traffic := map[string]trafficSnapshot{}
	for _, client := range draft.Clients {
		count := ledger.Totals[client.ID]
		seen := ledger.LastOnline[client.ID]
		traffic[client.ID] = trafficSnapshot{up: count.Uplink, down: count.Downlink, lastOnline: seen, online: running && !seen.IsZero() && time.Since(seen) <= 2*time.Minute}
	}
	return draft, doc.Revision, traffic, nil
}

type trafficSnapshot struct {
	up, down   uint64
	lastOnline time.Time
	online     bool
}

// humanBytes与网页一致按十进制自动单位。
func humanBytes(n uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value, unit := float64(n), 0
	for value >= 1000 && unit < len(units)-1 {
		value /= 1000
		unit++
	}
	if unit == 0 {
		return strconv.FormatUint(n, 10) + " B"
	}
	text := strconv.FormatFloat(value, 'f', 2, 64)
	if value >= 100 {
		text = strconv.FormatFloat(value, 'f', 0, 64)
	} else if value >= 10 {
		text = strconv.FormatFloat(value, 'f', 1, 64)
	}
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return text + " " + units[unit]
}

func presetLabel(preset string) string {
	return map[string]string{"hy2": "HY2", "reality": "Reality", "xhttp-tls": "XHTTP-TLS", "xhttp-reality": "XHTTP-Reality"}[preset]
}

func (rt *Runtime) telegramStatus(ctx context.Context, lang string) string {
	rt.mu.Lock()
	report := rt.telegramReport(ctx, time.Now())
	rt.mu.Unlock()
	lines := []string{strings.Replace(report, i18n.Pick(lang, "o-ui 每日报告", "o-ui daily report"), i18n.Pick(lang, "o-ui 服务器状态", "o-ui server status"), 1)}
	if node, _ := rt.business.network(); node != nil && node.Upload != nil && node.Download != nil {
		lines = append(lines, fmt.Sprintf(i18n.Pick(lang, "⚡ 节点网速：🔼 %s/s · 🔽 %s/s", "⚡ Node speed: 🔼 %s/s · 🔽 %s/s"), humanBytes(uint64(*node.Upload)), humanBytes(uint64(*node.Download))))
	}
	if nic := rt.monitor.Snapshot().Network; nic != nil && nic.Upload != nil && nic.Download != nil {
		lines = append(lines, fmt.Sprintf(i18n.Pick(lang, "📶 网卡总速度：🔼 %s/s · 🔽 %s/s", "📶 NIC speed: 🔼 %s/s · 🔽 %s/s"), humanBytes(uint64(*nic.Upload)), humanBytes(uint64(*nic.Download))))
	}
	if _, _, traffic, err := rt.telegramData(ctx); err == nil {
		online := 0
		for _, t := range traffic {
			if t.online {
				online++
			}
		}
		lines = append(lines, fmt.Sprintf(i18n.Pick(lang, "🟢 在线客户端：%d 个", "🟢 Online clients: %d"), online))
	}
	return strings.Join(lines, "\n")
}

func (rt *Runtime) telegramAction(ctx context.Context, s telegramSettings, chat, data, lang string) {
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	reply := func(text string, keyboard [][]tgButton) { telegramReply(ctx, s.Token, chat, text, keyboard) }
	failed := func(prefix string, err error) {
		reply("❌ "+prefix+html.EscapeString(i18n.Translate(err.Error(), lang)), backButton(lang))
	}
	action, argument, _ := strings.Cut(data, ":")
	draft, revision, traffic, err := rt.telegramData(ctx)
	if err != nil && action != "bk" && action != "rx" && action != "rxc" && action != "up" && action != "upc" {
		failed(say("读取数据失败：", "Failed to read data: "), err)
		return
	}
	inboundName := map[string]string{}
	for _, in := range draft.Inbounds {
		inboundName[in.ID] = in.Name
	}
	switch action {
	case "rk":
		clients := append([]clientDraft{}, draft.Clients...)
		sort.Slice(clients, func(i, j int) bool {
			a, b := traffic[clients[i].ID], traffic[clients[j].ID]
			return a.up+a.down > b.up+b.down
		})
		lines := []string{say("📈 <b>流量排行</b>（累计）", "📈 <b>Traffic ranking</b> (total)")}
		for i, client := range clients {
			if i == 15 {
				break
			}
			rank := strconv.Itoa(i+1) + "."
			if i < 3 {
				rank = []string{"🥇", "🥈", "🥉"}[i]
			}
			t := traffic[client.ID]
			lines = append(lines, fmt.Sprintf("%s <b>%s</b> · <code>%s</code>\n      🔼 %s · 🔽 %s", rank, html.EscapeString(client.Name), humanBytes(t.up+t.down), humanBytes(t.up), humanBytes(t.down)))
		}
		if len(clients) == 0 {
			lines = append(lines, say("📭 还没有客户端", "📭 No clients yet"))
		}
		reply(strings.Join(lines, "\n"), backButton(lang))
	case "on":
		lines := []string{say("🟢 <b>在线客户端</b>（最近 2 分钟有流量）", "🟢 <b>Online clients</b> (traffic in the last 2 minutes)")}
		for _, client := range draft.Clients {
			if traffic[client.ID].online {
				lines = append(lines, fmt.Sprintf("👤 <b>%s</b> · 📍 %s", html.EscapeString(client.Name), html.EscapeString(inboundName[client.InboundID])))
			}
		}
		if len(lines) == 1 {
			lines = append(lines, say("😴 当前没有在线客户端", "😴 No client is online"))
		}
		reply(strings.Join(lines, "\n"), backButton(lang))
	case "ds":
		lines := []string{say("⏳ <b>即将到期或流量快用完</b>\n（3 天内到期或已用 80% 以上）", "⏳ <b>Expiring or low on quota</b>\n(expiring within 3 days or 80%+ used)")}
		now := time.Now()
		for _, client := range draft.Clients {
			if !client.Enabled {
				continue
			}
			t, notes := traffic[client.ID], []string{}
			if client.ExpiresAt != nil && time.UnixMilli(*client.ExpiresAt).Before(now.Add(72*time.Hour)) {
				notes = append(notes, say("📅 到期 ", "📅 expires ")+time.UnixMilli(*client.ExpiresAt).Format("2006-01-02 15:04"))
			}
			if quota := quotaBytes(client.QuotaGB); quota > 0 && (t.up+t.down)*100 >= quota*80 {
				notes = append(notes, fmt.Sprintf("📊 %s / %s", humanBytes(t.up+t.down), humanBytes(quota)))
			}
			if len(notes) > 0 {
				lines = append(lines, fmt.Sprintf("👤 <b>%s</b>\n      %s", html.EscapeString(client.Name), strings.Join(notes, " · ")))
			}
		}
		if len(lines) == 1 {
			lines = append(lines, say("✅ 没有即将到期或流量快用完的客户端", "✅ No client is expiring soon or low on quota"))
		}
		reply(strings.Join(lines, "\n"), backButton(lang))
	case "ib":
		lines := []string{say("📋 <b>入站列表</b>（🟢 启用 · ⛔ 停用）", "📋 <b>Inbounds</b> (🟢 on · ⛔ off)")}
		for _, in := range draft.Inbounds {
			var total uint64
			count := 0
			for _, client := range draft.Clients {
				if client.InboundID == in.ID {
					total += traffic[client.ID].up + traffic[client.ID].down
					count++
				}
			}
			state := "🟢"
			if !in.Enabled {
				state = "⛔"
			}
			transport := "TCP"
			if in.Preset == "hy2" {
				transport = "UDP"
			}
			port := fmt.Sprintf("🔌 %s/%d", transport, in.Port)
			if in.PortHopping != "" {
				port += say(" · 🔀 跳跃 ", " · 🔀 hopping ") + in.PortHopping
			}
			lines = append(lines, fmt.Sprintf(say("%s <b>%s</b>\n      🧩 %s · %s\n      👥 %d 个客户端 · 🚦 %s", "%s <b>%s</b>\n      🧩 %s · %s\n      👥 %d clients · 🚦 %s"), state, html.EscapeString(in.Name), presetLabel(in.Preset), port, count, humanBytes(total)))
		}
		if len(draft.Inbounds) == 0 {
			lines = append(lines, say("📭 还没有入站，可用“一键创建入站”", "📭 No inbounds yet; use Quick create inbound"))
		}
		reply(strings.Join(lines, "\n"), backButton(lang))
	case "cl":
		page, _ := strconv.Atoi(argument)
		const size = 8
		if page < 0 || page*size >= len(draft.Clients) {
			page = 0
		}
		keyboard := [][]tgButton{}
		for i := page * size; i < len(draft.Clients) && i < (page+1)*size; i++ {
			client := draft.Clients[i]
			mark := "⚪"
			if !client.Enabled {
				mark = "⛔"
			} else if traffic[client.ID].online {
				mark = "🟢"
			}
			keyboard = append(keyboard, []tgButton{{mark + " " + client.Name, "c:" + client.ID}})
		}
		nav := []tgButton{}
		if page > 0 {
			nav = append(nav, tgButton{say("⬅️ 上一页", "⬅️ Prev"), "cl:" + strconv.Itoa(page-1)})
		}
		if (page+1)*size < len(draft.Clients) {
			nav = append(nav, tgButton{say("下一页 ➡️", "Next ➡️"), "cl:" + strconv.Itoa(page+1)})
		}
		if len(nav) > 0 {
			keyboard = append(keyboard, nav)
		}
		keyboard = append(keyboard, backButton(lang)...)
		title := fmt.Sprintf(say("👤 <b>客户端</b>（共 %d 个）\n🟢 在线 · ⚪ 离线 · ⛔ 停用", "👤 <b>Clients</b> (%d total)\n🟢 online · ⚪ offline · ⛔ disabled"), len(draft.Clients))
		if len(draft.Clients) == 0 {
			title = say("📭 还没有客户端", "📭 No clients yet")
		}
		reply(title, keyboard)
	case "c":
		client, ok := findClient(draft, argument)
		if !ok {
			reply(say("❌ 客户端不存在或已删除", "❌ The client does not exist or was deleted"), backButton(lang))
			return
		}
		reply(clientDetail(client, inboundName[client.InboundID], traffic[client.ID], lang), clientButtons(client, lang))
	case "ct":
		client, ok := findClient(draft, argument)
		if !ok {
			reply(say("❌ 客户端不存在或已删除", "❌ The client does not exist or was deleted"), backButton(lang))
			return
		}
		result, err := rt.botSaveWorkspace(ctx, func(next *workspaceDraft) error {
			for i := range next.Clients {
				if next.Clients[i].ID == client.ID {
					next.Clients[i].Enabled = !next.Clients[i].Enabled
					return nil
				}
			}
			return i18n.New("客户端不存在或已删除", "The client does not exist or was deleted")
		})
		if err != nil {
			failed(say("操作失败：", "Failed: "), err)
			return
		}
		client.Enabled = !client.Enabled
		verb := say("✅ 已启用", "✅ Enabled")
		if !client.Enabled {
			verb = say("⛔ 已停用", "⛔ Disabled")
		}
		reply(verb+" <b>"+html.EscapeString(client.Name)+"</b>\n"+appliedText(result, lang), clientButtons(client, lang))
	case "cn":
		rt.mu.Lock()
		connection, err := rt.connection(ctx, connectionRequest{ClientID: argument, WorkspaceRevision: revision})
		rt.mu.Unlock()
		if err != nil {
			failed(say("无法生成节点链接：", "Cannot build the node link: "), err)
			return
		}
		reply(say("🔗 <b>节点链接</b>（点击复制）\n", "🔗 <b>Node link</b> (tap to copy)\n")+"<code>"+html.EscapeString(connection.Link)+"</code>", backButton(lang))
	case "qc":
		reply(say("⚡ <b>一键创建入站</b>\n🎲 自动选端口、生成客户端，保存即生效\n🔐 TLS 类需要面板证书\n⚠️ 保存会重启 Xray，经本机节点的连接（包括 Telegram）会短暂断开", "⚡ <b>Quick create inbound</b>\n🎲 Port and client are generated, applied on save\n🔐 TLS types need the panel certificate\n⚠️ Saving restarts Xray; connections through this server (Telegram included) drop briefly"),
			append([][]tgButton{{{"Hysteria2", "q:hy2"}, {"VLESS Reality Vision", "q:reality"}}, {{"VLESS XHTTP TLS", "q:xhttp-tls"}, {"VLESS XHTTP Reality", "q:xhttp-reality"}}}, backButton(lang)...))
	case "q":
		name, clientID, result, err := rt.botQuickCreate(ctx, argument, lang)
		if err != nil {
			failed(say("创建失败：", "Create failed: "), err)
			return
		}
		reply(say("✅ 已创建 ", "✅ Created ")+"<b>"+html.EscapeString(name)+"</b>\n"+appliedText(result, lang), append([][]tgButton{{{say("🔗 节点链接", "🔗 Node link"), "cn:" + clientID}}}, backButton(lang)...))
	case "bk":
		if !rt.backupMu.TryLock() {
			reply(say("⏳ 备份正在进行，请稍后再试", "⏳ A backup is running; try again later"), backButton(lang))
			return
		}
		// 备份只读数据并查询一次流量计数，不重启Xray；文件附带返回菜单按钮。
		rt.mu.Lock()
		name, file, err := rt.exportBackup(ctx, s.BackupPassword)
		rt.mu.Unlock()
		rt.backupMu.Unlock()
		if err != nil {
			failed(say("备份失败：", "Backup failed: "), err)
			return
		}
		if err := telegramDocumentSend(ctx, s.Token, chat, name, file, backupCaption(time.Now(), s.BackupPassword != "", lang), backButton(lang)); err != nil {
			failed(say("备份文件发送失败：", "Failed to send the backup file: "), err)
		}
	case "rx":
		reply(say("🔄 <b>重启 Xray</b>\n⚠️ 按已保存配置重启，现有连接会短暂断开。确认重启？", "🔄 <b>Restart Xray</b>\n⚠️ Restarts with the saved configuration; existing connections drop briefly. Restart?"), [][]tgButton{{{say("✅ 确认重启", "✅ Restart"), "rxc"}, {say("✖️ 取消", "✖️ Cancel"), "m"}}})
	case "rxc":
		rt.mu.Lock()
		err := rt.applySaved(ctx)
		status := rt.engine.Status()
		rt.mu.Unlock()
		if err != nil {
			failed(say("重启失败：", "Restart failed: "), err)
			return
		}
		reply(fmt.Sprintf(say("✅ <b>Xray 已重启</b>\n📡 版本：%s\n📋 代理入站：%d 条", "✅ <b>Xray restarted</b>\n📡 Version: %s\n📋 Proxy inbounds: %d"), html.EscapeString(status.Version), status.Inbounds), backButton(lang))
	case "up", "upc":
		current := html.EscapeString(installedVersion())
		release, err := fetchLatestRelease(ctx)
		if err != nil {
			reply(say("❌ 无法连接 GitHub 检查更新", "❌ Cannot reach GitHub to check for updates"), backButton(lang))
			return
		}
		latest, changes := release.tag, releaseChanges(release.notes, lang)
		if !newerVersion(latest, installedVersion()) {
			reply(fmt.Sprintf(say("✅ 当前已是最新版本 <code>%s</code>", "✅ Already on the latest version <code>%s</code>"), current), backButton(lang))
			return
		}
		latest = html.EscapeString(latest)
		if !upgradeReady() {
			reply(fmt.Sprintf(say("⬆️ 发现新版本 <b>%s</b>（当前 %s）\n🛠 服务器上还没有网页升级助手，请先在服务器上运行一次 <code>o-ui update</code>", "⬆️ New version <b>%s</b> (current %s)\n🛠 The web update helper is not installed; run <code>o-ui update</code> on the server once"), latest, current)+changes, backButton(lang))
			return
		}
		if action == "up" {
			reply(fmt.Sprintf(say("⬆️ 发现新版本 <b>%s</b>（当前 %s）\n⚠️ 升级期间面板与代理会短暂中断，要立即升级吗？", "⬆️ New version <b>%s</b> (current %s)\n⚠️ The panel and proxies drop briefly during the update. Update now?"), latest, current)+changes, [][]tgButton{{{say("🚀 立即升级", "🚀 Update now"), "upc"}, {say("✖️ 取消", "✖️ Cancel"), "m"}}})
			return
		}
		if output, err := runUpgrade(ctx); err != nil {
			reply(say("❌ 启动升级失败：", "❌ Failed to start the update: ")+html.EscapeString(output), backButton(lang))
			return
		}
		reply(fmt.Sprintf(say("🚀 已开始升级到 <b>%s</b>，约一分钟后面板恢复。", "🚀 Updating to <b>%s</b>; the panel is back in about a minute."), latest), nil)
	}
}

// releaseChanges把本次更新的主要内容排成机器人消息的一段，按界面语言取用；没有要点时为空。
func releaseChanges(notes []releaseNote, lang string) string {
	if len(notes) == 0 {
		return ""
	}
	lines := []string{"", i18n.Pick(lang, "📝 <b>本次更新</b>", "📝 <b>In this release</b>")}
	for _, note := range notes {
		lines = append(lines, "• "+html.EscapeString(i18n.Pick(lang, note.Zh, note.En)))
	}
	return strings.Join(lines, "\n")
}

func findClient(draft workspaceDraft, id string) (clientDraft, bool) {
	for _, client := range draft.Clients {
		if client.ID == id {
			return client, true
		}
	}
	return clientDraft{}, false
}

func clientDetail(client clientDraft, inbound string, t trafficSnapshot, lang string) string {
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	state := say("⚪ 离线", "⚪ Offline")
	if !client.Enabled {
		state = say("⛔ 已停用", "⛔ Disabled")
	} else if t.online {
		state = say("🟢 在线", "🟢 Online")
	}
	quota := say("不限", "unlimited")
	if client.QuotaGB > 0 {
		quota = humanBytes(quotaBytes(client.QuotaGB))
	}
	expiry := say("永不过期", "never expires")
	if client.ExpiresAt != nil {
		at := time.UnixMilli(*client.ExpiresAt)
		days := int(time.Until(at).Hours()/24 + .999)
		if days > 0 {
			expiry = fmt.Sprintf(say("%s（%d 天后）", "%s (%d days left)"), at.Format("2006-01-02 15:04"), days)
		} else {
			expiry = at.Format("2006-01-02 15:04") + say("（已到期）", " (expired)")
		}
	}
	last := say("从未", "never")
	if !t.lastOnline.IsZero() {
		last = t.lastOnline.Local().Format("2006-01-02 15:04")
	}
	limit := say("不限", "unlimited")
	if client.IPLimit > 0 {
		limit = strconv.Itoa(client.IPLimit)
	}
	return strings.Join([]string{
		"👤 <b>" + html.EscapeString(client.Name) + "</b>", say("📍 入站：", "📍 Inbound: ") + html.EscapeString(inbound), say("🌐 状态：", "🌐 Status: ") + state,
		fmt.Sprintf(say("📊 流量：<code>%s / %s</code>", "📊 Traffic: <code>%s / %s</code>"), humanBytes(t.up+t.down), quota),
		fmt.Sprintf(say("🔼 上传：%s · 🔽 下载：%s", "🔼 Upload: %s · 🔽 Download: %s"), humanBytes(t.up), humanBytes(t.down)),
		say("📅 到期：", "📅 Expiry: ") + expiry, say("🔙 最近上线：", "🔙 Last online: ") + last, say("🔢 IP 上限：", "🔢 IP limit: ") + limit,
	}, "\n")
}

func clientButtons(client clientDraft, lang string) [][]tgButton {
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	toggle := say("⛔ 停用", "⛔ Disable")
	if !client.Enabled {
		toggle = say("✅ 启用", "✅ Enable")
	}
	return [][]tgButton{{{toggle, "ct:" + client.ID}, {say("🔗 节点链接", "🔗 Node link"), "cn:" + client.ID}}, {{say("👤 客户端列表", "👤 Clients"), "cl:0"}, {say("🏠 返回菜单", "🏠 Menu"), "m"}}}
}

// appliedText：保存会生成新的Xray进程，现有连接（包括经本机节点的Telegram）短暂断开后重连。
func appliedText(result savedDocument, lang string) string {
	text := i18n.Pick(lang, "✅ 已保存并生效（Xray 已重启，现有连接会短暂断开后自动重连）", "✅ Saved and applied (Xray restarted; existing connections drop briefly and reconnect)")
	if !result.Applied {
		text = i18n.Pick(lang, "⚠️ 已保存，但未生效：", "⚠️ Saved, but not applied: ") + html.EscapeString(i18n.Translate(result.ApplyError, lang))
	}
	if result.Firewall != "" {
		text += i18n.Pick(lang, "\n🧱 防火墙：", "\n🧱 Firewall: ") + html.EscapeString(i18n.Translate(result.Firewall, lang))
	}
	return text
}

// botSaveWorkspace与网页保存入站相同：校验、保存（含订阅失效处理）、预检并应用、同步防火墙；持有rt.mu。
func (rt *Runtime) botSaveWorkspace(ctx context.Context, mutate func(*workspaceDraft) error) (savedDocument, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	old, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		return savedDocument{}, err
	}
	draft := workspaceDraft{Inbounds: []inboundDraft{}, Clients: []clientDraft{}}
	if string(old.Data) != "null" && len(old.Data) > 0 && json.Unmarshal(old.Data, &draft) != nil {
		return savedDocument{}, i18n.New("入站草稿损坏", "Inbound draft is corrupted")
	}
	if err := mutate(&draft); err != nil {
		return savedDocument{}, err
	}
	data, err := json.Marshal(draft)
	if err != nil {
		return savedDocument{}, err
	}
	if err := validateWorkspaceDraft(data, old.Data); err != nil {
		return savedDocument{}, err
	}
	doc, err := saveSubscriptionWorkspace(ctx, rt.state, old, data)
	if err != nil {
		return savedDocument{}, err
	}
	result := rt.saved(ctx, doc)
	result.Firewall = syncFirewall(ctx, old.Data, doc.Data)
	return result, nil
}

// countryName：一键创建入站名称用的国家名（常见VPS所在地），查不到时用国家代码。
func countryName(code, lang string) string {
	switch code {
	case "US":
		return i18n.Pick(lang, "美国", "United States")
	case "JP":
		return i18n.Pick(lang, "日本", "Japan")
	case "HK":
		return i18n.Pick(lang, "香港", "Hong Kong")
	case "TW":
		return i18n.Pick(lang, "台湾", "Taiwan")
	case "SG":
		return i18n.Pick(lang, "新加坡", "Singapore")
	case "KR":
		return i18n.Pick(lang, "韩国", "South Korea")
	case "DE":
		return i18n.Pick(lang, "德国", "Germany")
	case "GB":
		return i18n.Pick(lang, "英国", "United Kingdom")
	case "FR":
		return i18n.Pick(lang, "法国", "France")
	case "NL":
		return i18n.Pick(lang, "荷兰", "Netherlands")
	case "CA":
		return i18n.Pick(lang, "加拿大", "Canada")
	case "AU":
		return i18n.Pick(lang, "澳大利亚", "Australia")
	case "RU":
		return i18n.Pick(lang, "俄罗斯", "Russia")
	case "IN":
		return i18n.Pick(lang, "印度", "India")
	case "MY":
		return i18n.Pick(lang, "马来西亚", "Malaysia")
	case "TH":
		return i18n.Pick(lang, "泰国", "Thailand")
	case "VN":
		return i18n.Pick(lang, "越南", "Vietnam")
	case "PH":
		return i18n.Pick(lang, "菲律宾", "Philippines")
	case "ID":
		return i18n.Pick(lang, "印度尼西亚", "Indonesia")
	case "TR":
		return i18n.Pick(lang, "土耳其", "Turkey")
	case "AE":
		return i18n.Pick(lang, "阿联酋", "United Arab Emirates")
	case "BR":
		return i18n.Pick(lang, "巴西", "Brazil")
	case "IT":
		return i18n.Pick(lang, "意大利", "Italy")
	case "ES":
		return i18n.Pick(lang, "西班牙", "Spain")
	case "SE":
		return i18n.Pick(lang, "瑞典", "Sweden")
	case "CH":
		return i18n.Pick(lang, "瑞士", "Switzerland")
	case "PL":
		return i18n.Pick(lang, "波兰", "Poland")
	case "FI":
		return i18n.Pick(lang, "芬兰", "Finland")
	case "MO":
		return i18n.Pick(lang, "澳门", "Macao")
	case "CN":
		return i18n.Pick(lang, "中国", "China")
	case "":
		return i18n.Pick(lang, "未知地区", "Unknown region")
	}
	return code
}

func randomText(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	for i := range out {
		index, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		out[i] = alphabet[index.Int64()]
	}
	return string(out)
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = (b[6]&15)|64, (b[8]&63)|128
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// botQuickCreate与网页“一键创建”相同：随机空闲端口（HY2另配端口跳跃范围），分享主机优先面板证书域名，
// 名称为“地区-类型-日期”，同名加(1)、(2)…；TLS类复用面板证书。
func (rt *Runtime) botQuickCreate(ctx context.Context, preset, lang string) (string, string, savedDocument, error) {
	label := presetLabel(preset)
	if label == "" {
		return "", "", savedDocument{}, i18n.New("入站预设无效", "Invalid inbound preset")
	}
	tls := preset == "hy2" || preset == "xhttp-tls"
	domain := ""
	if rt.panelTLS != nil {
		for _, name := range rt.panelTLS.names {
			if !strings.Contains(name, "*") {
				domain = name
				break
			}
		}
	}
	if tls && domain == "" {
		return "", "", savedDocument{}, i18n.New("Hysteria2 和 XHTTP TLS 需要面板证书，请先在“面板设置”配置证书", "Hysteria2 and XHTTP TLS need the panel certificate; configure it in Panel settings first")
	}
	info := rt.public.get()
	shareHost, country := domain, ""
	for _, address := range []*publicAddress{info.IPv4, info.IPv6} {
		if address != nil {
			if shareHost == "" {
				shareHost = address.IP
			}
			if country == "" {
				country = address.Country
			}
		}
	}
	if shareHost == "" {
		return "", "", savedDocument{}, i18n.New("面板没有证书域名，也未查询到服务器公网 IP", "There is no certificate domain and the public IP is unknown")
	}
	region := countryName(country, lang)
	var name, clientID string
	result, err := rt.botSaveWorkspace(ctx, func(draft *workspaceDraft) error {
		used, names := map[int]bool{}, map[string]bool{}
		for _, in := range draft.Inbounds {
			used[in.Port], names[in.Name] = true, true
			if start, end, ok := config.ParsePortRange(in.PortHopping); ok {
				for port := start; port <= end; port++ {
					used[port] = true
				}
			}
		}
		port, ok := freePort(used, preset == "hy2", func() int { return quickPortMin + mathrand.IntN(quickPortMax-quickPortMin+1) })
		if !ok {
			return i18n.New("暂时找不到空闲端口，请稍后重试", "No free port found right now; retry later")
		}
		hop := ""
		if preset == "hy2" {
			used[port] = true
			if hop, ok = freeHopRange(used, func() int { return hopRangeMin + mathrand.IntN(hopRangeMax-hopRangeSize+2-hopRangeMin) }); !ok {
				return i18n.New("暂时找不到空闲的端口跳跃范围，请稍后重试", "No free port hopping range found right now; retry later")
			}
		}
		base := fmt.Sprintf("%s-%s-%s", region, label, time.Now().Format("2006.01.02"))
		name = base
		for n := 1; names[name]; n++ {
			name = fmt.Sprintf("%s(%d)", base, n)
		}
		clientName := randomText(6)
		for slices.ContainsFunc(draft.Clients, func(c clientDraft) bool { return c.Name == clientName }) {
			clientName = randomText(6)
		}
		id := randomUUID()
		clientID = id + "-client"
		connection := &connectionDraft{Path: "/", Mode: "auto", UsePanelCertificate: tls}
		if !tls {
			connection.Target, connection.ServerName = "www.amd.com:443", "www.amd.com"
		}
		in := inboundDraft{ID: id, Name: name, Preset: preset, Port: port, Listen: "0.0.0.0", ShareHost: shareHost, PortHopping: hop, Enabled: true, Connection: connection}
		in.Sniffing.Enabled, in.Sniffing.HTTP, in.Sniffing.TLS, in.Sniffing.QUIC, in.Sniffing.RouteOnly = true, true, true, true, true
		draft.Inbounds = append(draft.Inbounds, in)
		draft.Clients = append(draft.Clients, clientDraft{ID: clientID, InboundID: id, Name: clientName, Enabled: true, Subscription: true})
		return nil
	})
	return name, clientID, result, err
}
