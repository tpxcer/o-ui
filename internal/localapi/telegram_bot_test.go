package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type botCall struct {
	method string
	body   map[string]any
}

// botFixture模拟Telegram Bot API：getUpdates依次返回预设批次，其它调用只记录。
func botFixture(t *testing.T, batches ...[]map[string]any) func() []botCall {
	t.Helper()
	var mu sync.Mutex
	calls := []botCall{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bot"+telegramFixtureToken+"/")
		var body map[string]any
		data, _ := io.ReadAll(r.Body)
		if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && params["boundary"] != "" {
			form, err := multipart.NewReader(bytes.NewReader(data), params["boundary"]).ReadForm(16 << 20)
			if err != nil {
				t.Error(err)
				return
			}
			body = map[string]any{}
			for key, values := range form.Value {
				body[key] = values[0]
			}
		} else {
			_ = json.Unmarshal(data, &body)
		}
		mu.Lock()
		calls = append(calls, botCall{method, body})
		result := any(true)
		if method == "getUpdates" {
			result = []map[string]any{}
			if len(batches) > 0 {
				result, batches = batches[0], batches[1:]
			} else {
				time.Sleep(20 * time.Millisecond)
			}
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(api.Close)
	previous := telegramAPIBase
	telegramAPIBase = api.URL
	t.Cleanup(func() { telegramAPIBase = previous })
	return func() []botCall { mu.Lock(); defer mu.Unlock(); return append([]botCall(nil), calls...) }
}

func message(id int64, chat int64, text string) map[string]any {
	return map[string]any{"update_id": id, "message": map[string]any{"chat": map[string]any{"id": chat}, "text": text}}
}

func callback(id int64, chat int64, data string) map[string]any {
	return map[string]any{"update_id": id, "callback_query": map[string]any{"id": "cb", "data": data, "message": map[string]any{"chat": map[string]any{"id": chat}}}}
}

func lastReply(t *testing.T, calls []botCall) (string, string) {
	t.Helper()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].method == "sendMessage" {
			keyboard, _ := json.Marshal(calls[i].body["reply_markup"])
			return calls[i].body["text"].(string), string(keyboard)
		}
	}
	t.Fatal("no reply sent")
	return "", ""
}

func saveBotSettings(t *testing.T, rt *Runtime) telegramSettings {
	t.Helper()
	s := telegramSettings{Enabled: true, Token: telegramFixtureToken, ChatIDs: "1001"}
	data, _ := json.Marshal(s)
	if _, err := rt.state.SaveDocument(context.Background(), telegramDocument, 0, data); err != nil {
		t.Fatal(err)
	}
	return s
}

// 只有设置里的管理员Chat能用菜单和操作；/id对任何聊天可用，便于配置。
func TestTelegramBotAdminMenuAndActions(t *testing.T) {
	calls := botFixture(t)
	_, rt, _, _ := subscriptionFixture(t)
	s := saveBotSettings(t, rt)
	ctx := context.Background()
	handle := func(update map[string]any) {
		data, _ := json.Marshal(update)
		var u tgUpdate
		if err := json.Unmarshal(data, &u); err != nil {
			t.Fatal(err)
		}
		rt.handleTelegram(ctx, s, u)
	}

	handle(message(1, 2002, "/id"))
	if text, _ := lastReply(t, calls()); !strings.Contains(text, "2002") {
		t.Fatal("/id should answer any chat", text)
	}
	handle(message(2, 2002, "/start"))
	if text, keyboard := lastReply(t, calls()); !strings.Contains(text, "2002") || keyboard != "null" {
		t.Fatal("non-admin must not get the menu", text, keyboard)
	}
	before := len(calls())
	handle(callback(3, 2002, "ct:client-one"))
	for _, call := range calls()[before:] {
		if call.method == "sendMessage" {
			t.Fatal("non-admin callback must be ignored")
		}
	}

	handle(message(4, 1001, "/start"))
	if last := calls()[len(calls())-1]; last.body["parse_mode"] != "HTML" {
		t.Fatal("bot replies should use HTML formatting", last.body)
	}
	if _, keyboard := lastReply(t, calls()); !strings.Contains(keyboard, `"rk"`) || !strings.Contains(keyboard, `"cl:0"`) || !strings.Contains(keyboard, `"qc"`) {
		t.Fatal("admin menu missing buttons", keyboard)
	}
	handle(callback(5, 1001, "cl:0"))
	if _, keyboard := lastReply(t, calls()); !strings.Contains(keyboard, `"c:client-one"`) {
		t.Fatal("client list should link to the client", keyboard)
	}
	handle(callback(6, 1001, "c:client-one"))
	if text, keyboard := lastReply(t, calls()); !strings.Contains(text, "client") || !strings.Contains(keyboard, `"ct:client-one"`) {
		t.Fatal("client detail missing", text, keyboard)
	}
	handle(callback(7, 1001, "ct:client-one"))
	doc, _ := rt.state.ReadDocument(ctx, "workspace")
	var draft workspaceDraft
	_ = json.Unmarshal(doc.Data, &draft)
	if draft.Clients[0].Enabled {
		t.Fatal("toggle should disable the client and save the workspace")
	}
	for _, action := range []string{"rk", "on", "ds", "ib", "st"} {
		handle(callback(8, 1001, action))
		if text, _ := lastReply(t, calls()); text == "" {
			t.Fatal("empty reply for", action)
		}
	}
	answered := 0
	for _, call := range calls() {
		if call.method == "answerCallbackQuery" {
			answered++
		}
	}
	if answered < 8 {
		t.Fatal("callbacks should be answered", answered)
	}
}

