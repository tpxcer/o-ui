package localapi

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

const setupPassword = "install-only-test-passphrase"

func sessionCount(t *testing.T, root string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "panel.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestOfflinePanelAdminInitialization(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "panel")
	for _, tc := range []struct{ name, root, username, password string }{
		{"relative", "relative-panel", "installqa", setupPassword},
		{"unclean", base + "/x/../panel", "installqa", setupPassword},
		{"missing-parent", filepath.Join(base, "missing", "panel"), "installqa", setupPassword},
		{"invalid-username", root, "x", setupPassword},
		{"short-password", root, "installqa", "short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := InitPanelAdmin(ctx, tc.root, tc.username, tc.password); err == nil {
				t.Fatal("unsafe offline initialization accepted")
			}
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatal("rejected initialization created state", err)
			}
		})
	}
	if err := InitPanelAdmin(ctx, root, "installqa", setupPassword); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("state directory not private", err)
	}
	if n := sessionCount(t, root); n != 0 {
		t.Fatal("offline initialization left a session", n)
	}
	if err := InitPanelAdmin(ctx, root, "otheradmin", "another-install-test-passphrase"); !errors.Is(err, store.ErrInitialized) {
		t.Fatal("second offline initialization not rejected", err)
	}
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "otheradmin", "another-install-test-passphrase"); !errors.Is(err, store.ErrCredentials) {
		t.Fatal("rejected initialization changed administrator", err)
	}
	if _, err := s.Login(ctx, "installqa", setupPassword); err != nil {
		t.Fatal("offline administrator cannot sign in", err)
	}
	s.Close()

	running := filepath.Join(base, "running")
	lock, err := lockRuntimeDirectory(filepath.Join(running, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := InitPanelAdmin(ctx, running, "installqa", setupPassword); err == nil {
		t.Fatal("initialization bypassed the running panel lock")
	}
	lock.Close()
	if _, err := os.Lstat(filepath.Join(running, "panel.sqlite")); !os.IsNotExist(err) {
		t.Fatal("locked initialization created a database", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := InitPanelAdmin(ctx, link, "installqa", setupPassword); err == nil {
		t.Fatal("linked state directory accepted")
	}
}

func TestOfflinePanelHTTPSAndFirstLogin(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "panel")
	cert, key := runtimeCertificate(t, t.TempDir(), "panel-install.test")
	listen, origin := "0.0.0.0:5190", "https://panel-install.test:5190"
	if err := EnablePanelHTTPS(ctx, root, cert, key, listen, origin, httpsConfirmation); err == nil {
		t.Fatal("HTTPS enabled without a database")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := EnablePanelHTTPS(ctx, root, cert, key, listen, origin, httpsConfirmation); err == nil {
		t.Fatal("HTTPS enabled before administrator initialization")
	}
	if err := InitPanelAdmin(ctx, root, "installqa", setupPassword); err != nil {
		t.Fatal(err)
	}
	wrongCert, wrongKey := runtimeCertificate(t, t.TempDir(), "other.test")
	for _, tc := range []struct{ name, cert, key, listen, origin, confirmation string }{
		{"no-confirmation", cert, key, listen, origin, ""},
		{"http-origin", cert, key, listen, "http://panel-install.test:5190", httpsConfirmation},
		{"wrong-name", wrongCert, wrongKey, listen, origin, httpsConfirmation},
		{"port-mismatch", cert, key, "0.0.0.0:5191", origin, httpsConfirmation},
		{"privileged-port", cert, key, "0.0.0.0:443", "https://panel-install.test", httpsConfirmation},
		{"hostname-listen", cert, key, "panel-install.test:5190", origin, httpsConfirmation},
		{"relative-cert", "cert.pem", key, listen, origin, httpsConfirmation},
		{"missing-key", cert, key + ".missing", listen, origin, httpsConfirmation},
		{"mismatched-pair", cert, wrongKey, listen, origin, httpsConfirmation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := EnablePanelHTTPS(ctx, root, tc.cert, tc.key, tc.listen, tc.origin, tc.confirmation); err == nil {
				t.Fatal("invalid HTTPS settings accepted")
			}
		})
	}
	lock, err := lockRuntimeDirectory(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := EnablePanelHTTPS(ctx, root, cert, key, listen, origin, httpsConfirmation); err == nil {
		t.Fatal("HTTPS settings written while the panel runs")
	}
	lock.Close()
	s, err = store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := s.ReadDocuments(ctx, []string{"panel-access", "panel-tls"})
	if err != nil || docs["panel-access"].Revision != 0 || docs["panel-tls"].Revision != 0 {
		t.Fatal("rejected HTTPS settings were partially saved", err)
	}
	s.Close()
	if err := EnablePanelHTTPS(ctx, root, cert, key, listen, origin, httpsConfirmation); err != nil {
		t.Fatal(err)
	}

	// 下次启动与面板一致地读取：公网HTTPS，管理员已存在。
	s, err = store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	panelTLS, err := LoadPanelTLS(ctx, s)
	if err != nil || panelTLS == nil {
		t.Fatal("saved panel certificate not loaded", err)
	}
	access, err := LoadPanelAccess(ctx, s, "127.0.0.1:5180", panelTLS)
	if err != nil || !access.Data.Public || access.Data.Listen != listen || access.Data.Origin != origin || access.Revision != 1 {
		t.Fatal("saved panel access mismatch", err, access)
	}
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	rt, err := NewRuntime(m, s, t.TempDir(), panelTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	handler := PanelHandler(m, s, t.TempDir(), access, rt)
	call := func(method, path string, value any, secure bool) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(value)
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		r.Host, r.RemoteAddr = "panel-install.test:5190", "198.51.100.7:4321"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		if secure {
			r.TLS = &tls.ConnectionState{}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	credentials := map[string]string{"username": "installqa", "password": setupPassword}
	if w := call("POST", "/api/auth/setup", map[string]string{"username": "attacker", "password": "attacker-chosen-passphrase"}, true); w.Code != 403 {
		t.Fatal("public web initialization reachable", w.Code)
	}
	for _, path := range []string{"/api/auth/status", "/api/auth/login"} {
		if w := call("POST", path, credentials, false); w.Code != 403 {
			t.Fatal("plain HTTP reached the public panel", path, w.Code)
		}
	}
	w := call("GET", "/api/auth/status", nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"initialized":true`) || !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatal("first HTTPS status mismatch", w.Code, w.Body.String())
	}
	w = call("POST", "/api/auth/login", credentials, true)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure || w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("first HTTPS login failed or insecure", w.Code)
	}
	if _, err := s.Login(ctx, "attacker", "attacker-chosen-passphrase"); !errors.Is(err, store.ErrCredentials) {
		t.Fatal("public setup changed administrator", err)
	}
	if w := call("GET", "/api/workspace", nil, true); w.Code != http.StatusUnauthorized {
		t.Fatal("anonymous HTTPS read allowed", w.Code)
	}
}

func TestInstallerDefaultLanguage(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html><html><head><title>o-ui</title></head></html>"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, tc := range []struct{ env, want string }{{"en", `<head><meta name="o-ui-default-lang" content="en"><base data-panel-base href="/"><title>`}, {"zh", `content="zh"`}, {"", `<head><base data-panel-base href="/"><title>`}, {"fr", `<head><base data-panel-base href="/"><title>`}, {`en"><script>`, `<head><base data-panel-base href="/"><title>`}} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("O_UI_DEFAULT_LANG", tc.env)
			handler := Handler(m, s, dist, []string{"127.0.0.1:5180"})
			r := httptest.NewRequest("GET", "/", nil)
			r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) || strings.Count(w.Body.String(), "o-ui-default-lang") > 1 {
				t.Fatal("default language page mismatch", w.Code, w.Body.String())
			}
		})
	}
}
