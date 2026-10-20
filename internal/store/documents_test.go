package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestDocumentSnapshotAndAtomicReplace(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	names := []string{"workspace", "xray-settings", "proxy-identities", "traffic"}
	if _, err = s.Initialize(ctx, "tester", "test-only-passphrase-924"); err != nil {
		t.Fatal(err)
	}
	docs, err := s.ReadDocuments(ctx, names)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]json.RawMessage{}
	for _, name := range names {
		data[name] = json.RawMessage(`{"value":1}`)
	}
	if err = s.ReplaceDocuments(ctx, docs, data); err != nil {
		t.Fatal(err)
	}
	next, _ := s.ReadDocuments(ctx, names)
	for _, name := range names {
		if next[name].Revision != 1 {
			t.Fatal("bad revision")
		}
	}
	if err = s.ReplaceDocuments(ctx, docs, data); !errors.Is(err, ErrConflict) {
		t.Fatal("stale accepted", err)
	}
	data["traffic"] = json.RawMessage(`invalid`)
	if err = s.ReplaceDocuments(ctx, next, data); err == nil {
		t.Fatal("invalid accepted")
	}
	after, _ := s.ReadDocuments(ctx, names)
	a, _ := json.Marshal(next)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("partial write")
	}
	if initialized, _ := s.Initialized(ctx); !initialized {
		t.Fatal("administrator lost")
	}
	// SQLite中途写失败也必须回滚之前已经执行的文档更新。
	if _, err = s.db.Exec(`CREATE TRIGGER reject_document_restore BEFORE UPDATE ON documents BEGIN SELECT RAISE(ABORT, 'test restore failure'); END`); err != nil {
		t.Fatal(err)
	}
	data["traffic"] = json.RawMessage(`{"value":2}`)
	if err = s.ReplaceDocuments(ctx, next, data); err == nil {
		t.Fatal("injected failure ignored")
	}
	after, _ = s.ReadDocuments(ctx, names)
	b, _ = json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("failed transaction changed state")
	}
}
