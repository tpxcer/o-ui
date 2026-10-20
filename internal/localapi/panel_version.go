package localapi

import (
	"context"
	"encoding/json"
	"independent-panel/internal/i18n"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const releaseAPI = "https://api.github.com/repos/tpxcer/o-ui/releases/latest"

var releaseTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.]+)?$`)

// panelUpdates只读当前包版本并按需查询GitHub最新正式版；不在网页里执行升级。
type panelUpdates struct {
	mu      sync.Mutex
	current string
	latest  string
	url     string
	issue   string
	checked time.Time
	fetch   func(context.Context) (string, string, error)
}

type panelVersion struct {
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	URL             string `json:"url,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Issue           string `json:"issue,omitempty"`
}

// 版本来自Release包根目录VERSION（bin/o-ui的上级目录）；开发运行时为dev。
func installedVersion() string {
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return "dev"
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(executable)), "VERSION"))
	if version := strings.TrimSpace(string(data)); err == nil && releaseTag.MatchString(version) {
		return version
	}
	return "dev"
}

func newerVersion(latest, current string) bool {
	a, b := releaseTag.FindStringSubmatch(latest), releaseTag.FindStringSubmatch(current)
	if a == nil || a[4] != "" {
		return false
	}
	if b == nil {
		return true
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x > y
		}
	}
	return b[4] != ""
}

func fetchLatestRelease(ctx context.Context) (string, string, error) {
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPI, nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "o-ui")
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", i18n.New("GitHub 返回异常状态", "GitHub returned an error status")
	}
	var release struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&release) != nil || !releaseTag.MatchString(release.Tag) || !strings.HasPrefix(release.URL, "https://github.com/") {
		return "", "", i18n.New("GitHub 返回的版本信息无效", "GitHub returned invalid release information")
	}
	return release.Tag, release.URL, nil
}

func (p *panelUpdates) get(ctx context.Context, check bool) panelVersion {
	p.mu.Lock()
	defer p.mu.Unlock()
	if check && (p.checked.IsZero() || time.Since(p.checked) > 10*time.Minute) {
		latest, url, err := p.fetch(ctx)
		p.checked, p.issue = time.Now(), ""
		if err != nil {
			p.issue = i18n.T("无法连接 GitHub 检查更新", "Cannot reach GitHub to check for updates")
		} else {
			p.latest, p.url = latest, url
		}
	}
	return panelVersion{Current: p.current, Latest: p.latest, URL: p.url, UpdateAvailable: newerVersion(p.latest, p.current), Issue: p.issue}
}

func registerPanelVersion(mux *http.ServeMux) {
	updates := &panelUpdates{current: installedVersion(), fetch: fetchLatestRelease}
	mux.HandleFunc("GET /api/panel/version", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, updates.get(r.Context(), r.URL.Query().Get("check") == "1"))
	})
}
