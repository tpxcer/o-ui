package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 参照OUI：本次满1 MB才通知上线且只通知一次；连续10分钟无流量才通知离线，附在线时长与本次流量；
// 停用的客户端直接结束会话不通知；累计被重置时重设基线，不因无符号数下溢误报。
func TestOnlineChangesAnnouncesOnceAndOffline(t *testing.T) {
	start := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	clients := []clientDraft{{ID: "a", Name: "a<b", Enabled: true}, {ID: "b", Name: "idle", Enabled: true}}
	sessions := map[string]onlineSession{}
	snap := func(up, down uint64, last, now time.Time) map[string]trafficSnapshot {
		return map[string]trafficSnapshot{"a": {up: up, down: down, lastOnline: last, online: now.Sub(last) <= 2*time.Minute}}
	}
	step := func(now time.Time, traffic map[string]trafficSnapshot, want int) []onlineEvent {
		t.Helper()
		events := onlineChanges(sessions, clients, traffic, now)
		if len(events) != want {
			t.Fatalf("at %s want %d events, got %+v", now.Sub(start), want, events)
		}
		return events
	}

	step(start, snap(100, 200, start, start), 0)
	if _, ok := sessions["a"]; !ok || len(sessions) != 1 {
		t.Fatal("first traffic should open a session only for the active client", sessions)
	}
	now := start.Add(30 * time.Second)
	step(now, snap(100, 200+999_999, now, now), 0)
	now = start.Add(time.Minute)
	online := step(now, snap(100+500_000, 200+600_000, now, now), 1)[0]
	if !online.online || !online.session.start.Equal(start) {
		t.Fatal("1 MB in this session should announce online once", online)
	}
	text := onlineMessage(online, "203.0.113.9", "zh")
	if strings.Contains(text, "节点名称") {
		t.Fatal("alerts no longer include the inbound name line", text)
	}
	for _, part := range []string{"💎 <b>o-ui 用户通知</b>\n🚀 <b>客户端上线</b>", "📧 用户/节点：<code>a&lt;b</code>", "🌐 IP 地址：<code>203.0.113.9</code>", "⏰ 上线时间：<code>"} {
		if !strings.Contains(text, part) {
			t.Fatalf("online message missing %q:\n%s", part, text)
		}
	}
	last := start.Add(90 * time.Second)
	step(last, snap(100+700_000, 200+900_000, last, last), 0)
	step(last.Add(9*time.Minute), snap(100+700_000, 200+900_000, last, last.Add(9*time.Minute)), 0)
	offline := step(last.Add(10*time.Minute), snap(100+700_000, 200+900_000, last, last.Add(10*time.Minute)), 1)[0]
	if offline.online || len(sessions) != 0 {
		t.Fatal("10 minutes without traffic should announce offline and end the session", offline, sessions)
	}
	messages := onlineMessages(offline, []string{"203.0.113.9", "198.51.100.7"}, "zh")
	if len(messages) != 2 || !strings.Contains(messages[0], "🌐 离线 IP 地址：<code>203.0.113.9</code>") || !strings.Contains(messages[1], "🌐 离线 IP 地址：<code>198.51.100.7</code>") {
		t.Fatal("like OUI, one message per IP", messages)
	}
	for _, text := range messages {
		for _, part := range []string{"💎 <b>o-ui 用户通知</b>\n📴 <b>客户端下线</b>", "📧 用户/节点：<code>a&lt;b</code>", "⏱ 在线时长：<code>1分钟30秒</code>", "⏰ 下线时间：<code>", "📊 流量：<code>↑683.59KB / ↓878.91KB / 合计1.53MB</code>"} {
			if !strings.Contains(text, part) {
				t.Fatalf("offline message missing %q:\n%s", part, text)
			}
		}
	}
	if only := onlineMessages(offline, nil, "zh"); len(only) != 1 || strings.Contains(only[0], "IP") {
		t.Fatal("without sampled IPs send one message without the IP line", only)
	}

	now = start.Add(time.Hour)
	step(now, snap(5_000_000, 5_000_000, now, now), 0)
	step(now.Add(time.Second), snap(0, 10, now.Add(time.Second), now.Add(time.Second)), 0)
	if s := sessions["a"]; s.up != 0 || s.down != 10 || s.announced {
		t.Fatal("a reset counter should reset the baseline", s)
	}
	clients[0].Enabled = false
	step(now.Add(2*time.Second), snap(0, 5_000_000, now.Add(2*time.Second), now.Add(2*time.Second)), 0)
	if len(sessions) != 0 {
		t.Fatal("disabled clients should end their session silently", sessions)
	}
}