// 启动时跳过积压的旧消息（不重放旧操作），之后正常处理新消息。
func TestTelegramBotSkipsBacklog(t *testing.T) {
	calls := botFixture(t, []map[string]any{message(50, 1001, "/start")}, []map[string]any{callback(51, 1001, "on")})
	_, rt, _, _ := subscriptionFixture(t)
	saveBotSettings(t, rt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.RunTelegramBot(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		replies := []string{}
		for _, call := range calls() {
			if call.method == "sendMessage" {
				replies = append(replies, call.body["text"].(string))
			}
		}
		if len(replies) > 0 {
			if len(replies) != 1 || strings.Contains(replies[0], "o-ui 管理菜单") {
				t.Fatal("backlog /start must be skipped; only the new callback answered", replies)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new update was not handled")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	first := calls()[0]
	if first.method != "getUpdates" || first.body["offset"] != float64(-1) {
		t.Fatal("first call should read only the latest update to skip the backlog", first)
	}
}

// 机器人一键创建与网页一致：名称“地区-类型-日期”，同名加(1)；分享主机无证书时用公网IP；REALITY目标默认www.amd.com。
func TestTelegramBotQuickCreate(t *testing.T) {
	_, rt, _, _ := subscriptionFixture(t)
	rt.public.fetch = func(_ context.Context, network string) (*publicAddress, error) {
		if network == "tcp6" {
			return nil, context.DeadlineExceeded
		}
		return &publicAddress{IP: "192.0.2.10", Country: "TW"}, nil
	}
	ctx := context.Background()
	if _, _, _, err := rt.botQuickCreate(ctx, "hy2", "zh"); err == nil || !strings.Contains(err.Error(), "面板证书") {
		t.Fatal("TLS presets need the panel certificate", err)
	}
	date := time.Now().Format("2006.01.02")
	first, firstClient, _, err := rt.botQuickCreate(ctx, "reality", "zh")
	if err != nil || first != "台湾-Reality-"+date {
		t.Fatal(first, err)
	}
	second, _, _, err := rt.botQuickCreate(ctx, "reality", "zh")
	if err != nil || second != "台湾-Reality-"+date+"(1)" {
		t.Fatal("same type and day should get a (1) suffix", second, err)
	}
	doc, _ := rt.state.ReadDocument(ctx, "workspace")
	var draft workspaceDraft
	if err := json.Unmarshal(doc.Data, &draft); err != nil {
		t.Fatal(err)
	}
	created := draft.Inbounds[len(draft.Inbounds)-2]
	if created.ShareHost != "192.0.2.10" || created.Listen != "0.0.0.0" || created.Connection.Target != "www.amd.com:443" || created.Port < quickPortMin || created.Port > quickPortMax {
		t.Fatal("unexpected quick-created inbound", created)
	}
	if client, ok := findClient(draft, firstClient); !ok || client.InboundID != created.ID || !client.Subscription || !client.Enabled {
		t.Fatal("quick create should add one enabled client", client, ok)
	}
}

// 立即备份：没有口令时提示设置；有口令时发送加密文件，说明用HTML排版并附返回菜单按钮，菜单不会“断掉”。
func TestTelegramBotBackupKeepsMenu(t *testing.T) {
	calls := botFixture(t)
	_, rt, _, _ := subscriptionFixture(t)
	s := saveBotSettings(t, rt)
	ctx := context.Background()
	rt.telegramAction(ctx, s, "1001", "bk", "zh")
	if text, keyboard := lastReply(t, calls()); !strings.Contains(text, "备份加密口令") || !strings.Contains(keyboard, `"m"`) {
		t.Fatal("backup without a password should ask for one", text, keyboard)
	}
	s.BackupPassword = "backup-passphrase-for-tests"
	rt.telegramAction(ctx, s, "1001", "bk", "zh")
	last := calls()[len(calls())-1]
	caption, _ := last.body["caption"].(string)
	markup, _ := last.body["reply_markup"].(string)
	if last.method != "sendDocument" || last.body["chat_id"] != "1001" || last.body["parse_mode"] != "HTML" || !strings.Contains(caption, "o-ui 加密备份") || !strings.Contains(caption, "备份时间") || !strings.Contains(markup, `"m"`) {
		t.Fatal("backup should be sent as a document with a caption and the menu button", last)
	}
}

// 整数部分的0不能被当作小数尾零删掉（700 KB曾显示成7 KB）。
func TestHumanBytesKeepsWholeNumbers(t *testing.T) {
	for n, want := range map[uint64]string{219: "219 B", 700_000: "700 KB", 250_000_000: "250 MB", 100e9: "100 GB", 10_000_000: "10 MB", 1_500_000: "1.5 MB", 2_850_000: "2.85 MB", 1_000_000: "1 MB"} {
		if got := humanBytes(n); got != want {
			t.Fatalf("%d: %q != %q", n, got, want)
		}
	}
}
