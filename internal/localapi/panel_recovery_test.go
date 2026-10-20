package localapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"independent-panel/internal/store"
)

func TestPanelAccessRecoveryPreservesState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	session, err := s.Initialize(ctx, "recoveryqa", "isolated-recovery-test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"panel-access", "panel-tls", "workspace", "xray-settings", "proxy-identities", "traffic", subscriptionsDocument, appliedDocument} {
		if _, err := s.SaveDocument(ctx, name, 0, json.RawMessage(`{"marker":"isolated-`+name+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{"workspace", "xray-settings", "proxy-identities", "traffic", subscriptionsDocument, appliedDocument}
	before, err := s.ReadDocuments(ctx, names)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.ReadDocuments(ctx, []string{"panel-access", "panel-tls"})
	if err != nil {
		t.Fatal(err)
	}
	backup, err := RecoverPanelAccess(ctx, root, "127.0.0.1:5199", "恢复面板本机访问")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(backup)) != root {
		t.Fatal("backup outside explicit state directory")
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(backup), 0700}, {backup, 0600}} {
		info, err := os.Lstat(item.path)
		if err != nil || info.Mode().Perm() != item.mode {
			t.Fatal("recovery backup permissions invalid")
		}
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Format    string
		Version   int
		Documents map[string]store.Document
	}
	if json.Unmarshal(data, &snapshot) != nil || snapshot.Format != "independent-panel-access-recovery" || snapshot.Version != 1 || !reflect.DeepEqual(snapshot.Documents, old) {
		t.Fatal("backup did not preserve exact previous documents")
	}
	if bytes.Contains(data, []byte(session.Token)) || bytes.Contains(data, []byte("isolated-recovery-test-passphrase")) || bytes.Contains(data, []byte("recoveryqa")) {
		t.Fatal("backup included account material")
	}
	access, err := LoadPanelAccess(ctx, s, "127.0.0.1:5180", nil)
	if err != nil || access.Data.Public || access.Data.Listen != "127.0.0.1:5199" || access.Revision != 2 {
		t.Fatal("loopback access not recovered", err)
	}
	certificate, err := LoadPanelTLS(ctx, s)
	if err != nil || certificate != nil {
		t.Fatal("explicit recovery did not disable panel TLS", err)
	}
	after, err := s.ReadDocuments(ctx, names)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("recovery changed business or runtime documents", err)
	}
	if _, err := s.Authenticate(ctx, session.Token); err != nil {
		t.Fatal("recovery invalidated existing administrator/session", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Login(ctx, "recoveryqa", "isolated-recovery-test-passphrase"); err != nil {
		t.Fatal("recovery changed password", err)
	}
	second, err := RecoverPanelAccess(ctx, root, "127.0.0.1:5199", "恢复面板本机访问")
	if err != nil || second == backup {
		t.Fatal("repeat recovery overwrote prior backup", err)
	}
}

func TestPanelAccessRecoveryRejectsUnsafeRequests(t *testing.T) {
	for _, tc := range []struct{ name, listen, confirmation, kind string }{
		{"no-confirm", "127.0.0.1:5180", "", ""},
		{"wrong-confirm", "127.0.0.1:5180", "yes", ""},
		{"public-listen", "0.0.0.0:5180", "恢复面板本机访问", ""},
		{"low-port", "127.0.0.1:443", "恢复面板本机访问", ""},
		{"relative-state", "127.0.0.1:5180", "恢复面板本机访问", "relative"},
		{"absent-database", "127.0.0.1:5180", "恢复面板本机访问", "missing"},
		{"state-link", "127.0.0.1:5180", "恢复面板本机访问", "state-link"},
		{"database-link", "127.0.0.1:5180", "恢复面板本机访问", "database-link"},
		{"runtime-link", "127.0.0.1:5180", "恢复面板本机访问", "runtime-link"},
		{"lock-link", "127.0.0.1:5180", "恢复面板本机访问", "lock-link"},
		{"lock-directory", "127.0.0.1:5180", "恢复面板本机访问", "lock-directory"},
		{"live-owner", "127.0.0.1:5180", "恢复面板本机访问", "locked"},
		{"uninitialized", "127.0.0.1:5180", "恢复面板本机访问", "uninitialized"},
		{"cancelled", "127.0.0.1:5180", "恢复面板本机访问", "cancelled"},
		{"oversized-document", "127.0.0.1:5180", "恢复面板本机访问", "oversized"},
		{"future-schema", "127.0.0.1:5180", "恢复面板本机访问", "future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s, err := store.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if tc.kind != "uninitialized" {
				if _, err := s.Initialize(context.Background(), "recoveryqa", "isolated-recovery-test-passphrase"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.kind == "oversized" {
				data, _ := json.Marshal(string(bytes.Repeat([]byte("x"), 1<<20)))
				if _, err := s.SaveDocument(context.Background(), "panel-access", 0, data); err != nil {
					t.Fatal(err)
				}
			}
			if tc.kind == "future" {
				db, err := sql.Open("sqlite", filepath.Join(root, "panel.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec("PRAGMA user_version=2"); err != nil {
					t.Fatal(err)
				}
			}
			old, err := s.ReadDocuments(context.Background(), []string{"panel-access", "panel-tls"})
			if err != nil {
				t.Fatal(err)
			}
			selected := root
			switch tc.kind {
			case "relative":
				selected = "relative-state-do-not-create"
			case "missing":
				selected = t.TempDir()
			case "state-link":
				selected = filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(root, selected); err != nil {
					t.Fatal(err)
				}
			case "database-link":
				selected = t.TempDir()
				if err := os.Symlink(filepath.Join(root, "panel.sqlite"), filepath.Join(selected, "panel.sqlite")); err != nil {
					t.Fatal(err)
				}
			case "runtime-link":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "runtime")); err != nil {
					t.Fatal(err)
				}
			case "lock-link", "lock-directory":
				if err := os.Mkdir(filepath.Join(root, "runtime"), 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "runtime", ".runtime.lock")
				if tc.kind == "lock-link" {
					err = os.Symlink(filepath.Join(root, "panel.sqlite"), path)
				} else {
					err = os.Mkdir(path, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "locked":
				lock, err := lockRuntimeDirectory(filepath.Join(root, "runtime"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.kind == "cancelled" {
				cancel()
			}
			if _, err := RecoverPanelAccess(ctx, selected, tc.listen, tc.confirmation); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			after, err := s.ReadDocuments(context.Background(), []string{"panel-access", "panel-tls"})
			if err != nil || !reflect.DeepEqual(old, after) {
				t.Fatal("rejected recovery changed documents", err)
			}
			if tc.kind == "missing" {
				if _, err := os.Lstat(filepath.Join(selected, "panel.sqlite")); !os.IsNotExist(err) {
					t.Fatal("recovery created empty database")
				}
			}
		})
	}
}

func TestPanelAccessRecoveryFailureIsAtomic(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Initialize(ctx, "recoveryqa", "isolated-recovery-test-passphrase"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"panel-access", "panel-tls"} {
		if _, err := s.SaveDocument(ctx, name, 0, json.RawMessage(`{"marker":"old"}`)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.ReadDocuments(ctx, []string{"panel-access", "panel-tls"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE recovery_writes (n INTEGER NOT NULL);
INSERT INTO recovery_writes VALUES(0);
CREATE TRIGGER reject_access_recovery BEFORE UPDATE ON documents WHEN NEW.name IN ('panel-access','panel-tls') BEGIN
UPDATE recovery_writes SET n=n+1;
SELECT CASE WHEN (SELECT n FROM recovery_writes)=2 THEN RAISE(ABORT,'isolated second write fault') END;
END`); err != nil {
		t.Fatal(err)
	}
	backup, err := RecoverPanelAccess(ctx, root, "127.0.0.1:5199", "恢复面板本机访问")
	if err == nil || backup == "" {
		t.Fatal("fault did not report failure and preserve backup")
	}
	after, err := s.ReadDocuments(ctx, []string{"panel-access", "panel-tls"})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed transaction partially changed transport settings", err)
	}
	var writes int
	if err := db.QueryRow("SELECT n FROM recovery_writes").Scan(&writes); err != nil || writes != 0 {
		t.Fatal("second-write failure did not roll back first write", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal("failed transaction lost old settings backup")
	}
	if _, err := db.Exec("DROP TRIGGER reject_access_recovery"); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverPanelAccess(ctx, root, "127.0.0.1:5199", "恢复面板本机访问"); err != nil {
		t.Fatal("failure did not release lock for retry", err)
	}
}