// 会话期间出现过的IP按顺序去重保留，离线时连接已断开也能列出；最多8个。
func TestMergeIPs(t *testing.T) {
	ips := mergeIPs(nil, []string{"203.0.113.9"})
	ips = mergeIPs(ips, []string{"203.0.113.9", "198.51.100.7"})
	if fmt.Sprint(ips) != "[203.0.113.9 198.51.100.7]" {
		t.Fatal(ips)
	}
	for i := range 20 {
		ips = mergeIPs(ips, []string{fmt.Sprintf("192.0.2.%d", i)})
	}
	if len(ips) != 8 {
		t.Fatal("keep at most 8 IPs", ips)
	}
}

// 流量写法与OUI一致，如截图中的↑1.41MB / ↓518.78KB。
func TestOUITraffic(t *testing.T) {
	for n, want := range map[uint64]string{0: "0.00B", 1023: "1023.00B", 531_231: "518.78KB", 1_478_492: "1.41MB", 5 << 30: "5.00GB"} {
		if got := ouiTraffic(n); got != want {
			t.Fatalf("%d: %q != %q", n, got, want)
		}
	}
}

func TestOnlineDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{-time.Second: "0秒", 30 * time.Second: "30秒", 10*time.Minute + 19*time.Second: "10分钟19秒", 125 * time.Minute: "2小时5分钟0秒", 50 * time.Hour: "50小时0分钟0秒"} {
		if got := onlineDuration(d, "zh"); got != want {
			t.Fatalf("%s: %q != %q", d, got, want)
		}
	}
}

// 上线/离线通知按客户端单独开启（用户2026-10-22要求）：开关存独立文档，不保存入站草稿（否则会重启Xray）；
// 不存在的客户端拒绝，已删除客户端的旧开关不返回；botEnabled反映机器人能否发送。
func TestTelegramNotifyPerClient(t *testing.T) {
	s, rt, handler, cookie := subscriptionFixture(t)
	call := func(method, body string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "/api/panel/telegram/notify", strings.NewReader(body))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body)
		}
		var result map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	ctx := context.Background()
	before, _ := s.ReadDocument(ctx, "workspace")
	if view := call("GET", "", 200); fmt.Sprint(view["clients"]) != "[]" || view["botEnabled"] != false {
		t.Fatal("alerts start off for every client", view)
	}
	call("PUT", `{"clientId":"missing","enabled":true}`, 404)
	if view := call("PUT", `{"clientId":"client-one","enabled":true}`, 200); fmt.Sprint(view["clients"]) != "[client-one]" {
		t.Fatal("enable one client", view)
	}
	if after, _ := s.ReadDocument(ctx, "workspace"); after.Revision != before.Revision {
		t.Fatal("toggling alerts must not save the inbound draft, which would restart Xray")
	}
	saveBotSettings(t, rt)
	if view := call("GET", "", 200); view["botEnabled"] != true {
		t.Fatal("botEnabled should follow the saved bot settings", view)
	}
	doc, _ := s.ReadDocument(ctx, telegramNotifyDocument)
	if _, err := s.SaveDocument(ctx, telegramNotifyDocument, doc.Revision, json.RawMessage(`{"clients":["client-one","deleted-client"]}`)); err != nil {
		t.Fatal(err)
	}
	if view := call("GET", "", 200); fmt.Sprint(view["clients"]) != "[client-one]" {
		t.Fatal("alerts of deleted clients are not returned", view)
	}
	if view := call("PUT", `{"clientId":"client-one","enabled":false}`, 200); fmt.Sprint(view["clients"]) != "[]" {
		t.Fatal("disable the client", view)
	}
	if set, _, _ := readTelegramNotify(ctx, s); len(set) != 0 {
		t.Fatal("saving drops stale and disabled entries", set)
	}
}
