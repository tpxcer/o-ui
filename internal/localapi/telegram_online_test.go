package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	text := onlineMessage(online, []string{"Reality · TCP/54716"}, "203.0.113.9", "zh")
	if strings.Contains(text, "节点名称") {
		t.Fatal("alerts no longer include the inbound name line", text)
	}
	for _, part := range []string{"💎 <b>o-ui 用户通知</b>\n🚀 <b>客户端上线</b>", "📧 用户/节点：<code>a&lt;b</code>\n🔌 入站：<code>Reality · TCP/54716</code>\n🌐 IP 地址：<code>203.0.113.9</code>", "⏰ 上线时间：<code>"} {
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
	perIP := map[string][]string{"203.0.113.9": {"Reality · TCP/54716"}, "198.51.100.7": {"HY2 · UDP/443"}}
	messages := onlineMessages(offline, []string{"203.0.113.9", "198.51.100.7"}, func(ip string) []string { return perIP[ip] }, "zh")
	if len(messages) != 2 || !strings.Contains(messages[0], "🌐 离线 IP 地址：<code>203.0.113.9</code>") || !strings.Contains(messages[1], "🌐 离线 IP 地址：<code>198.51.100.7</code>") {
		t.Fatal("like OUI, one message per IP", messages)
	}
	if !strings.Contains(messages[0], "🔌 入站：<code>Reality · TCP/54716</code>\n") || strings.Contains(messages[0], "HY2") || !strings.Contains(messages[1], "🔌 入站：<code>HY2 · UDP/443</code>\n") {
		t.Fatal("each IP's alert names only the inbound that IP actually used", messages)
	}
	for _, text := range messages {
		for _, part := range []string{"💎 <b>o-ui 用户通知</b>\n📴 <b>客户端下线</b>", "📧 用户/节点：<code>a&lt;b</code>\n🔌 入站：", "⏱ 在线时长：<code>1分钟30秒</code>", "⏰ 下线时间：<code>", "📊 流量：<code>↑683.59KB / ↓878.91KB / 合计1.53MB</code>"} {
			if !strings.Contains(text, part) {
				t.Fatalf("offline message missing %q:\n%s", part, text)
			}
		}
	}
	if unknown := onlineMessages(offline, nil, func(string) []string { return []string{} }, "zh"); !strings.Contains(unknown[0], "🔌 入站：未能识别") {
		t.Fatal("an undetermined inbound is shown as unknown, not as every linked inbound", unknown)
	}
	if only := onlineMessages(offline, nil, func(string) []string { return nil }, "zh"); len(only) != 1 || strings.Contains(only[0], "IP") || strings.Contains(only[0], "入站") {
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

// 入站写成“类型 · 传输/端口”；关联入站取已启用的，都停用时取全部；已删除的入站不出现。
func TestLinkedInbounds(t *testing.T) {
	draft := workspaceDraft{Inbounds: []inboundDraft{
		{ID: "r", Preset: "reality", Port: 54716, Enabled: true},
		{ID: "h", Preset: "hy2", Port: 443, Enabled: true},
		{ID: "x", Preset: "xhttp-reality", Port: 63094, Enabled: false},
	}}
	cases := []struct {
		client clientDraft
		want   string
	}{
		{clientDraft{InboundID: "r"}, "Reality · TCP/54716"},
		{clientDraft{InboundID: "h", InboundIDs: []string{"h", "x", "r", "gone"}}, "HY2 · UDP/443|Reality · TCP/54716"},
		{clientDraft{InboundID: "x"}, "XHTTP-Reality · TCP/63094"},
	}
	for _, c := range cases {
		labels := []string{}
		for _, in := range linkedInbounds(draft, c.client) {
			labels = append(labels, endpointLabel(in))
		}
		if got := strings.Join(labels, "|"); got != c.want {
			t.Fatalf("%+v: %q != %q", c.client, got, c.want)
		}
	}
}

// 连接表只取ESTABLISHED：IPv4、IPv4映射的IPv6与IPv6对端都还原成标准IP，监听行与表头跳过。
func TestTCPPeers(t *testing.T) {
	dir := t.TempDir()
	tcp := "  sl  local_address rem_address   st\n" +
		"   0: 00000000:D5BC 00000000:0000 0A 00000000:00000000\n" +
		"   1: 0100007F:D5BC 097100CB:C350 01 00000000:00000000\n"
	tcp6 := "  sl  local_address rem_address   st\n" +
		"   0: 00000000000000000000000000000000:E694 0000000000000000FFFF0000076433C6:D431 01 00000000:00000000\n" +
		"   1: 00000000000000000000000000000000:01BB B80D0120000000000000000001000000:D432 01 00000000:00000000\n" +
		"   2: 00000000000000000000000000000000:E694 0000000000000000FFFF0000096433C6:D433 06 00000000:00000000\n"
	_ = os.WriteFile(filepath.Join(dir, "tcp"), []byte(tcp), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "tcp6"), []byte(tcp6), 0o600)
	previous := procNetTCP
	t.Cleanup(func() { procNetTCP = previous })
	procNetTCP = []string{filepath.Join(dir, "tcp"), filepath.Join(dir, "tcp6")}
	peers := tcpPeers()
	if len(peers) != 3 || !peers["203.0.113.9"][54716] || !peers["198.51.100.7"][59028] || !peers["2001:db8::1"][443] {
		t.Fatal("unexpected peers", peers)
	}
	procNetTCP = []string{filepath.Join(dir, "missing")}
	if tcpPeers() != nil {
		t.Fatal("an unreadable connection table must not be treated as no connections")
	}
}

// 多入站客户端只写该IP实际连的入站：有TCP连接的写对应TCP入站；在线却没连任何TCP入站的归到HY2；
// 连接表读不到或判断不出时写“未能识别”，不再列出全部；单入站客户端直接写该入站。
func TestIPInboundsAndMessageInbounds(t *testing.T) {
	linked := []inboundDraft{
		{ID: "x", Preset: "xhttp-reality", Port: 59028, Enabled: true},
		{ID: "h", Preset: "hy2", Port: 62197, Enabled: true},
		{ID: "r", Preset: "reality", Port: 54716, Enabled: true},
	}
	peers := map[string]map[int]bool{"203.0.113.9": {59028: true}, "2001:db8::1": {54716: true}}
	if got := ipInbounds(linked, "203.0.113.9", peers); strings.Join(got, ",") != "x" {
		t.Fatal("TCP connection to the XHTTP port", got)
	}
	if got := ipInbounds(linked, "2001:0db8::0001", peers); strings.Join(got, ",") != "r" {
		t.Fatal("IPv6 addresses are compared in canonical form", got)
	}
	if got := ipInbounds(linked, "198.51.100.7", peers); strings.Join(got, ",") != "h" {
		t.Fatal("online without TCP connections means the HY2 inbound", got)
	}
	if got := ipInbounds(linked, "198.51.100.7", nil); got != nil {
		t.Fatal("no attribution without the connection table", got)
	}
	seen := map[string][]string{"203.0.113.9": {"x"}, "198.51.100.7": {"h"}}
	if got := strings.Join(messageInbounds(linked, seen, "203.0.113.9"), "|"); got != "XHTTP-Reality · TCP/59028" {
		t.Fatal(got)
	}
	if got := strings.Join(messageInbounds(linked, seen, ""), "|"); got != "XHTTP-Reality · TCP/59028|HY2 · UDP/62197" {
		t.Fatal("without an IP, merge every IP's inbounds in link order", got)
	}
	if got := messageInbounds(linked, seen, "192.0.2.1"); got == nil || len(got) != 0 {
		t.Fatal("undetermined inbound is an empty list, shown as unknown", got)
	}
	if got := strings.Join(messageInbounds(linked[2:], nil, "192.0.2.1"), "|"); got != "Reality · TCP/54716" {
		t.Fatal("single-inbound clients always name their inbound", got)
	}
	if messageInbounds(nil, seen, "203.0.113.9") != nil {
		t.Fatal("no linked inbound omits the line")
	}
}
