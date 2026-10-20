// Package store 保存独立面板数据，不读取或迁移旧面板数据库。
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var (
	ErrInitialized     = i18n.New("管理员已经设置，请登录", "The administrator is already set up; please sign in")
	ErrCredentials     = i18n.New("用户名或密码错误", "Wrong username or password")
	ErrSession         = i18n.New("请先登录，或登录已过期", "Please sign in, or your session has expired")
	ErrLimited         = i18n.New("尝试次数过多，请15分钟后重试", "Too many attempts; try again in 15 minutes")
	ErrConflict        = i18n.New("数据已被其他页面修改，请重新加载后重试", "Data was changed by another page; reload and try again")
	ErrCurrentPassword = i18n.New("当前密码错误", "Current password is incorrect")
	ErrSamePassword    = i18n.New("新密码不能与当前密码相同", "The new password must differ from the current one")
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,64}$`)

type Store struct {
	db     *sql.DB
	authMu sync.Mutex
	now    func() time.Time
}

type Session struct {
	Token     string    `json:"-"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Document struct {
	Revision int64           `json:"revision"`
	Data     json.RawMessage `json:"data"`
}

func Open(root string) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, i18n.New("数据目录必须是真实目录", "Data directory must be a real directory")
	}
	if err = os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "panel.sqlite")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = file.Close()
	} else if errors.Is(err, os.ErrExist) {
		info, err = os.Lstat(path)
		if err == nil && !info.Mode().IsRegular() {
			return nil, i18n.New("数据库必须是普通文件，不能是链接", "Database must be a regular file, not a link")
		}
	}
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err = s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return i18n.New("数据库版本高于当前程序，拒绝降级写入", "Database version is newer than this program; refusing downgrade writes")
	}
	if version == 0 {
		_, err = tx.Exec(`
CREATE TABLE administrator (id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL, password_hash BLOB NOT NULL);
CREATE TABLE sessions (digest BLOB PRIMARY KEY, expires INTEGER NOT NULL, admin_id INTEGER NOT NULL DEFAULT 1 REFERENCES administrator(id));
CREATE TABLE auth_throttle (id INTEGER PRIMARY KEY CHECK(id=1), failures INTEGER NOT NULL, reset_at INTEGER NOT NULL);
INSERT INTO auth_throttle VALUES(1,0,0);
CREATE TABLE documents (name TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision>0), data BLOB NOT NULL CHECK(json_valid(data)));
PRAGMA user_version=1;`)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Initialized(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&count)
	return count == 1, err
}

func ValidateAdministrator(username, password string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	return ValidatePassword(password)
}

func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return i18n.New("用户名需为3至64位字母、数字、点、下划线或短横线", "Username must be 3–64 letters, digits, dots, underscores or hyphens")
	}
	return nil
}

func ValidatePassword(password string) error {
	if len(password) < 6 || len(password) > 72 {
		return i18n.New("密码需为6至72字节", "Password must be 6–72 bytes")
	}
	return nil
}

func newSession(username string, now time.Time, lifetime time.Duration) (Session, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	return Session{token, username, now.Add(lifetime)}, digest[:], nil
}

func insertSession(ctx context.Context, tx *sql.Tx, session Session, digest []byte, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires<=?", now.Unix()); err != nil {
		return err
	}
	// 单管理员最多保留十个会话，避免无限积累。
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE digest IN (SELECT digest FROM sessions ORDER BY expires DESC LIMIT -1 OFFSET 9)"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO sessions(digest,expires) VALUES(?,?)", digest, session.ExpiresAt.Unix())
	return err
}

