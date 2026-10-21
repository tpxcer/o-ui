package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ipStaleAfter     = 2 * time.Minute  // 换网判定：旧IP这么久没有新连接、新IP在活动，就让新IP接替
	ipForgetAfter    = 5 * time.Minute  // 离线超过此时长才忘记首次出现时间，短暂断开不丢先到的位置
	ipExemptDuration = 30 * time.Minute // 手动解封后放行这么久
	ipNotifyCooldown = 30 * time.Minute
	ipReassertEvery  = time.Minute // 定期重申封禁，防止防火墙重载冲掉规则
)

type onlineIP struct {
	IP       string    `json:"ip"`
	LastSeen time.Time `json:"lastSeen"`
}

type ipBan struct {
	ClientID string    `json:"clientId"`
	IP       string    `json:"ip"`
	Port     int       `json:"port,omitempty"`
	Protocol string    `json:"protocol,omitempty"`
	Until    time.Time `json:"until"`
}

type ipLimitRecord struct {
	Bans   []ipBan `json:"bans"`
	Exempt []ipBan `json:"exempt"`
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
	if _, err := os.Stat(firewallHelper); err != nil {
		return "", errIPLimitHelper
	}
	return firewallCommand(ctx, args...)
}

var errIPLimitHelper = i18n.New("未安装防火墙助手（或缺少sudo），IP上限只能统计不能执行；请用新版安装脚本运行 o-ui update", "The firewall helper (or sudo) is missing, so IP limits are only counted, not enforced; run o-ui update with the new installer")

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
			result[user.Email] = append(result[user.Email], onlineIP{IP: ip, LastSeen: time.Unix(seconds, 0)})
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

// ipLimitTargets：启用且设置了上限的客户端，以及其入站端口与协议（HY2为UDP，其余TCP）。
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
		if !ok || client.IPLimit <= 0 || !client.Enabled || !in.Enabled {
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

func (rt *Runtime) ipBan(ctx context.Context, action string, ban ipBan) error {
	output, err := runIPBan(ctx, action, ban.IP, strconv.Itoa(ban.Port), ban.Protocol)
	if err != nil {
		if errors.Is(err, errIPLimitHelper) {
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
	// 面板启动后先清空本链再按记录重建：服务器重启后内核规则已清空，面板重启时也不留旧规则。
	if !l.synced {
		if _, err := runIPBan(ctx, "flush"); err == nil {
			for _, ban := range record.Bans {
				if ban.Until.After(now) {
					note(rt.ipBan(ctx, "ban", ban))
				}
			}
		} else {
			note(err)
		}
		l.synced, l.reasserted = true, now
	}
	workspace, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		l.issue = i18n.T("无法读取客户端记录", "Cannot read client records")
		return
	}
	targets := ipLimitTargets(workspace.Data)
	// 到期、客户端已删除/取消上限、入站端口或协议改变的封禁：解封。
	bans := record.Bans[:0:0]
	for _, ban := range record.Bans {
		target, ok := targets[ban.ClientID]
		if ban.Until.After(now) && ok && target.port == ban.Port && target.protocol == ban.Protocol {
			bans = append(bans, ban)
			continue
		}
		note(rt.ipBan(ctx, "unban", ban))
		changed = true
	}
	exempt := record.Exempt[:0:0]
	for _, item := range record.Exempt {
		if _, ok := targets[item.ClientID]; ok && item.Until.After(now) {
			exempt = append(exempt, item)
		} else {
			changed = true
		}
	}
	if now.Sub(l.reasserted) >= ipReassertEvery {
		for _, ban := range bans {
			note(rt.ipBan(ctx, "ban", ban))
		}
		l.reasserted = now
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
			if !limited || len(ips) <= target.limit {
				continue
			}
			skip, first := map[string]bool{}, map[string]time.Time{}
			for _, ban := range bans {
				if ban.ClientID == clientID {
					skip[ban.IP] = true
				}
			}
			for _, item := range exempt {
				if item.ClientID == clientID {
					skip[item.IP] = true
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
					notifyIPLimit(rt.state, target, keep, added)
				}
			}
		}
	}
	l.issue = issue
	if changed {
		data, _ := json.Marshal(ipLimitRecord{Bans: bans, Exempt: exempt})
		if _, err := rt.state.SaveDocument(ctx, ipLimitDocument, revision, data); err != nil {
			l.issue = i18n.T("无法保存IP上限记录", "Cannot save IP limit records")
		}
	}
}

// notifyIPLimit经Telegram（已启用时）发送超限提醒；同一客户端30分钟内只提醒一次。
func notifyIPLimit(state *store.Store, target ipLimitTarget, keep, banned []string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, _, err := readTelegram(ctx, state)
		if err != nil || !s.Enabled {
			return
		}
		lang := telegramLang()
		text := fmt.Sprintf(i18n.Pick(lang, "o-ui IP 上限：客户端 %s（上限 %d）同时在线超出，已在端口 %d/%s 封禁 %s 10 分钟；保留 %s", "o-ui IP limit: client %s (limit %d) exceeded; on port %d/%s banned %s for 10 minutes; kept %s"),
			target.name, target.limit, target.port, target.protocol, strings.Join(banned, ", "), strings.Join(keep, ", "))
		_, _ = telegramBroadcast(ctx, s, text)
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
		record, _, err := readIPLimit(ctx, rt.state)
		if err != nil {
			return nil, err
		}
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
			if ban.Until.After(now) {
				get(ban.ClientID).Banned = append(get(ban.ClientID).Banned, ban)
			}
		}
		for _, item := range record.Exempt {
			if item.Until.After(now) {
				get(item.ClientID).Exempt = append(get(item.ClientID).Exempt, item)
			}
		}
		_, helperErr := os.Stat(firewallHelper)
		return map[string]any{"enforced": helperErr == nil, "issue": l.issue, "clients": clients}, nil
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
	// 手动解封：立即撤销防火墙规则，并在30分钟内放行该IP（不计入上限）。
	mux.HandleFunc("POST /api/runtime/iplimit/unban", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		var request struct {
			ClientID string `json:"clientId"`
			IP       string `json:"ip"`
		}
		if !readJSON(w, r, &request, 1024) {
			return
		}
		l := rt.iplimit
		l.mu.Lock()
		record, revision, err := readIPLimit(r.Context(), rt.state)
		if err != nil {
			l.mu.Unlock()
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		found, bans := false, record.Bans[:0:0]
		for _, ban := range record.Bans {
			if ban.ClientID == request.ClientID && ban.IP == request.IP {
				found = true
				if err := rt.ipBan(r.Context(), "unban", ban); err != nil {
					l.mu.Unlock()
					respond(w, 502, map[string]string{"error": err.Error()})
					return
				}
				continue
			}
			bans = append(bans, ban)
		}
		if !found {
			l.mu.Unlock()
			respond(w, 404, map[string]string{"error": i18n.T("没有这条封禁记录，可能已到期解封", "No such ban; it may have expired")})
			return
		}
		exempt := append(record.Exempt, ipBan{ClientID: request.ClientID, IP: request.IP, Until: time.Now().Add(ipExemptDuration)})
		data, _ := json.Marshal(ipLimitRecord{Bans: bans, Exempt: exempt})
		_, err = rt.state.SaveDocument(r.Context(), ipLimitDocument, revision, data)
		l.mu.Unlock()
		if err != nil {
			code := 500
			if errors.Is(err, store.ErrConflict) {
				code = 409
			}
			respond(w, code, map[string]string{"error": i18n.T("解封已执行，但记录未保存，请刷新", "Unbanned, but the record was not saved; refresh")})
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
