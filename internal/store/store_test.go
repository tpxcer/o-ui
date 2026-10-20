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

const testPassword = "test-only-passphrase-924"

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAuthLifecycleAndPersistence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if initialized, err := s.Initialized(ctx); err != nil || initialized {
		t.Fatal(initialized, err)
	}
	session, err := s.Initialize(ctx, "tester", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Token) != 43 {
		t.Fatal("unexpected token length")
	}
	if _, err = s.Initialize(ctx, "other", testPassword); !errors.Is(err, ErrInitialized) {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, "bogus"); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, strings.Repeat("a", 43)); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	if got, err := s.Authenticate(ctx, session.Token); err != nil || got.Username != "tester" {
		t.Fatal(got, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(testPassword)) || bytes.Contains(content, []byte(session.Token)) {
		t.Fatal("plaintext credentials persisted")
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, session.Token); err != nil {
		t.Fatal("session lost after restart", err)
	}
	if _, err = s.Login(ctx, "wrong", testPassword); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
	if _, err = s.Login(ctx, "tester", testPassword+strings.Repeat("x", 80)); !errors.Is(err, ErrCredentials) {
		t.Fatal("overlong login accepted", err)
	}
	login, err := s.Login(ctx, "tester", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if login.Token == session.Token {
		t.Fatal("session not rotated")
	}
	if err = s.Logout(ctx, login.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, login.Token); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	s.now = func() time.Time { return session.ExpiresAt }
	if _, err = s.Authenticate(ctx, session.Token); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(root, "panel.sqlite")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if path == root {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatal(path, info.Mode())
		}
	}
}

func TestThrottleSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.Initialize(ctx, "tester", testPassword); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err = s.Login(ctx, "tester", "wrong"); !errors.Is(err, ErrCredentials) {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Login(ctx, "tester", testPassword); !errors.Is(err, ErrLimited) {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	if _, err = s.Login(ctx, "tester", testPassword); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentInitialize(t *testing.T) {
	s := openTest(t)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			_, err := s.Initialize(context.Background(), "tester", testPassword)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrInitialized) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
}

func TestAdministratorValidation(t *testing.T) {
	for _, value := range [][2]string{{"ab", testPassword}, {"a b", testPassword}, {"tester", "short"}, {"tester", strings.Repeat("a", 73)}, {"tester", strings.Repeat("密", 25)}} {
		if ValidateAdministrator(value[0], value[1]) == nil {
			t.Fatal("accepted invalid credentials")
		}
	}
}

func TestDocumentsAtomicCAS(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	doc, err := s.ReadDocument(ctx, "xray-settings")
	if err != nil || doc.Revision != 0 || string(doc.Data) != "null" {
		t.Fatal(doc, err)
	}
	doc, err = s.SaveDocument(ctx, "xray-settings", 0, json.RawMessage(`{"a":1}`))
	if err != nil || doc.Revision != 1 {
		t.Fatal(doc, err)
	}
	if _, err = s.SaveDocument(ctx, "xray-settings", 0, json.RawMessage(`{"a":2}`)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.SaveDocument(ctx, "xray-settings", 1, json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Go(func() {
			_, err := s.SaveDocument(ctx, "xray-settings", 1, json.RawMessage(`{"a":3}`))
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
	doc, err = s.ReadDocument(ctx, "xray-settings")
	if err != nil || doc.Revision != 2 || string(doc.Data) != `{"a":3}` {
		t.Fatal(doc, err)
	}
	if _, err = s.SaveDocument(ctx, "xray-settings", 999, json.RawMessage(`{}`)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestDocumentSurvivesReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveDocument(ctx, "workspace", 0, json.RawMessage(`{"inbounds":[],"clients":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	doc, err := s.ReadDocument(ctx, "workspace")
	if err != nil || doc.Revision != 1 || string(doc.Data) != `{"inbounds":[],"clients":[]}` {
		t.Fatal(doc, err)
	}
}

func TestRejectSymlinksAndFutureSchema(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(link); err == nil {
		_ = s.Close()
		t.Fatal("accepted linked root")
	}
	file := filepath.Join(target, "panel.sqlite")
	if err := os.Symlink(filepath.Join(root, "other"), file); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(target); err == nil {
		_ = s.Close()
		t.Fatal("accepted linked database")
	}
	s := openTest(t)
	if _, err := s.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err == nil {
		t.Fatal("accepted future schema")
	}
}
