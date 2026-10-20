package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const changedPassword = "different-test-only-passphrase-825"

func TestPasswordChangeInvalidatesAndPersists(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	first, err := s.Initialize(ctx, "tester", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Login(ctx, "tester", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveDocument(ctx, "workspace", 0, json.RawMessage(`{"preserved":true}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, first.Token, testPassword, changedPassword); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first.Token, second.Token} {
		if _, err = s.Authenticate(ctx, token); !errors.Is(err, ErrSession) {
			t.Fatal("old session survived", err)
		}
	}
	var count, version int
	if err = s.db.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 2 {
		t.Fatal("sessions were deleted instead of invalidated", count, err)
	}
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("schema changed", err)
	}
	_ = s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Login(ctx, "tester", testPassword); !errors.Is(err, ErrCredentials) {
		t.Fatal("old password accepted", err)
	}
	next, err := s.Login(ctx, "tester", changedPassword)
	if err != nil || next.Username != "tester" {
		t.Fatal("new login failed", err)
	}
	if doc, err := s.ReadDocument(ctx, "workspace"); err != nil || doc.Revision != 1 || string(doc.Data) != `{"preserved":true}` {
		t.Fatal("business data changed", err)
	}
	_ = s.Close()
	data, err := os.ReadFile(filepath.Join(root, "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testPassword, changedPassword, first.Token, second.Token, next.Token} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("plaintext credential persisted")
		}
	}
}

func TestPasswordChangeFailuresPreserveAccess(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	first, err := s.Initialize(ctx, "tester", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, "invalid", testPassword, changedPassword); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, strings.Repeat("a", 43), testPassword, changedPassword); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	for _, password := range []string{"short", strings.Repeat("a", 73), strings.Repeat("密", 25)} {
		if err = s.ChangePassword(ctx, first.Token, testPassword, password); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if err = s.ChangePassword(ctx, first.Token, testPassword, testPassword); !errors.Is(err, ErrSamePassword) {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, first.Token, "wrong-test-password", changedPassword); !errors.Is(err, ErrCurrentPassword) {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, first.Token); err != nil {
		t.Fatal("failed change revoked session", err)
	}
	if _, err = s.Login(ctx, "tester", testPassword); err != nil {
		t.Fatal("failed change changed password", err)
	}
	s.now = func() time.Time { return first.ExpiresAt }
	if err = s.ChangePassword(ctx, first.Token, testPassword, changedPassword); !errors.Is(err, ErrSession) {
		t.Fatal("expired session accepted", err)
	}
}

func TestPasswordChangeTransactionRollback(t *testing.T) {
	for _, table := range []string{"administrator", "sessions", "auth_throttle"} {
		t.Run(table, func(t *testing.T) {
			s, ctx := openTest(t), context.Background()
			session, err := s.Initialize(ctx, "tester", testPassword)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("CREATE TRIGGER reject_password_change BEFORE UPDATE ON " + table + " BEGIN SELECT RAISE(ABORT, 'test change failure'); END"); err != nil {
				t.Fatal(err)
			}
			if err = s.ChangePassword(ctx, session.Token, testPassword, changedPassword); err == nil {
				t.Fatal("injected failure accepted")
			}
			if _, err = s.Authenticate(ctx, session.Token); err != nil {
				t.Fatal("session changed despite rollback", err)
			}
			if _, err = s.db.Exec("DROP TRIGGER reject_password_change"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Login(ctx, "tester", testPassword); err != nil {
				t.Fatal("password changed despite rollback", err)
			}
			if _, err = s.Login(ctx, "tester", changedPassword); !errors.Is(err, ErrCredentials) {
				t.Fatal("new password survived rollback", err)
			}
		})
	}
}

func TestPasswordChangeThrottleSurvivesRestart(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	session, err := s.Initialize(ctx, "tester", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err = s.ChangePassword(ctx, session.Token, "wrong-test-password", changedPassword); !errors.Is(err, ErrCurrentPassword) {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, session.Token, testPassword, changedPassword); !errors.Is(err, ErrLimited) {
		t.Fatal(err)
	}
	if _, err = s.Login(ctx, "tester", testPassword); !errors.Is(err, ErrLimited) {
		t.Fatal("throttle not shared", err)
	}
	s.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	if err = s.ChangePassword(ctx, session.Token, testPassword, changedPassword); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPasswordChange(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	session, err := s.Initialize(ctx, "tester", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			err := s.ChangePassword(ctx, session.Token, testPassword, changedPassword)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrSession) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("multiple password changes committed", successes.Load())
	}
}

func TestPasswordChangeCancelledAndConcurrentLogin(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	session, err := s.Initialize(ctx, "tester", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = s.ChangePassword(cancelled, session.Token, testPassword, changedPassword); err == nil {
		t.Fatal("cancelled change committed")
	}
	if _, err = s.Authenticate(ctx, session.Token); err != nil {
		t.Fatal("cancelled change invalidated session", err)
	}
	var login Session
	var loginErr, changeErr error
	var wg sync.WaitGroup
	wg.Go(func() { login, loginErr = s.Login(ctx, "tester", testPassword) })
	wg.Go(func() { changeErr = s.ChangePassword(ctx, session.Token, testPassword, changedPassword) })
	wg.Wait()
	if changeErr != nil {
		t.Fatal(changeErr)
	}
	if loginErr != nil && !errors.Is(loginErr, ErrCredentials) {
		t.Fatal(loginErr)
	}
	for _, token := range []string{session.Token, login.Token} {
		if _, err = s.Authenticate(ctx, token); !errors.Is(err, ErrSession) {
			t.Fatal("concurrent old login survived", err)
		}
	}
}
