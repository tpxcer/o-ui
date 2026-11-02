package localapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 客户端上线/离线通知（参照OUI的Telegram上线通知，按客户端单独开启）：客户端开始有流量时记一次会话，本次累计满1 MB（OUI为1 MiB，这里与面板十进制单位一致）才通知上线，
// 过滤零星心跳；已通知的会话连续10分钟没有流量才通知离线，短暂空闲不反复提醒。会话只在内存，面板重启后重新计。
const (
	onlineNotifyTraffic = 1_000_000
	offlineNotifyAfter  = 10 * time.Minute
)

type onlineSession struct {
	start     time.Time
	up, down  uint64
	announced bool
	ips       []string            // 会话期间出现过的在线IP；离线时连接已断开，IP采样里已经没有它们
	inbounds  map[string][]string // 多入站客户端：每个IP会话期间实际连过的入站ID（按连接表判断）
}

type onlineEvent struct {
	client  clientDraft
	online  bool
	session onlineSession
	traffic trafficSnapshot
}

// onlineChanges按本轮流量更新会话并返回需要通知的上线/离线；停用或删除的客户端直接结束会话，不通知。
func onlineChanges(sessions map[string]onlineSession, clients []clientDraft, traffic map[string]trafficSnapshot, now time.Time) []onlineEvent {
	events := []onlineEvent{}
	enabled := map[string]bool{}
	for _, client := range clients {
		if !client.Enabled {
			continue
		}
		enabled[client.ID] = true
		t := traffic[client.ID]
		session, exists := sessions[client.ID]
		if !exists {
			if t.online {
				sessions[client.ID] = onlineSession{start: now, up: t.up, down: t.down}
			}
			continue
		}
		if t.up < session.up || t.down < session.down {
			session.up, session.down = t.up, t.down // 累计被重置：重设基线
		}
		if t.lastOnline.IsZero() || now.Sub(t.lastOnline) >= offlineNotifyAfter {
			delete(sessions, client.ID)
			if session.announced {
				events = append(events, onlineEvent{client: client, session: session, traffic: t})
			}
			continue
		}
		if !session.announced && t.up-session.up+t.down-session.down >= onlineNotifyTraffic {
			session.announced = true
			events = append(events, onlineEvent{client: client, online: true, session: session, traffic: t})
		}
		sessions[client.ID] = session
	}
	for id := range sessions {
		if !enabled[id] {
			delete(sessions, id)
		}
	}
	return events
}

