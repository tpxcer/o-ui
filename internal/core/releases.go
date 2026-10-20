package core

import (
	"context"
	"encoding/json"
	"independent-panel/internal/i18n"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultVersion = "26.7.28"
const repository = "https://github.com/XTLS/Xray-core"
const apiBase = "https://api.github.com/repos/XTLS/Xray-core"

var versionPattern = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,2}\.[0-9]{1,2}$`)

func NormalizeVersion(value string) (string, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if !versionPattern.MatchString(value) {
		return "", i18n.New("版本格式应为26.7.28或v26.7.28", "Version should look like 26.7.28 or v26.7.28")
	}
	return value, nil
}

type Release struct {
	Version    string  `json:"version"`
	Prerelease bool    `json:"prerelease"`
	URL        string  `json:"url"`
	Assets     []asset `json:"-"`
}

type asset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	URL    string `json:"browser_download_url"`
}

type githubRelease struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	URL        string  `json:"html_url"`
	Assets     []asset `json:"assets"`
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !officialURL(req.URL, false) {
			return i18n.New("拒绝非官方或过多下载重定向", "Refusing unofficial or excessive download redirects")
		}
		return nil
	}}
}

func officialURL(u *url.URL, api bool) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	if api {
		return u.Host == "api.github.com" && strings.HasPrefix(u.Path, "/repos/XTLS/Xray-core/releases")
	}
	return u.Host == "github.com" || u.Host == "release-assets.githubusercontent.com" || u.Host == "objects.githubusercontent.com"
}

func (m *Manager) get(ctx context.Context, endpoint string, api bool) (*http.Response, error) {
	u, err := url.Parse(endpoint)
	if err != nil || !officialURL(u, api) {
		return nil, i18n.New("拒绝非官方来源", "Refusing an unofficial source")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "independent-xray-panel")
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, i18n.New("连接Xray官方发布服务失败，请检查网络后重试", "Could not reach the official Xray release service; check the network and retry")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, i18n.Errorf("官方发布服务返回HTTP %d（版本不存在或请求受限），未更改当前选择", "Official release service returned HTTP %d (version missing or rate limited); selection unchanged", resp.StatusCode)
	}
	return resp, nil
}

func (m *Manager) fetchJSON(ctx context.Context, endpoint string, value any) error {
	resp, err := m.get(ctx, endpoint, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(value)
}

func parseRelease(r githubRelease) (Release, error) {
	v, err := NormalizeVersion(r.Tag)
	if err != nil || r.Draft || r.Tag != "v"+v || r.URL != repository+"/releases/tag/v"+v {
		return Release{}, i18n.New("官方版本元数据不匹配", "Official release metadata does not match")
	}
	return Release{Version: v, Prerelease: r.Prerelease, URL: r.URL, Assets: r.Assets}, nil
}

func (m *Manager) release(ctx context.Context, version string) (Release, error) {
	var raw githubRelease
	if err := m.fetchJSON(ctx, apiBase+"/releases/tags/v"+version, &raw); err != nil {
		return Release{}, err
	}
	r, err := parseRelease(raw)
	if err != nil || r.Version != version {
		return Release{}, i18n.New("官方版本元数据不匹配", "Official release metadata does not match")
	}
	return r, nil
}

// 列表不是latest选择策略；始终单独补回用户指定的默认版本。
func (m *Manager) Releases(ctx context.Context) ([]Release, error) {
	var raw []githubRelease
	if err := m.fetchJSON(ctx, apiBase+"/releases?per_page=50", &raw); err != nil {
		return nil, err
	}
	result := make([]Release, 0, len(raw)+1)
	seen := map[string]bool{}
	for _, item := range raw {
		r, err := parseRelease(item)
		if err == nil && !seen[r.Version] {
			result = append(result, r)
			seen[r.Version] = true
		}
	}
	if !seen[DefaultVersion] {
		r, err := m.release(ctx, DefaultVersion)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}

// LatestStable返回官方正式版（不含预发布）中版本号最高者，不依赖列表顺序。
func LatestStable(releases []Release) (string, error) {
	best, bestParts := "", [3]int{}
	for _, r := range releases {
		if r.Prerelease || !versionPattern.MatchString(r.Version) {
			continue
		}
		var parts [3]int
		for i, field := range strings.Split(r.Version, ".") {
			parts[i], _ = strconv.Atoi(field)
		}
		if best == "" || parts[0] > bestParts[0] || (parts[0] == bestParts[0] && (parts[1] > bestParts[1] || (parts[1] == bestParts[1] && parts[2] > bestParts[2]))) {
			best, bestParts = r.Version, parts
		}
	}
	if best == "" {
		return "", i18n.New("官方发布列表中没有正式版", "No stable release found in the official release list")
	}
	return best, nil
}

func assetName(osName, arch string) (string, error) {
	platforms := map[string]string{
		"darwin/arm64": "macos-arm64-v8a", "darwin/amd64": "macos-64",
		"linux/amd64": "linux-64", "linux/arm64": "linux-arm64-v8a",
	}
	platform, ok := platforms[osName+"/"+arch]
	if !ok {
		return "", i18n.Errorf("暂不支持本机架构%s/%s，不猜测下载包", "Architecture %s/%s is not supported; not guessing a package", osName, arch)
	}
	return "Xray-" + platform + ".zip", nil
}
