package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (s *Store) ChangePassword(ctx context.Context, token, current, next string) error {
	return s.ChangeAccount(ctx, token, current, "", next)
}

func (s *Store) ChangeAccount(ctx context.Context, token, current, username, next string) error {
	return s.ChangeAccountWithCode(ctx, token, current, username, next, "")
}

// ChangeAccountWithCode校验当前密码（已启用两步验证时还须验证码）后修改用户名和/或密码（空值表示不改），成功后全部会话失效。
func (s *Store) ChangeAccountWithCode(ctx context.Context, token, current, username, next, code string) error {
	if len(token) != 43 {
		return ErrSession
	}
	if username != "" {
		if err := ValidateUsername(username); err != nil {
			return err
		}
	}
	if next != "" || username == "" {
		if err := ValidatePassword(next); err != nil {
			return err
		}
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now()
	digest := sha256.Sum256([]byte(token))
	var hash []byte
	err = tx.QueryRowContext(ctx, "SELECT a.password_hash FROM sessions s JOIN administrator a ON a.id=s.admin_id WHERE s.digest=? AND s.expires>?", digest[:], now.Unix()).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSession
	}
	if err != nil {
		return err
	}
	var failures int
	var reset int64
	if err = tx.QueryRowContext(ctx, "SELECT failures,reset_at FROM auth_throttle WHERE id=1").Scan(&failures, &reset); err != nil {
		return err
	}
	if now.Unix() >= reset {
		failures, reset = 0, now.Add(15*time.Minute).Unix()
	}
	if failures >= 5 {
		return ErrLimited
	}
	if len(current) < 6 || len(current) > 72 || bcrypt.CompareHashAndPassword(hash, []byte(current)) != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE auth_throttle SET failures=?,reset_at=? WHERE id=1", failures+1, reset); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrCurrentPassword
	}
	if enabled, ok, err := checkTwoFactorTx(ctx, tx, code, now); err != nil {
		return err
	} else if enabled && !ok {
		return failAttempt(ctx, tx, failures, reset, ErrTwoFactorCode)
	}
	if next != "" {
		if current == next {
			return ErrSamePassword
		}
		hash, err = bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE administrator SET password_hash=? WHERE id=1", hash); err != nil {
			return err
		}
	}
	if username != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE administrator SET username=? WHERE id=1", username); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET expires=0"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE auth_throttle SET failures=0,reset_at=0 WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetAdministrator供服务器上的离线管理命令重置用户名和密码（不需要旧密码），并使全部会话失效、清除限速与两步验证。
func (s *Store) ResetAdministrator(ctx context.Context, username, password string) error {
	if err := ValidateAdministrator(username, password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE administrator SET username=?,password_hash=? WHERE id=1", username, hash)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrSession
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET expires=0"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE auth_throttle SET failures=0,reset_at=0 WHERE id=1"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM documents WHERE name=?", TwoFactorDocument); err != nil {
		return err
	}
	return tx.Commit()
}

// AdministratorName返回管理员用户名；未初始化时为空。
func (s *Store) AdministratorName(ctx context.Context) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT username FROM administrator WHERE id=1").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}
