package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"independent-panel/internal/engine"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IP上限（参照OUI，用户2026-10-21选定：防火墙封禁、封10分钟、按同时在线IP计）：每10秒读取Xray的在线IP
// （policy statsUserOnline，按连接计数，连接全断即下线），同一客户端同时在线的源IP超过上限时保留先连上的，
// 新来的IP经root防火墙助手在该入站端口封禁10分钟，到期自动解封。不读访问日志，不踢掉整个客户端。
const (
	ipLimitDocument  = "iplimit"
	ipLimitInterval  = 10 * time.Second
	ipBanDuration    = 10 * time.Minute
	ipStaleAfter     = 2 * time.Minute // 换网判定：旧IP这么久没有新连接、新IP在活动，就让新IP接替
	ipForgetAfter    = 5 * time.Minute // 离线超过此时长才忘记首次出现时间，短暂断开不丢先到的位置
	ipNotifyCooldown = 30 * time.Minute
	ipReassertEvery  = time.Minute // 定期重申封禁，防止防火墙重载冲掉规则
)

type onlineIP struct {
	IP       string    `json:"ip"`
	LastSeen time.Time `json:"lastSeen"`
}

type ipBan struct {
	ClientID  string    `json:"clientId"`
	IP        string    `json:"ip"`
	Port      int       `json:"port,omitempty"`
	Protocol  string    `json:"protocol,omitempty"`
	Until     time.Time `json:"until"`
	Manual    bool      `json:"manual,omitempty"`
	Permanent bool      `json:"permanent,omitempty"`
}

type ipLimitRecord struct {
	Bans   []ipBan `json:"bans"`
	Exempt []ipBan `json:"exempt"` // 兼容旧记录；不再参与上限判定，下一次采样清空。
}

type trackedIP struct{ first, lastOnline time.Time }

type ipLimiter struct {
	mu         sync.Mutex
	tracked    map[string]map[string]trackedIP
	online     map[string][]onlineIP
	notified   map[string]time.Time
	synced     bool
	reasserted time.Time
	issue      string
}

func newIPLimiter() *ipLimiter {
	return &ipLimiter{tracked: map[string]map[string]trackedIP{}, online: map[string][]onlineIP{}, notified: map[string]time.Time{}}
}

// 调用root防火墙助手的ban/unban/flush；测试中替换。
var runIPBan = func(ctx context.Context, args ...string) (string, error) {
	if err := ipLimitHelperReady(); err != nil {
		return "", err
	}
	return firewallCommand(ctx, args...)
}

var (
	errIPLimitHelper    = i18n.New("未安装防火墙助手（或缺少sudo），IP上限只能统计不能执行；请用新版安装脚本运行 o-ui update", "The firewall helper (or sudo) is missing, so IP limits are only counted, not enforced; run o-ui update with the new installer")
	errIPLimitHelperOld = i18n.New("防火墙助手是旧版本，不支持IP封禁；请在服务器上再运行一次 o-ui update", "The firewall helper is an old version without IP bans; run o-ui update once more on the server")
)

// ipLimitHelperReady检查助手存在且支持IP封禁（旧版安装脚本升级时会留下不含ban的旧助手）。
func ipLimitHelperReady() error {
	data, err := os.ReadFile(firewallHelper)
	if err != nil {
		return errIPLimitHelper
	}
	if !strings.Contains(string(data), "CHAIN=o-ui-iplimit") {
		return errIPLimitHelperOld
	}
	return nil
}

// 读取Xray各用户在线IP；核心未运行时返回nil。测试中替换。
var sampleOnlineIPs = func(ctx context.Context, rt *Runtime) (map[string][]onlineIP, error) {
	c, running := rt.engine.Current()
	if !running || !strings.HasPrefix(c.StatsAddress, "unix:///") {
		return nil, nil
	}
	return queryOnlineIPs(ctx, c)
}

func queryOnlineIPs(ctx context.Context, c engine.Candidate) (map[string][]onlineIP, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, c.Binary, "api", "statsonlineiplist", "--server="+c.StatsAddress, "-timeout=2", "-all")
	command.Dir = filepath.Dir(c.Binary)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	var output boundedOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return nil, i18n.New("无法读取Xray在线IP", "Cannot read online IPs from Xray")
	}
	return parseOnlineIPs(output.Bytes()), nil
}

