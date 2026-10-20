package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"independent-panel/internal/i18n"
)

// 两步验证与会话时长存在独立文档中，不改表结构，也不进业务备份。
const (
	TwoFactorDocument     = "auth-totp"
	SessionDocument       = "panel-session"
	MinSessionMinutes     = 5
	MaxSessionMinutes     = 43200
	DefaultSessionMinutes = 720
)

var (
	ErrTwoFactorCredentials = i18n.New("用户名、密码或验证码错误", "Wrong username, password or verification code")
	ErrTwoFactorCode        = i18n.New("验证码错误", "Wrong verification code")
	ErrTwoFactorState       = i18n.New("两步验证状态已变化，请刷新后重试", "Two-factor authentication changed; refresh and try again")
)

type twoFactor struct {
	Secret   string `json:"secret,omitempty"`
	Pending  string `json:"pending,omitempty"`
	LastStep int64  `json:"lastStep,omitempty"`
}

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// totpCode按RFC 6238（HMAC-SHA1、30秒、6位）计算某一时间步的验证码。
func totpCode(secret []byte, step int64) string {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}

// verifyTOTP允许前后各一个时间步的时钟偏差；同一时间步及更早的验证码不能重复使用。
func verifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	key, err := totpEncoding.DecodeString(secret)
	code = strings.TrimSpace(code)
	if err != nil || len(key) == 0 || len(code) != 6 {
		return 0, false
	}
	current := now.Unix() / 30
	for _, step := range []int64{current - 1, current, current + 1} {
		if step > lastStep && subtle.ConstantTimeCompare([]byte(totpCode(key, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

func readTwoFactor(ctx context.Context, tx *sql.Tx) (twoFactor, int64, error) {
	var state twoFactor
	var revision int64
	var data []byte
	err := tx.QueryRowContext(ctx, "SELECT revision,data FROM documents WHERE name=?", TwoFactorDocument).Scan(&revision, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return state, 0, nil
	}
	if err != nil {
		return state, 0, err
	}
	if json.Unmarshal(data, &state) != nil {
		return state, 0, i18n.New("两步验证设置损坏", "Two-factor settings are corrupted")
	}
	return state, revision, nil
}

func writeTwoFactor(ctx context.Context, tx *sql.Tx, state twoFactor, revision int64) error {
	data, _ := json.Marshal(state)
	var result sql.Result
	var err error
	if revision == 0 {
		result, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO documents(name,revision,data) VALUES(?,1,?)", TwoFactorDocument, data)
	} else {
		result, err = tx.ExecContext(ctx, "UPDATE documents SET revision=revision+1,data=? WHERE name=? AND revision=?", data, TwoFactorDocument, revision)
	}
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrTwoFactorState
	}
	return nil
}

// sessionLifetime读取面板设置的会话时长；缺失或超出范围时用默认12小时。
func sessionLifetime(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) time.Duration {
	var data []byte
	var value struct {
		Minutes int `json:"minutes"`
	}
	if query.QueryRowContext(ctx, "SELECT data FROM documents WHERE name=?", SessionDocument).Scan(&data) != nil || json.Unmarshal(data, &value) != nil || value.Minutes < MinSessionMinutes || value.Minutes > MaxSessionMinutes {
		return DefaultSessionMinutes * time.Minute
	}
	return time.Duration(value.Minutes) * time.Minute
}

func throttle(ctx context.Context, tx *sql.Tx, now time.Time) (int, int64, error) {
	var failures int
	var reset int64
	if err := tx.QueryRowContext(ctx, "SELECT failures,reset_at FROM auth_throttle WHERE id=1").Scan(&failures, &reset); err != nil {
		return 0, 0, err
	}
	if now.Unix() >= reset {
		failures, reset = 0, now.Add(15*time.Minute).Unix()
	}
	if failures >= 5 {
		return failures, reset, ErrLimited
	}
	return failures, reset, nil
}

// failAttempt记一次失败并提交事务，再返回给定错误。
func failAttempt(ctx context.Context, tx *sql.Tx, failures int, reset int64, cause error) error {
	if _, err := tx.ExecContext(ctx, "UPDATE auth_throttle SET failures=?,reset_at=? WHERE id=1", failures+1, reset); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return cause
}

func sha256Sum(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

func (s *Store) sessionTx(ctx context.Context, token string) (*sql.Tx, string, error) {
	if len(token) != 43 {
		return nil, "", ErrSession
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	digest := sha256Sum(token)
	var username string
	err = tx.QueryRowContext(ctx, "SELECT a.username FROM sessions s JOIN administrator a ON a.id=s.admin_id WHERE s.digest=? AND s.expires>?", digest, s.now().Unix()).Scan(&username)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrSession
		}
		return nil, "", err
	}
	return tx, username, nil
}

// TwoFactorEnabledTx在已有事务中读取是否启用两步验证。
func (s *Store) TwoFactorEnabledTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	state, _, err := readTwoFactor(ctx, tx)
	return state.Secret != "", err
}

// TwoFactorEnabled供登录页判断是否显示验证码输入（只透露是否启用）。
func (s *Store) TwoFactorEnabled(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	state, _, err := readTwoFactor(ctx, tx)
	return state.Secret != "", err
}

// BeginTwoFactor为已登录管理员生成待确认的密钥；确认验证码之前不影响登录。
func (s *Store) BeginTwoFactor(ctx context.Context, token string) (secret, username string, err error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	tx, username, err := s.sessionTx(ctx, token)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	state, revision, err := readTwoFactor(ctx, tx)
	if err != nil {
		return "", "", err
	}
	if state.Secret != "" {
		return "", "", ErrTwoFactorState
	}
	key := make([]byte, 20)
	if _, err = rand.Read(key); err != nil {
		return "", "", err
	}
	state.Pending = totpEncoding.EncodeToString(key)
	if err = writeTwoFactor(ctx, tx, state, revision); err != nil {
		return "", "", err
	}
	return state.Pending, username, tx.Commit()
}

// EnableTwoFactor用待确认密钥校验一次验证码后启用；错误计入登录限速。
func (s *Store) EnableTwoFactor(ctx context.Context, token, code string) error {
	return s.changeTwoFactor(ctx, token, code, true)
}

// DisableTwoFactor用当前密钥校验验证码后停用。
func (s *Store) DisableTwoFactor(ctx context.Context, token, code string) error {
	return s.changeTwoFactor(ctx, token, code, false)
}

func (s *Store) changeTwoFactor(ctx context.Context, token, code string, enable bool) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	tx, _, err := s.sessionTx(ctx, token)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now()
	failures, reset, err := throttle(ctx, tx, now)
	if err != nil {
		return err
	}
	state, revision, err := readTwoFactor(ctx, tx)
	if err != nil {
		return err
	}
	secret := state.Secret
	if enable {
		secret = state.Pending
	}
	if (enable && (state.Secret != "" || state.Pending == "")) || (!enable && state.Secret == "") {
		return ErrTwoFactorState
	}
	step, ok := verifyTOTP(secret, code, now, state.LastStep)
	if !ok {
		return failAttempt(ctx, tx, failures, reset, ErrTwoFactorCode)
	}
	if enable {
		state = twoFactor{Secret: secret, LastStep: step}
	} else {
		state = twoFactor{}
	}
	if err = writeTwoFactor(ctx, tx, state, revision); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE auth_throttle SET failures=0,reset_at=0 WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}

// checkTwoFactorTx在已启用两步验证时校验验证码并记录时间步，防止同一验证码重复使用。
func checkTwoFactorTx(ctx context.Context, tx *sql.Tx, code string, now time.Time) (enabled, ok bool, err error) {
	state, revision, err := readTwoFactor(ctx, tx)
	if err != nil || state.Secret == "" {
		return false, err == nil, err
	}
	step, valid := verifyTOTP(state.Secret, code, now, state.LastStep)
	if !valid {
		return true, false, nil
	}
	state.LastStep = step
	return true, true, writeTwoFactor(ctx, tx, state, revision)
}
