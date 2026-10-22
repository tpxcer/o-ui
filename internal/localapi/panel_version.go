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
	"unicode"
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
	notes   []releaseNote
	issue   string
	checked time.Time
	fetch   func(context.Context) (latestRelease, error)
	now     func() time.Time
}

type latestRelease struct {
	tag, url string
	notes    []releaseNote
}

// releaseNote是发布说明里的一条要点，分中英两种语言，网页与机器人按界面语言取用。
type releaseNote struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// releaseHighlights取发布说明开头的要点（“- ”开头，到第一个空行为止），即本次更新的主要内容；最多10条。
// 每条按“中文 / English”拆开：取其后不再出现汉字的那个“ / ”，中文部分里的“Chrome / Edge”之类不会被拆错。
func releaseHighlights(body string) []releaseNote {
	notes := []releaseNote{}
	plain := strings.NewReplacer("**", "", "`", "")
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		text, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
		if !ok {
			if len(notes) > 0 {
				break
			}
			continue
		}
		text = plain.Replace(strings.TrimSpace(text))
		note := releaseNote{Zh: text, En: text}
		for from := 0; ; {
			at := strings.Index(text[from:], " / ")
			if at < 0 {
				break
			}
			at += from
			if zh, en := strings.TrimSpace(text[:at]), strings.TrimSpace(text[at+3:]); hasHan(zh) && !hasHan(en) {
				note = releaseNote{Zh: zh, En: en}
				break
			}
			from = at + 3
		}
		notes = append(notes, releaseNote{Zh: clip(note.Zh, 300), En: clip(note.En, 300)})
		if len(notes) == 10 {
			break
		}
	}
	return notes
}

func hasHan(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// 只有手动点“检测更新”才访问GitHub；1分钟内重复点击用上次结果，避免触发GitHub未登录每小时60次的限制。
const updateCheckCache = time.Minute

type panelVersion struct {
	Current         string         `json:"current"`
	Latest          string         `json:"latest,omitempty"`
	URL             string         `json:"url,omitempty"`
	Notes           []releaseNote  `json:"notes,omitempty"`
	UpdateAvailable bool           `json:"updateAvailable"`
	Issue           string         `json:"issue,omitempty"`
	CanUpgrade      bool           `json:"canUpgrade"`
	Update          *upgradeStatus `json:"update,omitempty"`
}

// 网页升级（参照OUI）：经sudo无参数调用root升级助手，由它在独立的systemd临时服务里运行“o-ui update”
// （升级到GitHub最新正式版，下载校验与失败回退同命令行），面板停止重启时不受影响；结果写在root拥有的状态文件里。
const upgradeHelper = "/usr/local/libexec/o-ui-update"

var (
	upgradeStatusFile = "/var/lib/o-ui-update/status.json"
	upgradeReady      = func() bool { _, err := os.Stat(upgradeHelper); return err == nil }
	runUpgrade        = func(ctx context.Context) (string, error) { return helperCommand(ctx, upgradeHelper) }
)

type upgradeStatus struct {
	State string `json:"state"`
	From  string `json:"from"`
	To    string `json:"to"`
	Code  int    `json:"code"`
	At    int64  `json:"at"`
}

func readUpgradeStatus() *upgradeStatus {
	data, err := os.ReadFile(upgradeStatusFile)
	var status upgradeStatus
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &status) != nil || (status.State != "running" && status.State != "done" && status.State != "failed") {
		return nil
	}
	return &status
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

func fetchLatestRelease(ctx context.Context) (latestRelease, error) {
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPI, nil)
	if err != nil {
		return latestRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "o-ui")
	response, err := client.Do(request)
	if err != nil {
		return latestRelease{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return latestRelease{}, i18n.New("GitHub 返回异常状态", "GitHub returned an error status")
	}
	var release struct {
		Tag  string `json:"tag_name"`
		URL  string `json:"html_url"`
		Body string `json:"body"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&release) != nil || !validVersion(release.Tag) || !strings.HasPrefix(release.URL, "https://github.com/") {
		return latestRelease{}, i18n.New("GitHub 返回的版本信息无效", "GitHub returned invalid release information")
	}
	return latestRelease{tag: release.Tag, url: release.URL, notes: releaseHighlights(release.Body)}, nil
}

func (p *panelUpdates) get(ctx context.Context, check bool) panelVersion {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	if check && (p.checked.IsZero() || now().Sub(p.checked) >= updateCheckCache) {
		release, err := p.fetch(ctx)
		p.checked, p.issue = now(), ""
		if err != nil {
			p.issue = i18n.T("无法连接 GitHub 检查更新", "Cannot reach GitHub to check for updates")
		} else {
			p.latest, p.url, p.notes = release.tag, release.url, release.notes
		}
	}
	return panelVersion{Current: p.current, Latest: p.latest, URL: p.url, Notes: p.notes, UpdateAvailable: newerVersion(p.latest, p.current), Issue: p.issue}
}

func registerPanelVersion(mux *http.ServeMux) {
	registerPanelUpdates(mux, &panelUpdates{current: installedVersion(), fetch: fetchLatestRelease})
}

func registerPanelUpdates(mux *http.ServeMux, updates *panelUpdates) {
	mux.HandleFunc("GET /api/panel/version", func(w http.ResponseWriter, r *http.Request) {
		v := updates.get(r.Context(), r.URL.Query().Get("check") == "1")
		v.CanUpgrade, v.Update = upgradeReady(), readUpgradeStatus()
		respond(w, 200, v)
	})
	// 立即升级：重新向GitHub确认最新正式版确实更新，再启动升级助手；升级期间面板与代理会短暂中断。
	mux.HandleFunc("POST /api/panel/update", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Confirm bool `json:"confirm"`
		}
		if !readJSON(w, r, &request, 1024) {
			return
		}
		if !request.Confirm {
			respond(w, 400, map[string]string{"error": i18n.T("请确认升级", "Confirm the update")})
			return
		}
		if !upgradeReady() {
			respond(w, 409, map[string]string{"error": i18n.T("服务器上还没有网页升级助手，请先在服务器上运行一次 o-ui update", "The web update helper is not installed yet; run o-ui update on the server once")})
			return
		}
		release, err := updates.fetch(r.Context())
		latest := release.tag
		if err != nil {
			respond(w, 502, map[string]string{"error": i18n.T("无法连接 GitHub 检查更新", "Cannot reach GitHub to check for updates")})
			return
		}
		updates.mu.Lock()
		updates.latest, updates.url, updates.notes, updates.issue, updates.checked = release.tag, release.url, release.notes, "", time.Now()
		current := updates.current
		updates.mu.Unlock()
		if !newerVersion(latest, current) {
			respond(w, 409, map[string]string{"error": i18n.Errorf("当前已是最新版本 %s", "Already on the latest version %s", current).Error()})
			return
		}
		if output, err := runUpgrade(r.Context()); err != nil {
			respond(w, 502, map[string]string{"error": i18n.Errorf("启动升级失败：%s", "Failed to start the update: %s", output).Error()})
			return
		}
		respond(w, 202, map[string]string{"target": latest, "current": current})
	})
}
