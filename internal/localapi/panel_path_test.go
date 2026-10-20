package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if w := get("/secret-9/"); w.Code != 200 || w.Body.String() != "<html><head></head>panel</html>" {
		t.Fatal("panel page not served under the path", w.Code, w.Body.String())
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
