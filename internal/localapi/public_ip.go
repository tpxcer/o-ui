package localapi

import (
	"context"
	"encoding/json"
	"independent-panel/internal/i18n"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

// 用户已允许面板后端向ipinfo.io查询本机出口；只发出无凭据GET，结果仅缓存在内存。
const (
	publicIPv4URL     = "https://ipinfo.io/json"
	publicIPv6URL     = "https://v6.ipinfo.io/json"
	publicSuccessTTL  = 6 * time.Hour
	publicFailureTTL  = 10 * time.Minute
	publicLookupLimit = 8 * time.Second
)

type publicAddress struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Location string `json:"location"`
	Country  string `json:"country"`
	Org      string `json:"org"`
}

type publicInfo struct {
	CheckedAt time.Time      `json:"checkedAt"`
	IPv4      *publicAddress `json:"ipv4"`
	IPv6      *publicAddress `json:"ipv6"`
	Issues    []string       `json:"issues"`
}

type publicLookup struct {
	mu      sync.Mutex
	fetch   func(context.Context, string) (*publicAddress, error)
	cached  publicInfo
	expires time.Time
}

func newPublicLookup() *publicLookup { return &publicLookup{fetch: fetchIPInfo} }

func (p *publicLookup) get() publicInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Now().Before(p.expires) {
		return p.cached
	}
	ctx, cancel := context.WithTimeout(context.Background(), publicLookupLimit)
	defer cancel()
	var wg sync.WaitGroup
	var v4, v6 *publicAddress
	var err4, err6 error
	wg.Add(2)
	go func() { defer wg.Done(); v4, err4 = p.fetch(ctx, "tcp4") }()
	go func() { defer wg.Done(); v6, err6 = p.fetch(ctx, "tcp6") }()
	wg.Wait()
	info := publicInfo{CheckedAt: time.Now().UTC(), IPv4: v4, IPv6: v6, Issues: []string{}}
	if err4 != nil {
		info.Issues = append(info.Issues, i18n.T("IPv4公网信息查询失败", "IPv4 public info lookup failed"))
	}
	if err6 != nil {
		info.Issues = append(info.Issues, i18n.T("未查询到IPv6公网地址", "No public IPv6 address found"))
	}
	p.cached, p.expires = info, time.Now().Add(publicSuccessTTL)
	if err4 != nil {
		p.expires = time.Now().Add(publicFailureTTL)
	}
	return info
}

func fetchIPInfo(ctx context.Context, network string) (*publicAddress, error) {
	target := publicIPv4URL
	if network == "tcp6" {
		target = publicIPv6URL
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, i18n.New("公网信息服务返回异常状态", "Public info service returned an error status")
	}
	return parseIPInfo(io.LimitReader(response.Body, 16<<10), network)
}

func parseIPInfo(body io.Reader, network string) (*publicAddress, error) {
	var v struct{ IP, Hostname, City, Region, Country, Org string }
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(v.IP))
	if ip == nil || (ip.To4() != nil) != (network == "tcp4") {
		return nil, i18n.New("公网信息服务返回的地址族不符", "Public info service returned the wrong address family")
	}
	parts := []string{}
	for _, part := range []string{v.City, v.Region, v.Country} {
		if part = cleanPublicText(part); part != "" {
			parts = append(parts, part)
		}
	}
	hostname := cleanPublicText(v.Hostname)
	if hostname == ip.String() {
		hostname = ""
	}
	country := strings.ToUpper(strings.TrimSpace(v.Country))
	if len(country) != 2 || strings.Trim(country, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		country = ""
	}
	return &publicAddress{IP: ip.String(), Hostname: hostname, Location: strings.Join(parts, ", "), Country: country, Org: cleanPublicText(v.Org)}, nil
}

// 外部文本仅作显示：去掉控制字符并限长，前端另行转义。
func cleanPublicText(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(text))
	if runes := []rune(text); len(runes) > 120 {
		text = string(runes[:120])
	}
	return text
}

func registerPublicInfo(mux *http.ServeMux, rt *Runtime) {
	mux.HandleFunc("GET /api/system/public", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil || rt.public == nil {
			respond(w, 503, map[string]string{"error": i18n.T("公网信息查询尚未启用", "Public info lookup is not enabled")})
			return
		}
		respond(w, 200, rt.public.get())
	})
}