// onlineDuration与OUI一致精确到秒：“X小时Y分钟Z秒”。
func onlineDuration(d time.Duration, lang string) string {
	seconds := int(max(d, 0).Seconds())
	hours, minutes := seconds/3600, seconds%3600/60
	seconds %= 60
	switch {
	case hours > 0:
		return fmt.Sprintf(i18n.Pick(lang, "%d小时%d分钟%d秒", "%d h %d min %d s"), hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf(i18n.Pick(lang, "%d分钟%d秒", "%d min %d s"), minutes, seconds)
	}
	return fmt.Sprintf(i18n.Pick(lang, "%d秒", "%d s"), seconds)
}

// ouiTraffic与OUI通知里的流量写法一致：1024进制、两位小数、数字与单位之间无空格，如1.41MB。
func ouiTraffic(n uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	value, unit := float64(n), 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	return fmt.Sprintf("%.2f%s", value, units[unit])
}

// onlineMessages的字段、顺序与写法照OUI的上线/下线通知（用户2026-10-22提供截图）：每个IP一条，其余内容相同；没有采到IP时发一条不含IP。
// 不含“节点名称”行（用户2026-10-22要求去掉）；2026-11-02起按用户要求加一行“入站”，只写类型与端口，由inboundsFor按IP给出实际连的入站。
func onlineMessages(event onlineEvent, ips []string, inboundsFor func(ip string) []string, lang string) []string {
	if len(ips) == 0 {
		return []string{onlineMessage(event, inboundsFor(""), "", lang)}
	}
	messages := []string{}
	for _, ip := range ips {
		messages = append(messages, onlineMessage(event, inboundsFor(ip), ip, lang))
	}
	return messages
}

// linkedInbounds返回客户端关联的已启用入站（都已停用时返回全部关联入站），按关联顺序。
func linkedInbounds(draft workspaceDraft, client clientDraft) []inboundDraft {
	enabled, all := []inboundDraft{}, []inboundDraft{}
	for _, id := range client.inboundIDs() {
		for _, in := range draft.Inbounds {
			if in.ID == id {
				all = append(all, in)
				if in.Enabled {
					enabled = append(enabled, in)
				}
			}
		}
	}
	if len(enabled) > 0 {
		return enabled
	}
	return all
}

// endpointLabel把入站写成“类型 · 传输/端口”，如“Reality · TCP/54716”。
func endpointLabel(in inboundDraft) string {
	transport := "TCP"
	if in.Preset == "hy2" {
		transport = "UDP"
	}
	return fmt.Sprintf("%s · %s/%d", presetLabel(in.Preset), transport, in.Port)
}

// procNetTCP是内核TCP连接表；测试时换成夹具文件。
var procNetTCP = []string{"/proc/net/tcp", "/proc/net/tcp6"}

// tcpPeers读取内核TCP连接表，返回每个对端IP已建立连接的本地端口；一个表都读不到时返回nil，不做入站判断。
func tcpPeers() map[string]map[int]bool {
	peers, read := map[string]map[int]bool{}, false
	for _, path := range procNetTCP {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		read = true
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[3] != "01" { // 01即ESTABLISHED；表头与监听等状态跳过
				continue
			}
			_, localPort, ok := strings.Cut(fields[1], ":")
			remoteIP, _, ok2 := strings.Cut(fields[2], ":")
			port, err := strconv.ParseUint(localPort, 16, 16)
			ip := procIP(remoteIP)
			if !ok || !ok2 || err != nil || ip == "" {
				continue
			}
			if peers[ip] == nil {
				peers[ip] = map[int]bool{}
			}
			peers[ip][int(port)] = true
		}
	}
	if !read {
		return nil
	}
	return peers
}

// procIP把连接表里的十六进制地址（按32位分组、小端，发布平台amd64/arm64均为小端）还原成IP；IPv4映射的IPv6地址还原为IPv4。
func procIP(text string) string {
	raw, err := hex.DecodeString(text)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return ""
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return net.IP(raw).String()
}

// ipInbounds按连接表判断某个在线IP实际连的是哪个关联入站：TCP入站（Reality/XHTTP）看与该IP已建立连接的本地端口；
// HY2走UDP，连接表里看不到对端，该IP没连任何TCP入站时归到HY2入站（关联多个HY2时无法再细分）。连接表读不到时不判断。
func ipInbounds(linked []inboundDraft, ip string, peers map[string]map[int]bool) []string {
	parsed := net.ParseIP(ip)
	if peers == nil || parsed == nil {
		return nil
	}
	ports := peers[parsed.String()]
	tcp, udp := []string{}, []string{}
	for _, in := range linked {
		switch {
		case in.Preset == "hy2":
			udp = append(udp, in.ID)
		case ports[in.Port]:
			tcp = append(tcp, in.ID)
		}
	}
	if len(tcp) > 0 {
		return tcp
	}
	return udp
}

// messageInbounds给出通知里“入站”行的内容：单入站客户端直接写该入站；多入站写该IP会话期间实际连过的入站（没有IP时合并全部IP），
// 判断不出时返回空切片（显示“未能识别”），不再列出全部关联入站；没有关联入站时返回nil（不写这一行）。
func messageInbounds(linked []inboundDraft, seen map[string][]string, ip string) []string {
	if len(linked) == 0 {
		return nil
	}
	if len(linked) == 1 {
		return []string{endpointLabel(linked[0])}
	}
	ids := seen[ip]
	if ip == "" {
		for _, list := range seen {
			ids = mergeIPs(ids, list)
		}
	}
	labels := []string{}
	for _, in := range linked {
		if slices.Contains(ids, in.ID) {
			labels = append(labels, endpointLabel(in))
		}
	}
	return labels
}