// parseOnlineIPs解析statsonlineiplist -all的JSON；IPv6去掉方括号，非IP的条目丢弃。
func parseOnlineIPs(data []byte) map[string][]onlineIP {
	var response struct {
		Users []struct {
			Email string `json:"email"`
			IPs   []struct {
				IP        string          `json:"ip"`
				LastSeen  json.RawMessage `json:"lastSeen"`
				LastSeen2 json.RawMessage `json:"last_seen"`
			} `json:"ips"`
		} `json:"users"`
	}
	result := map[string][]onlineIP{}
	if json.Unmarshal(data, &response) != nil {
		return result
	}
	for _, user := range response.Users {
		for _, entry := range user.IPs {
			ip := strings.TrimSuffix(strings.TrimPrefix(entry.IP, "["), "]")
			if net.ParseIP(ip) == nil || user.Email == "" {
				continue
			}
			raw := entry.LastSeen
			if len(raw) == 0 {
				raw = entry.LastSeen2
			}
			seconds, _ := strconv.ParseInt(strings.Trim(string(raw), `"`), 10, 64)
			result[user.Email] = append(result[user.Email], onlineIP{IP: net.ParseIP(ip).String(), LastSeen: time.Unix(seconds, 0)})
		}
	}
	return result
}

// decideIPLimit：skip中的IP（已封禁或手动放行）不参与；其余在线IP按“仍活跃优先、首次出现早优先”排序，
// 前limit个保留，其余封禁。仍活跃指ipStaleAfter内有新连接，用于手机换网时旧连接挂着不放的情况。
func decideIPLimit(limit int, online []onlineIP, first map[string]time.Time, skip map[string]bool, now time.Time) (keep, ban []string) {
	candidates := []onlineIP{}
	for _, item := range online {
		if !skip[item.IP] {
			candidates = append(candidates, item)
		}
	}
	active := func(item onlineIP) bool { return now.Sub(item.LastSeen) < ipStaleAfter }
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if active(a) != active(b) {
			return active(a)
		}
		if !first[a.IP].Equal(first[b.IP]) {
			return first[a.IP].Before(first[b.IP])
		}
		return a.IP < b.IP
	})
	for i, item := range candidates {
		if i < limit {
			keep = append(keep, item.IP)
		} else {
			ban = append(ban, item.IP)
		}
	}
	return keep, ban
}

func readIPLimit(ctx context.Context, s *store.Store) (ipLimitRecord, int64, error) {
	doc, err := s.ReadDocument(ctx, ipLimitDocument)
	if err != nil {
		return ipLimitRecord{}, 0, err
	}
	var record ipLimitRecord
	if string(doc.Data) != "null" && len(doc.Data) > 0 && json.Unmarshal(doc.Data, &record) != nil {
		return ipLimitRecord{}, 0, i18n.New("IP上限记录损坏", "IP limit records are corrupted")
	}
	return record, doc.Revision, nil
}

type ipLimitTarget struct {
	name, protocol string
	limit, port    int
}

// 手动封禁也适用于没有设置IP上限的客户端；自动限制在判定时检查limit。
func ipLimitTargets(data json.RawMessage) map[string]ipLimitTarget {
	targets := map[string]ipLimitTarget{}
	var draft workspaceDraft
	if json.Unmarshal(data, &draft) != nil {
		return targets
	}
	inbounds := map[string]inboundDraft{}
	for _, in := range draft.Inbounds {
		inbounds[in.ID] = in
	}
	for _, client := range draft.Clients {
		in, ok := inbounds[client.InboundID]
		if !ok || !client.Enabled || !in.Enabled {
			continue
		}
		protocol := "tcp"
		if in.Preset == "hy2" {
			protocol = "udp"
		}
		targets[client.ID] = ipLimitTarget{name: client.Name, protocol: protocol, limit: client.IPLimit, port: in.Port}
	}
	return targets
}

func (ban ipBan) active(now time.Time) bool {
	return ban.Manual && ban.Permanent || ban.Until.After(now)
}

func sameIPBanRule(a, b ipBan) bool {
	return a.IP == b.IP && a.Port == b.Port && a.Protocol == b.Protocol
}

