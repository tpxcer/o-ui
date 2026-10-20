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
	if len(token) != 43 {
		return ErrSession
	}
	if err := ValidatePassword(next); err != nil {
		return err
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
	if len(current) < 12 || len(current) > 72 || bcrypt.CompareHashAndPassword(hash, []byte(current)) != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE auth_throttle SET failures=?,reset_at=? WHERE id=1", failures+1, reset); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrCurrentPassword
	}
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
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET expires=0"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE auth_throttle SET failures=0,reset_at=0 WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}
