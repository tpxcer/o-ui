package localapi

import (
	"context"
	"encoding/json"
	"independent-panel/internal/i18n"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 服务器套餐“服务商拉取”（参照OUI）：搬瓦工/64clouds的getServiceInfo，或返回同样字段的自定义链接。
// 链接里可用{veid}、{api_key}占位，否则自动附加veid与api_key参数；成功结果缓存15分钟，失败1分钟内不重试。
const defaultProviderURL = "https://api.64clouds.com/v1/getServiceInfo"

type providerInfo struct {
	Hostname        string            `json:"hostname"`
	NodeAlias       string            `json:"nodeAlias"`
	NodeLocation    string            `json:"nodeLocation"`
	Plan            string            `json:"plan"`
	OS              string            `json:"os"`
	PlanMonthlyData string            `json:"planMonthlyData"`
	PlanDisk        string            `json:"planDisk"`
	PlanRAM         string            `json:"planRam"`
	PlanSwap        string            `json:"planSwap"`
	DataCounter     string            `json:"dataCounter"`
	DataNextReset   int64             `json:"dataNextReset"`
	IPAddresses     []string          `json:"ipAddresses"`
	PTR             map[string]string `json:"ptr"`
}

type providerCacheEntry struct {
	info *providerInfo
	err  error
	at   time.Time
}

var providerCache = struct {
	sync.Mutex
	entries map[string]providerCacheEntry
}{entries: map[string]providerCacheEntry{}}

var providerClient = &http.Client{Timeout: 8 * time.Second}

func providerEndpoint(base, veid, key string) (string, error) {
	if strings.TrimSpace(base) == "" {
		base = defaultProviderURL
	}
	placeholder := func(names ...string) bool {
		for _, name := range names {
			if strings.Contains(base, "{"+name+"}") || strings.Contains(base, "%7B"+name+"%7D") {
				return true
			}
		}
		return false
	}
	hasVEID, hasKey := placeholder("veid", "VEID"), placeholder("api_key", "API_KEY", "apiKey", "APIKey")
	replaced := strings.NewReplacer("{veid}", url.QueryEscape(veid), "{VEID}", url.QueryEscape(veid), "%7Bveid%7D", url.QueryEscape(veid), "%7BVEID%7D", url.QueryEscape(veid),
		"{api_key}", url.QueryEscape(key), "{API_KEY}", url.QueryEscape(key), "{apiKey}", url.QueryEscape(key), "{APIKey}", url.QueryEscape(key),
		"%7Bapi_key%7D", url.QueryEscape(key), "%7BAPI_KEY%7D", url.QueryEscape(key), "%7BapiKey%7D", url.QueryEscape(key), "%7BAPIKey%7D", url.QueryEscape(key)).Replace(base)
	endpoint, err := url.Parse(replaced)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return "", i18n.New("拉取链接须以 http:// 或 https:// 开头", "The provider URL must start with http:// or https://")
	}
	query := endpoint.Query()
	if !hasVEID {
		query.Set("veid", veid)
	}
	if !hasKey {
		query.Set("api_key", key)
	}
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func rawText(raw map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		if value, ok := raw[name]; ok {
			var s string
			if json.Unmarshal(value, &s) == nil {
				return strings.TrimSpace(s)
			}
			var n json.Number
			if json.Unmarshal(value, &n) == nil {
				return n.String()
			}
		}
	}
	return ""
}

// rawCount把数字或数字字符串转成十进制字符串，无效时为空。
func rawCount(raw map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		text := rawText(raw, name)
		if f, err := strconv.ParseFloat(text, 64); err == nil && f >= 0 && f < 1e19 {
			return strconv.FormatUint(uint64(f), 10)
		}
	}
	return ""
}

// providerError：error为0、false或空时视为成功；success:false时取message。
func providerError(raw map[string]json.RawMessage) string {
	if value, ok := raw["error"]; ok {
		var b bool
		var n float64
		var s string
		switch {
		case json.Unmarshal(value, &b) == nil:
			if b {
				return rawText(raw, "message", "msg")
			}
		case json.Unmarshal(value, &n) == nil:
			if n != 0 {
				if message := rawText(raw, "message", "msg"); message != "" {
					return message
				}
				return strconv.FormatFloat(n, 'f', -1, 64)
			}
		case json.Unmarshal(value, &s) == nil:
			if s = strings.TrimSpace(s); s != "" && s != "0" {
				return s
			}
		}
	}
	if value, ok := raw["success"]; ok {
		var b bool
		if json.Unmarshal(value, &b) == nil && !b {
			if message := rawText(raw, "message", "msg"); message != "" {
				return message
			}
			return "success=false"
		}
	}
	return ""
}