func (rt *Runtime) ipBan(ctx context.Context, action string, ban ipBan) error {
	output, err := runIPBan(ctx, action, ban.IP, strconv.Itoa(ban.Port), ban.Protocol)
	if err != nil {
		if errors.Is(err, errIPLimitHelper) || errors.Is(err, errIPLimitHelperOld) {
			return err
		}
		return i18n.Errorf("防火墙助手执行失败：%s", "The firewall helper failed: %s", output)
	}
	return nil
}

// ipLimitTick执行一次：同步/到期解封、采样在线IP、判定并封禁超出的IP，最后保存记录。
func (rt *Runtime) ipLimitTick(ctx context.Context, now time.Time) {
	l := rt.iplimit
	l.mu.Lock()
	defer l.mu.Unlock()
	record, revision, err := readIPLimit(ctx, rt.state)
	if err != nil {
		l.issue = err.Error()
		return
	}
	issue := ""
	note := func(err error) {
		if err != nil && issue == "" {
			issue = err.Error()
		}
	}
	changed := false
	workspace, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		l.issue = i18n.T("无法读取客户端记录", "Cannot read client records")
		return
	}
	targets := ipLimitTargets(workspace.Data)
	valid := func(ban ipBan) bool {
		target, ok := targets[ban.ClientID]
		return ok && target.port == ban.Port && target.protocol == ban.Protocol && (ban.Manual || target.limit > 0) && ban.active(now)
	}
	// 启动重建与定期重申只处理仍有效的记录，失败留待下次重试。
	reassert := !l.synced || now.Sub(l.reasserted) >= ipReassertEvery
	if !l.synced {
		_, err := runIPBan(ctx, "flush")
		note(err)
		l.synced = err == nil
	}
	bans := record.Bans[:0:0]
	for _, ban := range record.Bans {
		if valid(ban) {
			if reassert {
				note(rt.ipBan(ctx, "ban", ban))
			}
			bans = append(bans, ban)
			continue
		}
		// 共享入站的另一条有效记录仍需要同一条规则，不能提前撤销。
		shared := false
		for _, other := range record.Bans {
			if valid(other) && sameIPBanRule(ban, other) {
				shared = true
				break
			}
		}
		if !shared {
			if err := rt.ipBan(ctx, "unban", ban); err != nil {
				note(err)
				bans = append(bans, ban) // 保留到期记录，让界面显示待解封并重试。
				continue
			}
		}
		changed = true
	}
	if reassert && issue == "" {
		l.reasserted = now
	}
	// 旧版手动解封写入30分钟豁免；升级后恢复正常上限规则并清理旧记录。
	if len(record.Exempt) > 0 {
		changed = true
	}
	l.online = map[string][]onlineIP{}
	online, err := sampleOnlineIPs(ctx, rt)
	note(err)
	if online != nil {
		for clientID := range l.tracked {
			if _, ok := online["client-"+clientID]; !ok {
				online["client-"+clientID] = nil
			}
		}
		for email, ips := range online {
			clientID, ok := strings.CutPrefix(email, "client-")
			if !ok {
				continue
			}
			if len(ips) > 0 {
				l.online[clientID] = ips
			}
			tracked := l.tracked[clientID]
			if tracked == nil {
				tracked = map[string]trackedIP{}
				l.tracked[clientID] = tracked
			}
			for _, item := range ips {
				entry, seen := tracked[item.IP]
				if !seen {
					entry.first = now
				}
				entry.lastOnline = now
				tracked[item.IP] = entry
			}
			for ip, entry := range tracked {
				if now.Sub(entry.lastOnline) > ipForgetAfter {
					delete(tracked, ip)
				}
			}
			if len(tracked) == 0 {
				delete(l.tracked, clientID)
			}
			target, limited := targets[clientID]
			if !limited || target.limit <= 0 || len(ips) <= target.limit {
				continue
			}
			skip, first := map[string]bool{}, map[string]time.Time{}
			for _, ban := range bans {
				if ban.Port == target.port && ban.Protocol == target.protocol {
					skip[ban.IP] = true
				}
			}
			for ip, entry := range tracked {
				first[ip] = entry.first
			}
			keep, excess := decideIPLimit(target.limit, ips, first, skip, now)
			added := []string{}
			for _, ip := range excess {
				ban := ipBan{ClientID: clientID, IP: ip, Port: target.port, Protocol: target.protocol, Until: now.Add(ipBanDuration)}
				if err := rt.ipBan(ctx, "ban", ban); err != nil {
					note(err)
					continue
				}
				bans = append(bans, ban)
				added = append(added, ip)
				changed = true
			}
			if len(added) > 0 {
				rt.logs.Add("iplimit.ban")
				if now.Sub(l.notified[clientID]) >= ipNotifyCooldown {
					l.notified[clientID] = now
					notifyIPLimit(rt.state, rt, target, keep, added)
				}
			}
		}
	}
	l.issue = issue
	if changed {
		data, _ := json.Marshal(ipLimitRecord{Bans: bans})
		if _, err := rt.state.SaveDocument(ctx, ipLimitDocument, revision, data); err != nil {
			l.issue = i18n.T("无法保存IP上限记录", "Cannot save IP limit records")
		}
	}
}

