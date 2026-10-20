package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"math"
)

// ReadDocuments从一个事务读取相关业务文档，避免拼出不同时间的备份。
func (s *Store) ReadDocuments(ctx context.Context, names []string) (map[string]Document, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := map[string]Document{}
	for _, name := range names {
		doc := Document{Data: json.RawMessage("null")}
		err = tx.QueryRowContext(ctx, "SELECT revision,data FROM documents WHERE name=?", name).Scan(&doc.Revision, &doc.Data)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		result[name] = doc
	}
	return result, tx.Commit()
}

// ReplaceDocuments仅在所有修订匹配时整体替换；保留账号及会话，修订不回退。
func (s *Store) ReplaceDocuments(ctx context.Context, expected map[string]Document, data map[string]json.RawMessage) error {
	if len(expected) == 0 || len(expected) != len(data) {
		return i18n.New("恢复文档集合无效", "Invalid restore document set")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, old := range expected {
		next, exists := data[name]
		if !exists || !json.Valid(next) || old.Revision < 0 || old.Revision == math.MaxInt64 {
			return i18n.New("恢复文档无效", "Invalid restore document")
		}
		var result sql.Result
		if old.Revision == 0 {
			result, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO documents(name,revision,data) VALUES(?,1,?)", name, []byte(next))
		} else {
			result, err = tx.ExecContext(ctx, "UPDATE documents SET revision=revision+1,data=? WHERE name=? AND revision=?", []byte(next), name, old.Revision)
		}
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ErrConflict
		}
	}
	return tx.Commit()
}