func parseProviderInfo(data []byte) (*providerInfo, error) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil, i18n.New("服务商接口返回的不是JSON对象", "The provider API did not return a JSON object")
	}
	if message := providerError(raw); message != "" {
		return nil, i18n.Errorf("服务商接口错误：%s", "Provider API error: %s", message)
	}
	fields := raw
	if nested, ok := raw["data"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(nested, &inner) == nil && len(inner) > 0 {
			fields = inner
		}
	}
	info := &providerInfo{
		Hostname: rawText(fields, "hostname", "hostName"), NodeAlias: rawText(fields, "node_alias", "nodeAlias", "node"),
		NodeLocation: rawText(fields, "node_location", "nodeLocation", "location"), Plan: rawText(fields, "plan"), OS: rawText(fields, "os", "operatingSystem"),
		PlanMonthlyData: rawCount(fields, "plan_monthly_data", "planMonthlyData", "monthly_data", "monthlyData"),
		PlanDisk:        rawCount(fields, "plan_disk", "planDisk", "disk"), PlanRAM: rawCount(fields, "plan_ram", "planRam", "ram", "memory"), PlanSwap: rawCount(fields, "plan_swap", "planSwap", "swap"),
		DataCounter: rawCount(fields, "data_counter", "dataCounter", "used_data", "usedData"),
		IPAddresses: []string{}, PTR: map[string]string{},
	}
	if reset, err := strconv.ParseInt(rawCount(fields, "data_next_reset", "dataNextReset", "next_reset", "nextReset"), 10, 64); err == nil {
		info.DataNextReset = reset
	}
	for _, name := range []string{"ip_addresses", "ipAddresses", "ips"} {
		if value, ok := fields[name]; ok && json.Unmarshal(value, &info.IPAddresses) == nil {
			break
		}
	}
	for _, name := range []string{"ptr", "rdns"} {
		if value, ok := fields[name]; ok && json.Unmarshal(value, &info.PTR) == nil {
			break
		}
	}
	if info.IPAddresses == nil {
		info.IPAddresses = []string{}
	}
	if info.PTR == nil {
		info.PTR = map[string]string{}
	}
	return info, nil
}

func fetchProvider(ctx context.Context, base, veid, key string) (*providerInfo, error) {
	if strings.TrimSpace(veid) == "" || key == "" {
		return nil, i18n.New("请在“面板设置 → 服务器套餐”填写 VEID 和 API KEY", "Fill in VEID and API KEY under Panel settings → Server plan")
	}
	cacheKey := base + "\x00" + veid + "\x00" + key
	providerCache.Lock()
	entry, ok := providerCache.entries[cacheKey]
	providerCache.Unlock()
	if ok && ((entry.err == nil && time.Since(entry.at) < 15*time.Minute) || (entry.err != nil && time.Since(entry.at) < time.Minute)) {
		return entry.info, entry.err
	}
	info, err := requestProvider(ctx, base, veid, key)
	providerCache.Lock()
	providerCache.entries = map[string]providerCacheEntry{cacheKey: {info: info, err: err, at: time.Now()}}
	providerCache.Unlock()
	return info, err
}

func requestProvider(ctx context.Context, base, veid, key string) (*providerInfo, error) {
	endpoint, err := providerEndpoint(base, veid, key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, i18n.New("拉取链接无效", "Invalid provider URL")
	}
	request.Header.Set("User-Agent", "o-ui")
	response, err := providerClient.Do(request)
	if err != nil {
		return nil, i18n.New("无法连接服务商接口", "Cannot reach the provider API")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, i18n.Errorf("服务商接口返回 HTTP %d", "The provider API returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, i18n.New("读取服务商接口失败", "Failed to read the provider API")
	}
	return parseProviderInfo(data)
}
