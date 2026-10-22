package localapi

import (
	"context"
	"fmt"
	"html"
	"independent-panel/internal/i18n"
	"strings"
	"time"
)

// 客户端上线/离线通知（参照OUI的Telegram上线通知）：客户端开始有流量时记一次会话，本次累计满1 MB（OUI为1 MiB，这里与面板十进制单位一致）才通知上线，
// 过滤零星心跳；已通知的会话连续10分钟没有流量才通知离线，短暂空闲不反复提醒。会话只在内存，面板重启后重新计。
const (
	onlineNotifyTraffic = 1_000_000
	offlineNotifyAfter  = 10 * time.Minute
)

type onlineSession struct {
	start     time.Time
	up, down  uint64
	announced bool
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

func onlineDuration(d time.Duration, lang string) string {
	minutes := int(d.Minutes())
	switch {
	case minutes < 1:
		return i18n.Pick(lang, "不到 1 分钟", "under a minute")
	case minutes < 60:
		return fmt.Sprintf(i18n.Pick(lang, "%d 分钟", "%d min"), minutes)
	case minutes < 24*60:
		return fmt.Sprintf(i18n.Pick(lang, "%d 小时 %d 分钟", "%d h %d min"), minutes/60, minutes%60)
	}
	return fmt.Sprintf(i18n.Pick(lang, "%d 天 %d 小时", "%d d %d h"), minutes/(24*60), minutes%(24*60)/60)
}

func onlineMessage(event onlineEvent, inbound string, ips []string, lang string) string {
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	client := say("👤 客户端：", "👤 Client: ") + "<code>" + html.EscapeString(event.client.Name) + "</code>"
	where := say("📍 入站：", "📍 Inbound: ") + "<code>" + html.EscapeString(inbound) + "</code>"
	if event.online {
		lines := []string{say("🚀 <b>o-ui 客户端上线</b>", "🚀 <b>o-ui client online</b>"), client, where}
		if len(ips) > 0 {
			lines = append(lines, say("🌐 IP：", "🌐 IP: ")+"<code>"+html.EscapeString(strings.Join(ips, ", "))+"</code>")
		}
		return strings.Join(append(lines, say("⏰ 上线时间：", "⏰ Online since: ")+event.session.start.Local().Format("2006-01-02 15:04:05")), "\n")
	}
	offline := event.traffic.lastOnline
	up, down := event.traffic.up-event.session.up, event.traffic.down-event.session.down
	return strings.Join([]string{
		say("📴 <b>o-ui 客户端离线</b>", "📴 <b>o-ui client offline</b>"), client, where,
		say("⏱ 在线时长：", "⏱ Online for: ") + onlineDuration(max(offline.Sub(event.session.start), 0), lang),
		say("⏰ 离线时间：", "⏰ Offline at: ") + offline.Local().Format("2006-01-02 15:04:05"),
		fmt.Sprintf(say("📊 本次流量：🔼 %s · 🔽 %s · 合计 %s", "📊 This session: 🔼 %s · 🔽 %s · total %s"), humanBytes(up), humanBytes(down), humanBytes(up+down)),
	}, "\n")
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

func (rt *Runtime) notifyOnline(ctx context.Context, s telegramSettings, sessions map[string]onlineSession, now time.Time) {
	draft, _, traffic, err := rt.telegramData(ctx)
	if err != nil {
		return
	}
	inbounds := map[string]string{}
	for _, in := range draft.Inbounds {
		inbounds[in.ID] = in.Name
	}
	lang := telegramLang()
	for _, event := range onlineChanges(sessions, draft.Clients, traffic, now) {
		var ips []string
		if event.online {
			ips = rt.onlineIPs(event.client.ID)
		}
		_, _ = telegramBroadcast(ctx, s, onlineMessage(event, inbounds[event.client.InboundID], ips, lang))
	}
}
