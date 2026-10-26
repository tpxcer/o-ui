package localapi

import (
	"bytes"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// 只为已知页面返回入口；API、订阅和未知路径不走SPA回退。
func panelPages(dist, base string) http.Handler {
	files := http.FileServer(http.Dir(dist))
	lang := os.Getenv("O_UI_DEFAULT_LANG")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		page := path == "/"
		for _, segment := range []string{"panel", "inbounds", "clients", "settings", "xray"} {
			if path == "/"+segment || path == "/"+segment+"/" {
				page = true
				break
			}
		}
		if !page || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			files.ServeHTTP(w, r)
			return
		}
		if !strings.HasSuffix(path, "/") {
			target := base + strings.TrimPrefix(path, "/") + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		pageBytes, err := os.ReadFile(filepath.Join(dist, "index.html"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		injected := "<head>"
		if lang == "zh" || lang == "en" {
			injected += `<meta name="o-ui-default-lang" content="` + lang + `">`
		}
		injected += `<base data-panel-base href="` + html.EscapeString(base) + `">`
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodHead {
			_, _ = w.Write(bytes.Replace(pageBytes, []byte("<head>"), []byte(injected), 1))
		}
	})
}
