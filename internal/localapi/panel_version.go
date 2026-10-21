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

// 版本号按发布日期（用户2026-10-21决定）：当天第一个为2026-10-21，第二个起为2026-10-21-v1、-v2……；
// 兼容此前的vX.Y.Z（含预发布后缀），任何日期版本都比旧版本新。
var (
	releaseTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.]+)?$`)
	dateTag    = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(?:-v([1-9][0-9]{0,3}))?$`)
)

type versionKey struct {
	kind       int // 1为旧vX.Y.Z，2为日期版本
	parts      [4]int
	prerelease bool
}

func parseVersion(version string) (versionKey, bool) {
	if m := dateTag.FindStringSubmatch(version); m != nil {
		day, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			return versionKey{}, false
		}
		n, _ := strconv.Atoi(m[2])
		return versionKey{kind: 2, parts: [4]int{day.Year(), int(day.Month()), day.Day(), n}}, true
	}
	if m := releaseTag.FindStringSubmatch(version); m != nil {
		key := versionKey{kind: 1, prerelease: m[4] != ""}
		for i := 0; i < 3; i++ {
			key.parts[i], _ = strconv.Atoi(m[i+1])
		}
		return key, true
	}
	return versionKey{}, false
}

func validVersion(version string) bool {
	_, ok := parseVersion(version)
	return ok
}

// panelUpdates只读当前包版本并按需查询GitHub最新正式版；不在网页里执行升级。
type panelUpdates struct {
	mu      sync.Mutex
	current string
	latest  string
	url     string
	issue   string
	checked time.Time
	fetch   func(context.Context) (string, string, error)
	now     func() time.Time
}

// 只有手动点“检测更新”才访问GitHub；1分钟内重复点击用上次结果，避免触发GitHub未登录每小时60次的限制。
const updateCheckCache = time.Minute

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
	if version := strings.TrimSpace(string(data)); err == nil && validVersion(version) {
		return version
	}
	return "dev"
}

func newerVersion(latest, current string) bool {
	a, ok := parseVersion(latest)
	if !ok || a.prerelease {
		return false
	}
	b, ok := parseVersion(current)
	if !ok {
		return true
	}
	if a.kind != b.kind {
		return a.kind > b.kind
	}
	for i := range a.parts {
		if a.parts[i] != b.parts[i] {
			return a.parts[i] > b.parts[i]
		}
	}
	return b.prerelease
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
	if json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&release) != nil || !validVersion(release.Tag) || !strings.HasPrefix(release.URL, "https://github.com/") {
		return "", "", i18n.New("GitHub 返回的版本信息无效", "GitHub returned invalid release information")
	}
	return release.Tag, release.URL, nil
}

func (p *panelUpdates) get(ctx context.Context, check bool) panelVersion {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	if check && (p.checked.IsZero() || now().Sub(p.checked) >= updateCheckCache) {
		latest, url, err := p.fetch(ctx)
		p.checked, p.issue = now(), ""
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
