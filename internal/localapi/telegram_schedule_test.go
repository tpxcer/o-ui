package localapi

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"independent-panel/internal/store"
)

func TestTelegramPeriods(t *testing.T) {
	for _, tc := range []struct {
		date, period string
		due          bool
	}{
		{"2026-10-26", "", true}, {"2026-10-27", "daily", true},
		{"2026-10-26", "weekly", true}, {"2026-10-27", "weekly", false},
		{"2026-11-01", "monthly", true}, {"2026-11-02", "monthly", false},
		{"2028-02-29", "monthly", false}, {"2026-10-26", "yearly", false},
	} {
		now, _ := time.ParseInLocation("2006-01-02", tc.date, time.FixedZone("qa", 8*3600))
		if got := telegramDue(tc.period, now); got != tc.due {
			t.Errorf("%s %s got %v", tc.date, tc.period, got)
		}
	}
	old, err := validateTelegram(telegramSettings{}, telegramSettings{})
	if err != nil || old.ReportPeriod != "daily" || old.BackupPeriod != "daily" {
		t.Fatal("old settings", err)
	}
	for _, s := range []telegramSettings{{ReportPeriod: "yearly"}, {BackupPeriod: "x"}, {ServerName: strings.Repeat("中", 65)}, {ServerName: "bad\nname"}} {
		if _, err := validateTelegram(s, telegramSettings{}); err == nil {
			t.Fatal("invalid setting accepted")
		}
	}
}

func TestTelegramSchedulePersistsAndClaimsOnce(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{state: s}
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rt.claimTelegramSchedule(ctx, "report", now) {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claims=%d", wins.Load())
	}
	if !rt.claimTelegramSchedule(ctx, "backup", now) {
		t.Fatal("backup is independent")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt = &Runtime{state: s}
	if rt.claimTelegramSchedule(ctx, "report", now) || rt.claimTelegramSchedule(ctx, "backup", now) {
		t.Fatal("restart duplicated")
	}
	if rt.claimTelegramSchedule(ctx, "report", now.AddDate(0, 0, -1)) {
		t.Fatal("clock rollback duplicated")
	}
	if !rt.claimTelegramSchedule(ctx, "report", now.AddDate(0, 0, 1)) {
		t.Fatal("next day blocked")
	}
}

func TestTelegramScheduleIndependentDeliveryAndIdentity(t *testing.T) {
	sent, _ := telegramFixture(t)
	_, rt, _, _ := subscriptionFixture(t)
	rt.public.cached = publicInfo{IPv4: &publicAddress{IP: "192.0.2.10"}, IPv6: &publicAddress{IP: "2001:db8::10"}}
	s := telegramSettings{Enabled: true, Token: telegramFixtureToken, ChatIDs: "1001", ServerName: "美国<&01", ReportTime: "09:00", ReportPeriod: "weekly", BackupPeriod: "monthly", BackupEnabled: true}
	ctx := context.Background()
	date := func(day string) time.Time {
		now, _ := time.ParseInLocation("2006-01-02 15:04", day+" 09:00", time.FixedZone("qa", 8*3600))
		return now
	}
	rt.sendScheduledTelegram(ctx, s, date("2026-11-01")) // 周日、月初，仅备份。
	got := sent()
	if len(got) != 1 || got[0].method != "sendDocument" {
		t.Fatal("monthly backup independent", len(got))
	}
	rt.sendScheduledTelegram(ctx, s, date("2026-11-01"))
	if len(sent()) != 1 {
		t.Fatal("duplicate backup")
	}
	rt.sendScheduledTelegram(ctx, s, date("2026-11-02")) // 周一，只有报告。
	got = sent()
	if len(got) != 2 || got[1].method != "sendMessage" {
		t.Fatal("weekly report independent", len(got))
	}
	for _, message := range got {
		for _, part := range []string{"美国&lt;&amp;01", "服务器公网 IP：", "192.0.2.10", "2001:db8::10"} {
			if !strings.Contains(message.text, part) {
				t.Errorf("identity missing %s", part)
			}
		}
	}
	rt.sendScheduledTelegram(ctx, s, date("2026-11-03"))
	if len(sent()) != 2 {
		t.Fatal("non-due day sent")
	}
	s.Enabled = false
	rt.sendScheduledTelegram(ctx, s, date("2026-12-01"))
	if len(sent()) != 2 {
		t.Fatal("disabled sent")
	}
	s.Enabled = true
	s.ReportTime = "08:00"
	rt.sendScheduledTelegram(ctx, s, date("2026-12-01"))
	if len(sent()) != 2 {
		t.Fatal("wrong clock sent")
	}
	encoded, _ := json.Marshal(rt.telegramIdentity(s))
	if strings.Contains(string(encoded), "192.0.2.10") {
		t.Fatal("transient IP persisted")
	}
	host, _ := os.Hostname()
	fallback := (telegramSettings{}).identify("test")
	if !strings.Contains(fallback, cleanPublicText(host)) || !strings.Contains(fallback, "未知") {
		t.Fatal("hostname fallback")
	}
	rt.public.mu.Lock()
	done := make(chan struct{})
	go func() { rt.telegramIdentity(s); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("identity waited for lookup")
	}
	rt.public.mu.Unlock()
}