// notifyIPLimit经Telegram（已启用时）发送超限提醒；同一客户端30分钟内只提醒一次。
func notifyIPLimit(state *store.Store, rt *Runtime, target ipLimitTarget, keep, banned []string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, _, err := readTelegram(ctx, state)
		if err != nil || !s.Enabled {
			return
		}
		lang := telegramLang()
		text := fmt.Sprintf(i18n.Pick(lang, "🚫 <b>o-ui IP 上限</b>\n👤 客户端：<code>%s</code>（上限 %d）\n🔌 端口：%d/%s\n⛔ 已封禁 10 分钟：%s\n✅ 保留：%s", "🚫 <b>o-ui IP limit</b>\n👤 Client: <code>%s</code> (limit %d)\n🔌 Port: %d/%s\n⛔ Banned for 10 minutes: %s\n✅ Kept: %s"),
			html.EscapeString(target.name), target.limit, target.port, target.protocol, html.EscapeString(strings.Join(banned, ", ")), html.EscapeString(strings.Join(keep, ", ")))
		_, _ = telegramBroadcast(ctx, rt.telegramIdentity(s), text)
	}()
}

// RunIPLimit每10秒执行一次，直到ctx结束。
func (rt *Runtime) RunIPLimit(ctx context.Context) {
	ticker := time.NewTicker(ipLimitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			rt.ipLimitTick(ctx, now)
		}
	}
}

type ipLimitClientView struct {
	Online []onlineIP `json:"online"`
	Banned []ipBan    `json:"banned"`
	Exempt []ipBan    `json:"exempt"`
}

func registerIPLimit(mux *http.ServeMux, rt *Runtime) {
	view := func(ctx context.Context) (map[string]any, error) {
		l := rt.iplimit
		l.mu.Lock()
		defer l.mu.Unlock()
		record, revision, err := readIPLimit(ctx, rt.state)
		if err != nil {
			return nil, err
		}
		workspace, err := rt.state.ReadDocument(ctx, "workspace")
		if err != nil {
			return nil, err
		}
		targets := ipLimitTargets(workspace.Data)
		clients := map[string]*ipLimitClientView{}
		get := func(id string) *ipLimitClientView {
			if clients[id] == nil {
				clients[id] = &ipLimitClientView{Online: []onlineIP{}, Banned: []ipBan{}, Exempt: []ipBan{}}
			}
			return clients[id]
		}
		for id, ips := range l.online {
			get(id).Online = ips
		}
		now := time.Now()
		for _, ban := range record.Bans {
			get(ban.ClientID).Banned = append(get(ban.ClientID).Banned, ban)
			for id, target := range targets {
				if id != ban.ClientID && target.port == ban.Port && target.protocol == ban.Protocol {
					get(id).Banned = append(get(id).Banned, ban)
				}
			}
		}
		issue, helperErr := l.issue, ipLimitHelperReady()
		if helperErr != nil && issue == "" {
			issue = helperErr.Error()
		}
		return map[string]any{"enforced": helperErr == nil, "issue": issue, "clients": clients, "revision": revision, "workspaceRevision": workspace.Revision, "serverTime": now}, nil
	}
	mux.HandleFunc("GET /api/runtime/iplimit", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		result, err := view(r.Context())
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, result)
	})
	for _, action := range []string{"ban", "unban"} {
		mux.HandleFunc("POST /api/runtime/iplimit/"+action, func(w http.ResponseWriter, r *http.Request) {
			if rt == nil {
				respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
				return
			}
			var request ipBanRequest
			if !readJSON(w, r, &request, 1024) {
				return
			}
			code, err := rt.changeIPBan(r.Context(), action, request)
			if err != nil {
				respond(w, code, map[string]string{"error": err.Error()})
				return
			}
			result, err := view(r.Context())
			if err != nil {
				respond(w, 500, map[string]string{"error": err.Error()})
				return
			}
			respond(w, 200, result)
		})
	}
}