func (s *Store) Initialize(ctx context.Context, username, password string) (Session, error) {
	if err := ValidateAdministrator(username, password); err != nil {
		return Session{}, err
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	initialized, err := s.Initialized(ctx)
	if err != nil {
		return Session{}, err
	}
	if initialized {
		return Session{}, ErrInitialized
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	session, digest, err := newSession(username, now, sessionLifetime(ctx, s.db))
	if err != nil {
		return Session{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO administrator(id,username,password_hash) VALUES(1,?,?)", username, hash)
	if err != nil {
		return Session{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Session{}, ErrInitialized
	}
	if err = insertSession(ctx, tx, session, digest, now); err != nil {
		return Session{}, err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Store) Login(ctx context.Context, username, password string) (Session, error) {
	return s.LoginWithCode(ctx, username, password, "")
}

// LoginWithCode校验用户名密码；已启用两步验证时还须当前验证码，任何一项错误都返回同一错误并计入限速。
func (s *Store) LoginWithCode(ctx context.Context, username, password, code string) (Session, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var failures int
	var reset int64
	if err = tx.QueryRowContext(ctx, "SELECT failures,reset_at FROM auth_throttle WHERE id=1").Scan(&failures, &reset); err != nil {
		return Session{}, err
	}
	now := s.now()
	if now.Unix() >= reset {
		failures, reset = 0, now.Add(15*time.Minute).Unix()
	}
	if failures >= 5 {
		return Session{}, ErrLimited
	}
	var actual string
	var hash []byte
	err = tx.QueryRowContext(ctx, "SELECT username,password_hash FROM administrator WHERE id=1").Scan(&actual, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	validPassword := false
	if len(password) >= 6 && len(password) <= 72 {
		validPassword = bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	}
	if !validPassword || subtle.ConstantTimeCompare([]byte(actual), []byte(username)) != 1 {
		cause := ErrCredentials
		if enabled, _ := s.TwoFactorEnabledTx(ctx, tx); enabled {
			cause = ErrTwoFactorCredentials
		}
		return Session{}, failAttempt(ctx, tx, failures, reset, cause)
	}
	if enabled, ok, err := checkTwoFactorTx(ctx, tx, code, now); err != nil {
		return Session{}, err
	} else if enabled && !ok {
		return Session{}, failAttempt(ctx, tx, failures, reset, ErrTwoFactorCredentials)
	}
	session, digest, err := newSession(actual, now, sessionLifetime(ctx, tx))
	if err != nil {
		return Session{}, err
	}
	if err = insertSession(ctx, tx, session, digest, now); err != nil {
		return Session{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE auth_throttle SET failures=0,reset_at=0 WHERE id=1"); err != nil {
		return Session{}, err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Store) Authenticate(ctx context.Context, token string) (Session, error) {
	if len(token) != 43 {
		return Session{}, ErrSession
	}
	digest := sha256.Sum256([]byte(token))
	var session Session
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT a.username,s.expires FROM sessions s JOIN administrator a ON a.id=s.admin_id WHERE s.digest=? AND s.expires>?", digest[:], s.now().Unix()).Scan(&session.Username, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSession
	}
	if err != nil {
		return Session{}, err
	}
	session.ExpiresAt = time.Unix(expires, 0).UTC()
	return session, nil
}

func (s *Store) Logout(ctx context.Context, token string) error {
	digest := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE digest=?", digest[:])
	return err
}

func (s *Store) ReadDocument(ctx context.Context, name string) (Document, error) {
	doc := Document{Data: json.RawMessage("null")}
	err := s.db.QueryRowContext(ctx, "SELECT revision,data FROM documents WHERE name=?", name).Scan(&doc.Revision, &doc.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return doc, nil
	}
	return doc, err
}

func (s *Store) SaveDocument(ctx context.Context, name string, expected int64, data json.RawMessage) (Document, error) {
	if expected < 0 || !json.Valid(data) {
		return Document{}, i18n.New("数据格式无效", "Invalid data format")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback()
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO documents(name,revision,data) VALUES(?,1,?)", name, []byte(data))
	} else {
		result, err = tx.ExecContext(ctx, "UPDATE documents SET revision=revision+1,data=? WHERE name=? AND revision=?", []byte(data), name, expected)
	}
	if err != nil {
		return Document{}, i18n.Wrap("保存失败: ", "Save failed: ", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Document{}, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return Document{}, err
	}
	return Document{expected + 1, append(json.RawMessage(nil), data...)}, nil
}
