package localapi

import (
	"context"
	"encoding/json"
	"html"
	"independent-panel/internal/i18n"
	"net"
	"os"
	"strings"
	"time"
)

func telegramPeriod(period string) string {
	if period == "" {
		return "daily"
	}
	return period
}

func validTelegramPeriod(period string) bool {
	return period == "daily" || period == "weekly" || period == "monthly"
}

func telegramDue(period string, now time.Time) bool {
	switch telegramPeriod(period) {
	case "daily":
		return true
	case "weekly":
		return now.Weekday() == time.Monday
	case "monthly":
		return now.Day() == 1
	}
	return false
}

// 先持久化领取再发送，维持至多一次语义；重启或修改周期不重发当日任务。
func (rt *Runtime) claimTelegramSchedule(ctx context.Context, kind string, now time.Time) bool {
	doc, err := rt.state.ReadDocument(ctx, "telegram-schedule")
	if err != nil {
		return false
	}
	last := map[string]string{}
	if len(doc.Data) > 0 && string(doc.Data) != "null" && json.Unmarshal(doc.Data, &last) != nil {
		return false
	}
	if last == nil {
		last = map[string]string{}
	}
	date := now.Format("2006-01-02")
	if last[kind] >= date {
		return false
	}
	last[kind] = date
	data, _ := json.Marshal(last)
	_, err = rt.state.SaveDocument(ctx, "telegram-schedule", doc.Revision, data)
	return err == nil
}

func (rt *Runtime) sendScheduledTelegram(ctx context.Context, s telegramSettings, now time.Time) {
	if !s.Enabled || s.ReportTime == "" || now.Format("15:04") != s.ReportTime {
		return
	}
	if telegramDue(s.ReportPeriod, now) && rt.claimTelegramSchedule(ctx, "report", now) {
		rt.sendTelegramReport(ctx, s, now)
	}
	if s.BackupEnabled && telegramDue(s.BackupPeriod, now) && rt.claimTelegramSchedule(ctx, "backup", now) {
		rt.sendTelegramBackup(ctx, s, now)
	}
}

// 只读现有公网缓存，登录提醒与持锁的机器人操作不会等待外部查询。
func (rt *Runtime) telegramIdentity(s telegramSettings) telegramSettings {
	if rt == nil || rt.public == nil {
		return s
	}
	if !rt.public.mu.TryLock() {
		return s
	}
	defer rt.public.mu.Unlock()
	addresses := []string{}
	for _, address := range []*publicAddress{rt.public.cached.IPv4, rt.public.cached.IPv6} {
		if address != nil {
			if ip := net.ParseIP(address.IP); ip != nil {
				addresses = append(addresses, ip.String())
			}
		}
	}
	s.serverIP = strings.Join(addresses, " / ")
	return s
}

func (s telegramSettings) identify(text string) string {
	name := s.ServerName
	if name == "" {
		name, _ = os.Hostname()
	}
	name = cleanPublicText(name)
	if name == "" {
		name = "o-ui"
	}
	ip := s.serverIP
	if ip == "" {
		ip = i18n.Pick(telegramLang(), "未知", "Unknown")
	}
	return i18n.Pick(telegramLang(), "服务器：", "Server: ") + "<b>" + html.EscapeString(name) + "</b>\n" +
		i18n.Pick(telegramLang(), "服务器公网 IP：", "Server public IP: ") + "<code>" + html.EscapeString(ip) + "</code>\n\n" + text
}
