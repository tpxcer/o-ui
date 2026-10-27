package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"independent-panel/internal/store"
)

func TestPanelServedOnlyUnderAccessPath(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dist := t.TempDir()
	if err = os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html><head></head>panel</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	h := PanelHandler(nil, s, dist, &PanelAccess{Data: PanelAccessDraft{Listen: "127.0.0.1:5180", BasePath: "/secret-9/"}}, nil)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:1234", "127.0.0.1:5180"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get("/secret-9/"); w.Code != 200 || !strings.Contains(w.Body.String(), `<base data-panel-base href="/secret-9/">`) {
		t.Fatal("panel page not served under the path", w.Code, w.Body.String())
	}
	// 页面、接口与订阅都带CSP：脚本只许同源，禁止被嵌入、插件和外部表单提交。
	for _, path := range []string{"/secret-9/", "/secret-9/api/auth/status", "/sub/missing"} {
		csp := get(path).Header().Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'self'", "script-src 'self';", "object-src 'none'", "base-uri 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, directive) {
				t.Fatal("missing CSP directive", path, directive, csp)
			}
		}
		if strings.Contains(csp, "unsafe-eval") || strings.Contains(strings.SplitN(strings.SplitN(csp, "script-src", 2)[1], ";", 2)[0], "unsafe-inline") {
			t.Fatal("scripts must not allow eval or inline code", csp)
		}
	}
	if w := get("/secret-9/api/auth/status"); w.Code != 200 {
		t.Fatal("API not served under the path", w.Code)
	}
	if w := get("/secret-9"); w.Code != http.StatusFound || w.Header().Get("Location") != "/secret-9/" {
		t.Fatal("missing trailing slash must redirect", w.Code, w.Header())
	}
	for _, path := range []string{"/", "/api/auth/status", "/index.html", "/other/"} {
		if w := get(path); w.Code != http.StatusNotFound {
			t.Fatal("served outside the access path", path, w.Code)
		}
	}
	if w := get("/sub/missing"); w.Code == http.StatusNotFound && w.Body.String() == "404 page not found\n" {
		t.Fatal("subscriptions must stay at the root path")
	}
}

func TestPanelPagePathsAndBoundaries(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dist := t.TempDir()
	if err = os.Mkdir(filepath.Join(dist, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"index.html": `<html><head></head><script src="./assets/app.js"></script></html>`, "assets/app.js": "asset"} {
		if err = os.WriteFile(filepath.Join(dist, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("O_UI_DEFAULT_LANG", "en")
	for _, base := range []string{"/", "/secret-9/", "/panel/", "/clients/"} {
		t.Run(base, func(t *testing.T) {
			h := PanelHandler(nil, s, dist, &PanelAccess{Data: PanelAccessDraft{Listen: "127.0.0.1:5180", BasePath: base}}, nil)
			get := func(path, method string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, nil)
				r.RemoteAddr, r.Host = "127.0.0.1:1234", "127.0.0.1:5180"
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			for _, page := range []string{"panel", "inbounds", "clients", "settings", "xray"} {
				path := base + page
				w := get(path+"/", "GET")
				if w.Code != 200 || !strings.Contains(w.Body.String(), `<base data-panel-base href="`+base+`">`) || !strings.Contains(w.Body.String(), `content="en"`) || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(path, w.Code, w.Body.String())
				}
				if w = get(path+"?test=1", "GET"); w.Code != 302 || w.Header().Get("Location") != path+"/?test=1" {
					t.Fatal("canonical page redirect", w.Code, w.Header())
				}
				if w = get(path+"/", "HEAD"); w.Code != 200 || w.Body.Len() != 0 {
					t.Fatal("HEAD page response", w.Code)
				}
			}
			if w := get(base+"assets/app.js", "GET"); w.Code != 200 || w.Body.String() != "asset" {
				t.Fatal("assets", w.Code, w.Body.String())
			}
			if w := get(base+"api/auth/status", "GET"); w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
				t.Fatal("auth status", w.Code)
			}
			if w := get(base+"api/workspace", "GET"); w.Code != 401 {
				t.Fatal("anonymous API read", w.Code)
			}
			for _, path := range []string{base + "unknown/", base + "panel/nested/", base + "assets/missing.js", base + "panel/api/auth/status"} {
				if w := get(path, "GET"); w.Code != 404 {
					t.Fatal("unknown route must not return the entry page", path, w.Code)
				}
			}
			if base != "/" {
				if w := get("/", "GET"); w.Code != 404 {
					t.Fatal("outside access path", w.Code)
				}
			}
			if w := get("/sub/missing", "GET"); strings.Contains(w.Header().Get("Content-Type"), "text/html") {
				t.Fatal("subscription fell through to HTML")
			}
		})
	}
}

func TestPanelAccessPathValidation(t *testing.T) {
	for path, valid := range map[string]bool{"": true, "/": true, "/a/": true, "/Ab_9-x/": true, "/a": false, "a/": false, "/a/b/": false, "/api/": false, "/SUB/": false, "/assets/": false, "/a b/": false} {
		if validPanelBasePath(path) != valid {
			t.Fatal(path)
		}
	}
}

func TestOfflinePanelPathAndInfo(t *testing.T) {
	ctx, root := context.Background(), filepath.Join(t.TempDir(), "panel")
	if err := InitPanelAdmin(ctx, root, "admin", "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := SetPanelPath(ctx, root, "/abc/"); err == nil {
		t.Fatal("path set before access settings exist")
	}
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(PanelAccessDraft{Listen: "127.0.0.1:23456"})
	if _, err = s.SaveDocument(ctx, "panel-access", 0, data); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err = SetPanelPath(ctx, root, "/api/"); err == nil {
		t.Fatal("reserved path accepted")
	}
	if err = SetPanelPath(ctx, root, "/abc/"); err != nil {
		t.Fatal(err)
	}
	info, err := ReadPanelInfo(ctx, root)
	if err != nil || info.BasePath != "/abc/" || info.URL != "http://127.0.0.1:23456/abc/" || info.Username != "admin" || info.Public {
		t.Fatal(info, err)
	}
	if err = SetPanelPath(ctx, root, "/"); err != nil {
		t.Fatal(err)
	}
	if info, _ = ReadPanelInfo(ctx, root); info.BasePath != "/" {
		t.Fatal("root path not restored", info.BasePath)
	}
}

func TestOfflineResetPanelAdmin(t *testing.T) {
	ctx, root := context.Background(), filepath.Join(t.TempDir(), "panel")
	if err := InitPanelAdmin(ctx, root, "admin", "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := ResetPanelAdmin(ctx, root, "admin2", "12345"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := ResetPanelAdmin(ctx, root, "admin2", "xyz789"); err != nil {
		t.Fatal(err)
	}
	if info, err := ReadPanelInfo(ctx, root); err != nil || info.Username != "admin2" {
		t.Fatal(info, err)
	}
}