type ipBanRequest struct {
	ClientID          string `json:"clientId"`
	IP                string `json:"ip"`
	Minutes           int    `json:"minutes"`
	Confirm           bool   `json:"confirm"`
	Revision          int64  `json:"revision"`
	WorkspaceRevision int64  `json:"workspaceRevision"`
}

func (rt *Runtime) changeIPBan(ctx context.Context, action string, request ipBanRequest) (int, error) {
	ip := net.ParseIP(request.IP)
	if !request.Confirm || ip == nil || request.IP != ip.String() || !draftID.MatchString(request.ClientID) {
		return 400, i18n.New("请选择有效IP并确认本次操作", "Select a valid IP and confirm this operation")
	}
	if action == "ban" {
		switch request.Minutes {
		case 0, 10, 30, 60, 1440, 10080:
		default:
			return 400, i18n.New("请选择有效封禁时长", "Select a valid ban duration")
		}
	}
	// 与草稿保存串行，确认后不能因关联入站变化而操作另一个端口。
	rt.mu.Lock()
	defer rt.mu.Unlock()
	l := rt.iplimit
	l.mu.Lock()
	defer l.mu.Unlock()
	workspace, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		return 500, err
	}
	record, revision, err := readIPLimit(ctx, rt.state)
	if err != nil {
		return 500, err
	}
	if workspace.Revision != request.WorkspaceRevision || revision != request.Revision {
		return 409, i18n.New("IP状态或客户端配置已变化，请刷新后重新确认", "IP status or client settings changed; refresh and confirm again")
	}
	targets := ipLimitTargets(workspace.Data)
	target, ok := targets[request.ClientID]
	if !ok {
		return 422, i18n.New("客户端或入站未启用，请刷新列表", "The client or inbound is not enabled; refresh the list")
	}
	now := time.Now()
	ban := ipBan{ClientID: request.ClientID, IP: request.IP, Port: target.port, Protocol: target.protocol, Manual: true, Permanent: request.Minutes == 0}
	if !ban.Permanent {
		ban.Until = now.Add(time.Duration(request.Minutes) * time.Minute)
	}
	known, wasBanned := false, false
	bans := make([]ipBan, 0, len(record.Bans)+1)
	for _, item := range record.Bans {
		if sameIPBanRule(ban, item) {
			known, wasBanned = true, true
		} else {
			bans = append(bans, item)
		}
	}
	for _, item := range l.online[request.ClientID] {
		if item.IP == request.IP {
			known = true
		}
	}
	if action == "unban" && !wasBanned {
		return 404, i18n.New("没有这条封禁记录，可能已到期解封", "No such ban; it may have expired")
	}
	if action == "ban" && !known {
		return 422, i18n.New("此IP不在当前客户端的在线或封禁列表中，请刷新", "This IP is not in this client's online or banned list; refresh")
	}
	if action == "ban" {
		bans = append(bans, ban)
	}
	if err := rt.ipBan(ctx, action, ban); err != nil {
		l.issue = err.Error()
		return 502, err
	}
	data, _ := json.Marshal(ipLimitRecord{Bans: bans})
	if _, err := rt.state.SaveDocument(ctx, ipLimitDocument, revision, data); err != nil {
		// 持久化失败时恢复原规则；使用独立期限，不受浏览器断开影响。
		recoverCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		restore := "unban"
		if wasBanned {
			restore = "ban"
		}
		if rollback := rt.ipBan(recoverCtx, restore, ban); rollback != nil {
			l.synced = false
			l.issue = i18n.T("IP操作保存与回退失败，正在重新同步防火墙，请刷新检查", "Saving and rollback failed; the firewall is being resynchronized, refresh to check")
			return 500, errors.New(l.issue)
		}
		return 500, i18n.New("IP操作未保存，已恢复原规则，请刷新后重试", "IP change was not saved; the previous rule was restored, refresh and retry")
	}
	l.issue = ""
	return 200, nil
}
