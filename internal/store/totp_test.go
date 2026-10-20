package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestTOTPMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"} {
		if got := totpCode(secret, unix/30); got != want {
			t.Fatal("RFC 6238 vector mismatch", unix, got, want)
		}
	}
	encoded := totpEncoding.EncodeToString(secret)
	now := time.Unix(1111111109, 0)
	for _, tc := range []struct {
		code     string
		lastStep int64
		ok       bool
	}{
		{"081804", 0, true}, {totpCode(secret, now.Unix()/30-1), 0, true}, {totpCode(secret, now.Unix()/30+1), 0, true},
		{totpCode(secret, now.Unix()/30-2), 0, false}, {"081804", now.Unix() / 30, false}, {"81804", 0, false}, {"0818045", 0, false}, {"abcdef", 0, false},
	} {
		if _, ok := verifyTOTP(encoded, tc.code, now, tc.lastStep); ok != tc.ok {
			t.Fatal("verification mismatch", tc)
		}
	}
}

func currentCode(t *testing.T, secret string, now time.Time) string {
	t.Helper()
	key, err := totpEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totpCode(key, now.Unix()/30)
}

func TestTwoFactorLoginFlow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	clock := time.Unix(1800000000, 0)
	s.now = func() time.Time { return clock }
	session, err := s.Initialize(ctx, "admin", "first-password")
	if err != nil {
		t.Fatal(err)
	}
	if on, err := s.TwoFactorEnabled(ctx); err != nil || on {
		t.Fatal("two-factor enabled by default", on, err)
	}
	if _, _, err := s.BeginTwoFactor(ctx, "not-a-session-token-0000000000000000000000"); !errors.Is(err, ErrSession) {
		t.Fatal("setup without a session accepted", err)
	}
	secret, name, err := s.BeginTwoFactor(ctx, session.Token)
	if err != nil || name != "admin" || len(secret) != 32 {
		t.Fatal("setup secret", secret, name, err)
	}
	// 待确认的密钥不影响登录
	if _, err := s.Login(ctx, "admin", "first-password"); err != nil {
		t.Fatal("pending secret blocked sign-in", err)
	}
	if err := s.EnableTwoFactor(ctx, session.Token, "000000"); !errors.Is(err, ErrTwoFactorCode) {
		t.Fatal("wrong enable code accepted", err)
	}
	if err := s.EnableTwoFactor(ctx, session.Token, currentCode(t, secret, clock)); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.TwoFactorEnabled(ctx); !on {
		t.Fatal("two-factor not enabled")
	}
	if _, _, err := s.BeginTwoFactor(ctx, session.Token); !errors.Is(err, ErrTwoFactorState) {
		t.Fatal("re-setup while enabled", err)
	}
	// 启用时用过的时间步不能再用于登录
	if _, err := s.LoginWithCode(ctx, "admin", "first-password", currentCode(t, secret, clock)); !errors.Is(err, ErrTwoFactorCredentials) {
		t.Fatal("enable code replayed for sign-in", err)
	}
	if _, err := s.Login(ctx, "admin", "first-password"); !errors.Is(err, ErrTwoFactorCredentials) {
		t.Fatal("sign-in without code accepted", err)
	}
	clock = clock.Add(30 * time.Second)
	if _, err := s.LoginWithCode(ctx, "admin", "wrong-password", currentCode(t, secret, clock)); !errors.Is(err, ErrTwoFactorCredentials) {
		t.Fatal("wrong password with valid code", err)
	}
	code := currentCode(t, secret, clock)
	signed, err := s.LoginWithCode(ctx, "admin", "first-password", code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginWithCode(ctx, "admin", "first-password", code); !errors.Is(err, ErrTwoFactorCredentials) {
		t.Fatal("code replayed within its time step", err)
	}
	clock = clock.Add(30 * time.Second)
	if err := s.ChangeAccount(ctx, signed.Token, "first-password", "", "second-password"); !errors.Is(err, ErrTwoFactorCode) {
		t.Fatal("account change without code", err)
	}
	clock = clock.Add(15 * time.Minute) // 上面的失败计入限速，等待窗口结束
	if err := s.ChangeAccountWithCode(ctx, signed.Token, "first-password", "", "second-password", currentCode(t, secret, clock)); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	again, err := s.LoginWithCode(ctx, "admin", "second-password", currentCode(t, secret, clock))
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	if err := s.DisableTwoFactor(ctx, again.Token, "123456"); !errors.Is(err, ErrTwoFactorCode) {
		t.Fatal("wrong disable code accepted", err)
	}
	if err := s.DisableTwoFactor(ctx, again.Token, currentCode(t, secret, clock)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "admin", "second-password"); err != nil {
		t.Fatal("sign-in after disabling", err)
	}
	// 离线重置管理员同时清除两步验证，作为丢失验证器时的恢复途径
	third, _ := s.Login(ctx, "admin", "second-password")
	secret, _, _ = s.BeginTwoFactor(ctx, third.Token)
	clock = clock.Add(30 * time.Second)
	if err := s.EnableTwoFactor(ctx, third.Token, currentCode(t, secret, clock)); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetAdministrator(ctx, "admin", "reset-password"); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.TwoFactorEnabled(ctx); on {
		t.Fatal("offline reset kept two-factor")
	}
	if _, err := s.Login(ctx, "admin", "reset-password"); err != nil {
		t.Fatal(err)
	}
}

func TestTwoFactorFailuresAreThrottled(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	session, _ := s.Initialize(ctx, "admin", "first-password")
	secret, _, _ := s.BeginTwoFactor(ctx, session.Token)
	for i := 0; i < 5; i++ {
		if err := s.EnableTwoFactor(ctx, session.Token, "000000"); !errors.Is(err, ErrTwoFactorCode) {
			t.Fatal(err)
		}
	}
	if err := s.EnableTwoFactor(ctx, session.Token, currentCode(t, secret, time.Now())); !errors.Is(err, ErrLimited) {
		t.Fatal("throttle bypassed by two-factor codes", err)
	}
}

func TestSessionLifetimeSetting(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	clock := time.Unix(1800000000, 0)
	s.now = func() time.Time { return clock }
	first, _ := s.Initialize(ctx, "admin", "first-password")
	if first.ExpiresAt != clock.Add(DefaultSessionMinutes*time.Minute) {
		t.Fatal("default lifetime", first.ExpiresAt)
	}
	revision := int64(0)
	for _, tc := range []struct {
		minutes int
		want    time.Duration
	}{{30, 30 * time.Minute}, {MaxSessionMinutes, MaxSessionMinutes * time.Minute}, {1, DefaultSessionMinutes * time.Minute}, {MaxSessionMinutes + 1, DefaultSessionMinutes * time.Minute}} {
		data, _ := json.Marshal(map[string]int{"minutes": tc.minutes})
		doc, err := s.SaveDocument(ctx, SessionDocument, revision, data)
		if err != nil {
			t.Fatal(err)
		}
		revision = doc.Revision
		session, err := s.Login(ctx, "admin", "first-password")
		if err != nil || session.ExpiresAt != clock.Add(tc.want) {
			t.Fatal("lifetime mismatch", tc.minutes, session.ExpiresAt, err)
		}
		if got, err := s.Authenticate(ctx, session.Token); err != nil || !got.ExpiresAt.Equal(clock.Add(tc.want)) {
			t.Fatal("stored expiry mismatch", got, err)
		}
	}
}