func onlineMessage(event onlineEvent, inbounds []string, ip, lang string) string {
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	code := func(value string) string { return "<code>" + html.EscapeString(value) + "</code>" }
	lines := []string{say("💎 <b>o-ui 用户通知</b>", "💎 <b>o-ui client alert</b>"), say("📴 <b>客户端下线</b>", "📴 <b>Client offline</b>")}
	if event.online {
		lines[1] = say("🚀 <b>客户端上线</b>", "🚀 <b>Client online</b>")
	}
	lines = append(lines, say("📧 用户/节点：", "📧 Client: ")+code(event.client.Name))
	if inbounds != nil {
		endpoints := []string{}
		for _, endpoint := range inbounds {
			endpoints = append(endpoints, code(endpoint))
		}
		if len(endpoints) == 0 {
			endpoints = append(endpoints, say("未能识别", "unknown"))
		}
		lines = append(lines, say("🔌 入站：", "🔌 Inbound: ")+strings.Join(endpoints, say("、", ", ")))
	}
	if ip != "" {
		label := say("🌐 IP 地址：", "🌐 IP: ")
		if !event.online {
			label = say("🌐 离线 IP 地址：", "🌐 Last IP: ")
		}
		lines = append(lines, label+code(ip))
	}
	if event.online {
		return strings.Join(append(lines, say("⏰ 上线时间：", "⏰ Online at: ")+code(event.session.start.Local().Format("2006-01-02 15:04:05"))), "\n")
	}
	offline := event.traffic.lastOnline
	up, down := event.traffic.up-event.session.up, event.traffic.down-event.session.down
	return strings.Join(append(lines,
		say("⏱ 在线时长：", "⏱ Online for: ")+code(onlineDuration(offline.Sub(event.session.start), lang)),
		say("⏰ 下线时间：", "⏰ Offline at: ")+code(offline.Local().Format("2006-01-02 15:04:05")),
		say("📊 流量：", "📊 Traffic: ")+code(fmt.Sprintf(say("↑%s / ↓%s / 合计%s", "↑%s / ↓%s / total %s"), ouiTraffic(up), ouiTraffic(down), ouiTraffic(up+down))),
	), "\n")
}

// mergeIPs按出现顺序去重合并，最多保留8个。
func mergeIPs(ips, more []string) []string {
	for _, ip := range more {
		if len(ips) < 8 && !slices.Contains(ips, ip) {
			ips = append(ips, ip)
		}
	}
	return ips
}

// onlineIPs读取IP上限采样到的当前在线IP（每10秒一次；Xray不统计本机回环地址）。
func (rt *Runtime) onlineIPs(clientID string) []string {
	if rt.iplimit == nil {
		return nil
	}
	rt.iplimit.mu.Lock()
	defer rt.iplimit.mu.Unlock()
	ips := []string{}
	for _, item := range rt.iplimit.online[clientID] {
		ips = append(ips, item.IP)
	}
	return ips
}

// 开启了通知的客户端单独存一份文档：切换开关不改入站草稿，不会重启Xray。
const telegramNotifyDocument = "telegram-notify"

func readTelegramNotify(ctx context.Context, state *store.Store) (map[string]bool, int64, error) {
	doc, err := state.ReadDocument(ctx, telegramNotifyDocument)
	if err != nil {
		return nil, 0, err
	}
	var saved struct {
		Clients []string `json:"clients"`
	}
	if string(doc.Data) != "null" && json.Unmarshal(doc.Data, &saved) != nil {
		return nil, 0, i18n.New("上线通知设置损坏", "Online alert settings are corrupted")
	}
	set := map[string]bool{}
	for _, id := range saved.Clients {
		set[id] = true
	}
	return set, doc.Revision, nil
}

// telegramNotifyView返回仍存在的客户端中开启了通知的ID，以及机器人是否能发送。
func telegramNotifyView(ctx context.Context, state *store.Store) (map[string]any, workspaceDraft, map[string]bool, int64, error) {
	set, revision, err := readTelegramNotify(ctx, state)
	if err != nil {
		return nil, workspaceDraft{}, nil, 0, err
	}
	doc, err := state.ReadDocument(ctx, "workspace")
	if err != nil {
		return nil, workspaceDraft{}, nil, 0, err
	}
	draft := workspaceDraft{}
	if string(doc.Data) != "null" && json.Unmarshal(doc.Data, &draft) != nil {
		return nil, workspaceDraft{}, nil, 0, i18n.New("入站草稿损坏", "Inbound draft is corrupted")
	}
	s, _, err := readTelegram(ctx, state)
	if err != nil {
		return nil, workspaceDraft{}, nil, 0, err
	}
	ids := []string{}
	for _, client := range draft.Clients {
		if set[client.ID] {
			ids = append(ids, client.ID)
		}
	}
	return map[string]any{"clients": ids, "botEnabled": s.Enabled && s.Token != "" && len(s.chats()) > 0}, draft, set, revision, nil
}

