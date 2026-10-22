package localapi

import (
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

	"independent-panel/internal/backup"
)

const telegramFixtureToken = "123456789:AAFakeTokenForIsolatedTestsOnly_abcdefgh"

type telegramSent struct {
	method, chat, text, document, mode string
	file                               []byte
}

// 只用本地模拟的Telegram接口，不连接真实机器人。
func telegramFixture(t *testing.T) (func() []telegramSent, func(bool)) {
	t.Helper()
	var mu sync.Mutex
	sent, reject := []telegramSent{}, false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot"+telegramFixtureToken+"/") {
			t.Error("unexpected Telegram path")
		}
		mu.Lock()
		defer mu.Unlock()
		if reject {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Bad Request: chat not found `+telegramFixtureToken+`\u0007"}`)
			return
		}
		item := telegramSent{method: strings.TrimPrefix(r.URL.Path, "/bot"+telegramFixtureToken+"/")}
		if item.method == "sendMessage" {
			var body struct {
				ChatID string `json:"chat_id"`
				Text   string `json:"text"`
				Mode   string `json:"parse_mode"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			item.chat, item.text, item.mode = body.ChatID, body.Text, body.Mode
		} else {
			_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			form, err := multipart.NewReader(r.Body, params["boundary"]).ReadForm(16 << 20)
			if err != nil {
				t.Error(err)
				return
			}
			item.chat, item.text, item.mode = form.Value["chat_id"][0], form.Value["caption"][0], form.Value["parse_mode"][0]
			header := form.File["document"][0]
			file, _ := header.Open()
			item.document = header.Filename
			item.file, _ = io.ReadAll(file)
		}
		sent = append(sent, item)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(api.Close)
	previous := telegramAPIBase
	telegramAPIBase = api.URL
	t.Cleanup(func() { telegramAPIBase = previous })
	return func() []telegramSent { mu.Lock(); defer mu.Unlock(); return append([]telegramSent(nil), sent...) },
		func(value bool) { mu.Lock(); reject = value; mu.Unlock() }
}

func TestTelegramSettingsAndNotifications(t *testing.T) {
	sent, reject := telegramFixture(t)
	s, rt, handler, cookie := subscriptionFixture(t)
	call := func(method, path, body string, cookie *http.Cookie, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), telegramFixtureToken) || strings.Contains(w.Body.String(), "backup-passphrase") || strings.ContainsRune(w.Body.String(), 7) {
			t.Fatal("token, backup password or control characters leaked", w.Body.String())
		}
		var result map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	put := func(revision int, data map[string]any, want int) map[string]any {
		body, _ := json.Marshal(map[string]any{"revision": revision, "data": data})
		return call("PUT", "/api/panel/telegram", string(body), cookie, want)
	}
	for _, bad := range []map[string]any{
		{"enabled": true},
		{"token": "not-a-token"},
		{"chatIds": "12a"},
		{"reportTime": "24:00"},
		{"cpuThreshold": 101},
		{"backupEnabled": true, "reportTime": "09:00"},
	} {
		put(0, bad, 400)
	}
	call("POST", "/api/panel/telegram/test", "{}", cookie, 409)
	saved := put(0, map[string]any{"enabled": true, "token": telegramFixtureToken, "chatIds": " 1001 , -2002 ", "reportTime": "09:00", "notifyLogin": true, "cpuThreshold": 90, "backupEnabled": true, "backupPassword": "backup-passphrase-for-tests"}, 200)
	view := saved["data"].(map[string]any)
	if view["tokenSet"] != true || view["backupPasswordSet"] != true || view["chatIds"] != "1001,-2002" {
		t.Fatal("saved view", view)
	}
	// 留空令牌与口令时保留原值。
	view = put(1, map[string]any{"enabled": true, "chatIds": "1001,-2002", "reportTime": "09:00", "notifyLogin": true, "backupEnabled": true}, 200)["data"].(map[string]any)
	if view["tokenSet"] != true || view["backupPasswordSet"] != true {
		t.Fatal("empty secret fields should keep saved values", view)
	}
	if result := call("POST", "/api/panel/telegram/test", "{}", cookie, 200); result["sent"] != float64(2) {
		t.Fatal("test message", result)
	}
	if got := sent(); len(got) != 2 || got[0].chat != "1001" || got[1].chat != "-2002" || !strings.Contains(got[0].text, "o-ui") {
		t.Fatal("test messages", got)
	}
	reject(true)
	if result := call("POST", "/api/panel/telegram/test", "{}", cookie, 502); !strings.Contains(result["error"].(string), "***") {
		t.Fatal("rejection should redact the token", result)
	}
	reject(false)

	before := len(sent())
	call("POST", "/api/auth/login", `{"username":"evil\u0007name","password":"wrong-password-value"}`, nil, 401)
	deadline := time.Now().Add(5 * time.Second)
	for len(sent()) < before+2 {
		if time.Now().After(deadline) {
			t.Fatal("login alert not sent")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if alert := sent()[before].text; !strings.Contains(alert, "失败") || !strings.Contains(alert, "evilname") || !strings.Contains(alert, "127.0.0.1") {
		t.Fatal("login alert", alert)
	}

	before = len(sent())
	settings, _, err := readTelegram(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(24 * time.Hour).UnixMilli()
	data := strings.Replace(string(validWorkspace()), `"expiresAt":null`, `"expiresAt":`+jsonNumber(expires), 1)
	if _, err := s.SaveDocument(context.Background(), "workspace", 1, json.RawMessage(data)); err != nil {
		t.Fatal(err)
	}
	rt.sendTelegramReport(context.Background(), settings, time.Now())
	got := sent()[before:]
	if len(got) != 4 || got[0].method != "sendMessage" || got[2].method != "sendDocument" {
		t.Fatal("report and backup", got)
	}
	for _, part := range []string{"📊 <b>o-ui 每日报告</b>", "📡 Xray：🔴 已停止", "👥 客户端：共 1 个 · 启用 1 个", "⏳ 3 天内到期：client", "🚦 业务流量累计："} {
		if !strings.Contains(got[0].text, part) {
			t.Fatalf("report missing %q:\n%s", part, got[0].text)
		}
	}
	if got[0].mode != "HTML" || got[2].mode != "HTML" || !strings.Contains(got[2].text, "🗄 <b>o-ui 加密备份</b>") {
		t.Fatal("report and backup caption should use HTML formatting", got[0].mode, got[2].mode, got[2].text)
	}
	if !strings.HasSuffix(got[2].document, ".ixpb") {
		t.Fatal("backup filename", got[2].document)
	}
	if _, err := backup.Open(got[2].file, "backup-passphrase-for-tests"); err != nil {
		t.Fatal("sent backup does not decrypt with the configured password", err)
	}
}

func jsonNumber(value int64) string {
	data, _ := json.Marshal(value)
	return string(data)
}
