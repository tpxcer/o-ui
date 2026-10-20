package diagnostics

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogBoundAndIsolation(t *testing.T) {
	l := New()
	for i := 0; i < Limit+17; i++ {
		l.Add([]string{"panel.started", "xray.running"}[i%2])
	}
	s := l.Snapshot()
	if len(s.Entries) != Limit || s.Limit != Limit || s.Dropped != 17 || s.Since.IsZero() || s.Entries[0].Sequence != 18 || s.Entries[Limit-1].Sequence != Limit+17 {
		t.Fatal("日志容量或顺序不正确")
	}
	s.Entries[0].Message = "changed"
	if l.Snapshot().Entries[0].Message == "changed" {
		t.Fatal("快照共享底层内存")
	}
	if len(New().Snapshot().Entries) != 0 {
		t.Fatal("新实例继承旧日志")
	}
	l.Add("arbitrary-secret")
	if l.Snapshot().Dropped != 17 {
		t.Fatal("未知代码进入日志")
	}
	l.Add("panel.started") // 相邻重复摘要在一秒内合并。
	if l.Snapshot().Dropped != 17 {
		t.Fatal("未合并重复摘要")
	}
	l.entries[len(l.entries)-1].At = time.Now().Add(-2 * time.Second)
	l.Add("panel.started")
	if l.Snapshot().Dropped != 18 {
		t.Fatal("一秒后的摘要被永久抑制")
	}
	var absent *Log
	absent.Add("panel.started")
	absent.Operation("POST /api/auth/login", 200)
}

func TestOperationOnlyFixedActionsAndStatus(t *testing.T) {
	l := New()
	for _, status := range []int{200, 400, 503} {
		l.Operation("POST /api/auth/login", status)
	}
	for _, key := range []string{"secret", "GET /api/logs", "POST /api/auth/login?password=sentinel", "POST /api/not-found", "POST /api/auth/login\nCookie: sentinel"} {
		l.Operation(key, 200)
	}
	l.Operation("POST /api/auth/login", 99)
	l.Operation("POST /api/auth/login", 600)
	s := l.Snapshot()
	if len(s.Entries) != 3 {
		t.Fatal("未知操作被记录或不同状态被错误合并")
	}
	for i, level := range []string{"info", "warning", "error"} {
		if s.Entries[i].Level != level || !strings.Contains(s.Entries[i].Message, fmt.Sprintf("HTTP %d", []int{200, 400, 503}[i])) {
			t.Fatal("HTTP状态或级别不正确")
		}
	}
}

func TestLogConcurrent(t *testing.T) {
	l := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l.Add([]string{"panel.started", "xray.running"}[j%2])
				if len(l.Snapshot().Entries) > Limit {
					t.Error("无界日志")
				}
			}
		}()
	}
	wg.Wait()
	s := l.Snapshot()
	for i, e := range s.Entries {
		if i > 0 && e.Sequence <= s.Entries[i-1].Sequence {
			t.Fatal("并发顺序不单调")
		}
	}
}

func TestFilterFixedCategoriesAndNoRawData(t *testing.T) {
	secret := "fixture-secret-<script>-Cookie-UUID-PRIVATE-KEY-/private/path-https://secret.test"
	for _, v := range []struct{ line, code string }{
		{"listen tcp: address already in use", "core.port_busy"},
		{"permission denied", "core.permission"}, {"operation not permitted", "core.permission"},
		{"open no such file or directory", "core.file_missing"},
		{"x509: certificate expired", "core.certificate"}, {"failed to parse private key", "core.certificate"},
		{"failed to load config", "core.config"}, {"invalid UUID", "core.config"},
		{"no such host", "core.dns"}, {"connection refused", "core.network"},
		{"[Warning] unknown", "core.hidden"}, {"panic: unknown", "core.hidden"},
		{"Failed to start: unknown", "core.hidden"},
		{"ordinary access accepted", ""}, {"-----BEGIN PRIVATE KEY-----", ""},
	} {
		l := New()
		var codes []string
		f := NewFilter(func(code string) { codes = append(codes, code); l.Add(code) })
		line := []byte(v.line + " " + secret + "\r\n")
		for _, b := range line {
			if n, err := f.Write([]byte{b}); n != 1 || err != nil {
				t.Fatal("写入失败")
			}
		}
		f.Flush()
		if v.code == "" && len(codes) != 0 || v.code != "" && (len(codes) != 1 || codes[0] != v.code) {
			t.Fatal("错误分类", v.code, codes)
		}
		data, _ := json.Marshal(l.Snapshot())
		if strings.Contains(string(data), secret) || strings.Contains(string(data), "<script>") {
			t.Fatal("原文泄漏")
		}
	}
}

func TestFilterOverflowFlushAndRateLimit(t *testing.T) {
	var codes []string
	f := NewFilter(func(code string) { codes = append(codes, code) })
	_, _ = f.Write([]byte(strings.Repeat("x", 4097)))
	if len(f.line) > 4096 || !f.overflow {
		t.Fatal("超长行未丢弃")
	}
	_, _ = f.Write([]byte("address already in use\npermission denied\npermission denied"))
	f.Flush()
	if strings.Join(codes, ",") != "core.long_line,core.permission" {
		t.Fatal(codes)
	}
	f.last["core.permission"] = time.Now().Add(-2 * time.Second)
	_, _ = f.Write([]byte("permission denied"))
	f.Flush()
	if len(codes) != 3 {
		t.Fatal("末尾或限频恢复失败")
	}
	f.Flush()
	if len(codes) != 3 {
		t.Fatal("空行被记录")
	}
}

func TestFilterConcurrentStreams(t *testing.T) {
	l := New()
	f := NewFilter(l.Add)
	var wg sync.WaitGroup
	for _, line := range []string{"permission denied\n", "connection refused\n", "no such host\n"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, _ = f.Write([]byte(line))
			}
		}()
	}
	wg.Wait()
	f.Flush()
	if len(l.Snapshot().Entries) != 3 {
		t.Fatal("并发或限频丢失不同类别")
	}
}

func TestReceiverRejectsRawUnknownAndOversized(t *testing.T) {
	l := New()
	r := NewReceiver(l)
	for _, line := range []string{"not-json\n", `"panel.started"` + "\n", `"core.injected-secret"` + "\n", strings.Repeat("x", 129) + "\n", `{"code":"core.port_busy"}` + "\n", `"core.permission" extra` + "\n"} {
		_, _ = r.Write([]byte(line))
	}
	_, _ = r.Write([]byte(`"core.permission"`))
	if len(l.Snapshot().Entries) != 0 {
		t.Fatal("无换行或非法输入进入日志")
	}
	_, _ = r.Write([]byte("\n\"core.port_"))
	_, _ = r.Write([]byte("busy\"\n"))
	s := l.Snapshot()
	if len(s.Entries) != 2 || s.Entries[0].Code != "core.permission" || s.Entries[1].Code != "core.port_busy" {
		t.Fatal("合法分片代码未收到")
	}
}