func registerTelegramNotify(mux *http.ServeMux, state *store.Store) {
	mux.HandleFunc("GET /api/panel/telegram/notify", func(w http.ResponseWriter, r *http.Request) {
		view, _, _, _, err := telegramNotifyView(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, view)
	})
	mux.HandleFunc("PUT /api/panel/telegram/notify", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ClientID string `json:"clientId"`
			Enabled  bool   `json:"enabled"`
		}
		if !readJSON(w, r, &request, 1024) {
			return
		}
		telegramMu.Lock()
		defer telegramMu.Unlock()
		_, draft, set, revision, err := telegramNotifyView(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if _, ok := findClient(draft, request.ClientID); !ok {
			respond(w, 404, map[string]string{"error": i18n.T("客户端不存在或已删除", "The client does not exist or was deleted")})
			return
		}
		set[request.ClientID] = request.Enabled
		ids := []string{}
		for _, client := range draft.Clients {
			if set[client.ID] {
				ids = append(ids, client.ID)
			}
		}
		data, _ := json.Marshal(map[string][]string{"clients": ids})
		if _, err := state.SaveDocument(r.Context(), telegramNotifyDocument, revision, data); err != nil {
			code, message := 500, i18n.T("保存上线通知设置失败", "Failed to save online alert settings")
			if errors.Is(err, store.ErrConflict) {
				code, message = 409, err.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		view, _, _, _, err := telegramNotifyView(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, view)
	})
}

func (rt *Runtime) notifyOnline(ctx context.Context, s telegramSettings, sessions map[string]onlineSession, now time.Time) {
	draft, _, traffic, err := rt.telegramData(ctx)
	if err != nil {
		return
	}
	set, _, err := readTelegramNotify(ctx, rt.state)
	if err != nil {
		return
	}
	clients := []clientDraft{}
	for _, client := range draft.Clients {
		if set[client.ID] {
			clients = append(clients, client)
		}
	}
	lang := telegramLang()
	events := onlineChanges(sessions, clients, traffic, now)
	// 多入站客户端每轮按连接表记下各在线IP实际连的入站；离线时连接已断开，只能用会话期间记下的结果。连接表每轮最多读一次。
	var peers map[string]map[int]bool
	loaded := false
	attribute := func(client clientDraft, seen map[string][]string, ips []string) map[string][]string {
		linked := linkedInbounds(draft, client)
		if len(linked) < 2 || len(ips) == 0 {
			return seen
		}
		if !loaded {
			peers, loaded = tcpPeers(), true
		}
		if seen == nil {
			seen = map[string][]string{}
		}
		for _, ip := range ips {
			seen[ip] = mergeIPs(seen[ip], ipInbounds(linked, ip, peers))
		}
		return seen
	}
	byID := map[string]clientDraft{}
	for _, client := range clients {
		byID[client.ID] = client
	}
	for id, session := range sessions {
		current := rt.onlineIPs(id)
		session.ips = mergeIPs(session.ips, current)
		session.inbounds = attribute(byID[id], session.inbounds, current)
		sessions[id] = session
	}
	for _, event := range events {
		ips, seen := event.session.ips, event.session.inbounds
		if event.online {
			current := rt.onlineIPs(event.client.ID)
			ips = mergeIPs(slices.Clone(ips), current)
			seen = attribute(event.client, maps.Clone(seen), current)
		}
		linked := linkedInbounds(draft, event.client)
		inboundsFor := func(ip string) []string { return messageInbounds(linked, seen, ip) }
		for _, text := range onlineMessages(event, ips, inboundsFor, lang) {
			_, _ = telegramBroadcast(ctx, rt.telegramIdentity(s), text)
		}
	}
}
