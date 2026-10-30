package config

import (
	"encoding/base64"
	"encoding/hex"
	"independent-panel/internal/i18n"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Sniffing struct {
	Enabled         bool     `json:"enabled"`
	DestOverride    []string `json:"destOverride"`
	RouteOnly       bool     `json:"routeOnly"`
	MetadataOnly    bool     `json:"metadataOnly"`
	IPsExcluded     []string `json:"ipsExcluded,omitempty"`
	DomainsExcluded []string `json:"domainsExcluded,omitempty"`
}

func ValidateSniffing(s Sniffing) error {
	if len(s.IPsExcluded) > 64 || len(s.DomainsExcluded) > 64 {
		return i18n.New("嗅探排除规则每组最多64项", "Each sniffing exclusion list allows at most 64 entries")
	}
	for _, rule := range s.IPsExcluded {
		if net.ParseIP(rule) == nil {
			if _, _, err := net.ParseCIDR(rule); err != nil {
				return i18n.New("嗅探排除IP仅支持IP或CIDR，不支持外部Geo文件", "Sniffing IP exclusions accept only IPs or CIDRs; external Geo files are not supported")
			}
		}
	}
	for _, rule := range s.DomainsExcluded {
		value := rule
		if strings.HasPrefix(rule, "domain:") {
			value = strings.TrimPrefix(rule, "domain:")
		} else if strings.HasPrefix(rule, "full:") {
			value = strings.TrimPrefix(rule, "full:")
		}
		if len(rule) > 256 || !validHost(value) || net.ParseIP(value) != nil || strings.Contains(value, ":") {
			return i18n.New("嗅探排除域名仅支持域名、domain:域名或full:域名", "Sniffing domain exclusions accept only domain, domain:domain or full:domain")
		}
	}
	return nil
}

type Reality struct {
	Target       string   `json:"target"`
	ServerNames  []string `json:"serverNames"`
	PrivateKey   string   `json:"privateKey"`
	ShortIDs     []string `json:"shortIds"`
	MinClientVer string   `json:"minClientVer,omitempty"`
}

type Certificate struct {
	File string `json:"certificateFile"`
	Key  string `json:"keyFile"`
}

type XHTTP struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// 输入是独立业务记录，不含运行指令，也不负责读取证书、生成凭据或保存文件。
type Inbound struct {
	ID          string
	Preset      string
	Listen      string
	Port        int
	Enabled     bool
	Sniffing    Sniffing
	Reality     *Reality
	Certificate *Certificate
	XHTTP       *XHTTP
	NoVision    bool // 仅reality预设：不加xtls-rprx-vision流控（兼容从OUI导入的无流控客户端）
}

type Client struct {
	ID         string
	InboundID  string
	InboundIDs []string
	Enabled    bool
	ExpiresAt  *time.Time
	QuotaBytes uint64
	UsedBytes  uint64
	IPLimit    uint32
	UUID       string
	Auth       string
	ReverseTag string
}

// LinkedInbounds兼容旧版单入站；显式空列表不会退回旧字段。
func LinkedInbounds(primary string, ids []string) []string {
	if ids != nil {
		return ids
	}
	if primary == "" {
		return nil
	}
	return []string{primary}
}

type User struct {
	ID      string       `json:"id,omitempty"`
	Auth    string       `json:"auth,omitempty"`
	Flow    string       `json:"flow,omitempty"`
	Level   uint32       `json:"level"`
	Email   string       `json:"email"`
	Reverse *UserReverse `json:"reverse,omitempty"`
}

// UserReverse让VLESS客户端作为反向代理的桥接端接入：其隧道在本机成为同名出站，供路由使用。
type UserReverse struct {
	Tag string `json:"tag"`
}

// ReverseTagPattern是反向代理标签的允许格式（门户端客户端与桥接端出站共用）。
var ReverseTagPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

type ProtocolSettings struct {
	Clients    []User `json:"clients"`
	Decryption string `json:"decryption,omitempty"`
	Version    int    `json:"version,omitempty"`
}

type TLSSettings struct {
	Certificates []Certificate `json:"certificates"`
	ALPN         []string      `json:"alpn"`
	MinVersion   string        `json:"minVersion"`
}

type HysteriaSettings struct {
	Version int `json:"version"`
}

type StreamSettings struct {
	Network  string            `json:"network"`
	Security string            `json:"security"`
	Reality  *Reality          `json:"realitySettings,omitempty"`
	TLS      *TLSSettings      `json:"tlsSettings,omitempty"`
	XHTTP    *XHTTP            `json:"xhttpSettings,omitempty"`
	Hysteria *HysteriaSettings `json:"hysteriaSettings,omitempty"`
}

type CompiledInbound struct {
	Tag      string           `json:"tag"`
	Listen   string           `json:"listen"`
	Port     int              `json:"port"`
	Protocol string           `json:"protocol"`
	Settings ProtocolSettings `json:"settings"`
	Stream   StreamSettings   `json:"streamSettings"`
	Sniffing Sniffing         `json:"sniffing"`
	// PortHopping是HY2端口跳跃范围（如48000-48199）：服务端由防火墙助手把该UDP范围转发到监听端口，
	// 不写入Xray配置，只用于生成客户端连接参数。
	PortHopping string `json:"-"`
}

// ParsePortRange解析“起始-结束”端口范围（1024至65535，起始小于结束，不带前导零，与防火墙助手的校验一致）。
func ParsePortRange(value string) (start, end int, ok bool) {
	a, b, found := strings.Cut(value, "-")
	if !found || !validPortString(a) || !validPortString(b) {
		return 0, 0, false
	}
	start, _ = strconv.Atoi(a)
	end, _ = strconv.Atoi(b)
	return start, end, start >= 1024 && start < end && strconv.Itoa(start)+"-"+strconv.Itoa(end) == value
}

var recordID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var hostname = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

func validHost(value string) bool {
	return net.ParseIP(value) != nil || (len(value) <= 253 && hostname.MatchString(value) && !strings.Contains(value, ".."))
}

func stream(in Inbound) (StreamSettings, error) {
	result := StreamSettings{Network: "tcp", Security: "reality"}
	if in.Preset == "hy2" || in.Preset == "xhttp-tls" {
		cert := in.Certificate
		if cert == nil || !filepath.IsAbs(cert.File) || !filepath.IsAbs(cert.Key) || cert.File == cert.Key || strings.ContainsAny(cert.File+cert.Key, "\x00\r\n") {
			return result, i18n.New("TLS预设需要不同的证书/私钥绝对路径；本步骤不读取或验证证书", "TLS presets need distinct absolute certificate/key paths; this step does not read or verify them")
		}
		result.Security = "tls"
		result.TLS = &TLSSettings{Certificates: []Certificate{*cert}, ALPN: []string{"h2", "http/1.1"}, MinVersion: "1.2"}
	} else {
		r := in.Reality
		if r == nil {
			return result, i18n.New("REALITY配置缺失", "REALITY settings are missing")
		}
		host, port, err := net.SplitHostPort(r.Target)
		if err != nil || !validHost(host) || !validPortString(port) {
			return result, i18n.New("REALITY目标需要明确的主机和端口", "REALITY target needs an explicit host and port")
		}
		key, err := base64.RawURLEncoding.DecodeString(r.PrivateKey)
		if err != nil || len(key) != 32 {
			return result, i18n.New("REALITY私钥格式无效", "Invalid REALITY private key format")
		}
		if len(r.ServerNames) == 0 || len(r.ShortIDs) == 0 {
			return result, i18n.New("REALITY需要serverNames和shortIds", "REALITY requires serverNames and shortIds")
		}
		for _, name := range r.ServerNames {
			if !validHost(name) || net.ParseIP(name) != nil {
				return result, i18n.New("REALITY的serverNames需为有效域名", "REALITY serverNames must be valid domains")
			}
		}
		for _, id := range r.ShortIDs {
			if !ValidShortID(id) {
				return result, i18n.New("REALITY的shortId需为2至16位偶数长度十六进制", "REALITY shortId must be hex with an even length of 2–16")
			}
		}
		if r.MinClientVer != "" && !ValidClientVersion(r.MinClientVer) {
			return result, i18n.New("REALITY最低客户端版本须为x.y.z（每段0至255）", "REALITY minimum client version must be x.y.z (each part 0–255)")
		}
		result.Reality = &Reality{Target: r.Target, ServerNames: append([]string{}, r.ServerNames...), PrivateKey: r.PrivateKey, ShortIDs: append([]string{}, r.ShortIDs...), MinClientVer: r.MinClientVer}
	}
	if in.Preset == "hy2" {
		result.Network = "hysteria"
		result.Hysteria = &HysteriaSettings{Version: 2}
		result.TLS.ALPN, result.TLS.MinVersion = []string{"h3"}, "1.3"
	} else if strings.HasPrefix(in.Preset, "xhttp-") {
		x := in.XHTTP
		if x == nil || !strings.HasPrefix(x.Path, "/") || strings.ContainsAny(x.Path, "\x00\r\n?#") {
			return result, i18n.New("XHTTP需要以/开头且不含查询/片段的路径", "XHTTP needs a path starting with / without query or fragment")
		}
		if x.Mode != "auto" && x.Mode != "packet-up" && x.Mode != "stream-up" && x.Mode != "stream-one" {
			return result, i18n.New("XHTTP模式无效", "Invalid XHTTP mode")
		}
		result.Network, result.XHTTP = "xhttp", &XHTTP{Path: x.Path, Mode: x.Mode}
	}
	return result, nil
}

// ValidShortID：REALITY shortId为2至16位偶数长度十六进制。
func ValidShortID(id string) bool {
	_, err := hex.DecodeString(id)
	return err == nil && len(id) > 0 && len(id) <= 16
}

// ValidUUID：VLESS客户端UUID，不接受全零。
func ValidUUID(id string) bool {
	return uuidPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

// ValidClientVersion：Xray按x.y.z三段（每段0至255）解析REALITY的最低/最高客户端版本。
func ValidClientVersion(version string) bool {
	parts := strings.Split(version, ".")
	for _, part := range parts {
		if n, err := strconv.Atoi(part); err != nil || n < 0 || n > 255 || strconv.Itoa(n) != part {
			return false
		}
	}
	return len(parts) == 3
}

// ValidHY2Auth：HY2认证值为16至256字节且不含空白/控制符（本面板生成的与从OUI导入的都要满足）。
func ValidHY2Auth(auth string) bool {
	return len(auth) >= 16 && len(auth) <= 256 && !strings.ContainsFunc(auth, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func validPortString(value string) bool {
	n := 0
	if value == "" || len(value) > 5 {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
		n = n*10 + int(digit-'0')
	}
	return n > 0 && n <= 65535
}

// CompileInbounds是纯编译：不会启动核心或联网。到期/限额只按传入快照过滤，持续执行仍需运行管理层。
func CompileInbounds(inbounds []Inbound, clients []Client, now time.Time) ([]CompiledInbound, error) {
	if now.IsZero() {
		return nil, i18n.New("必须提供明确的校验时间", "An explicit validation time is required")
	}
	byID := map[string]Inbound{}
	for _, in := range inbounds {
		if !recordID.MatchString(in.ID) {
			return nil, i18n.New("入站标识无效", "Invalid inbound ID")
		}
		if _, exists := byID[in.ID]; exists {
			return nil, i18n.New("入站标识重复", "Duplicate inbound ID")
		}
		if in.Preset != "reality" && in.Preset != "hy2" && in.Preset != "xhttp-tls" && in.Preset != "xhttp-reality" {
			return nil, i18n.New("未支持的入站预设", "Unsupported inbound preset")
		}
		if net.ParseIP(in.Listen) == nil || in.Port < 1 || in.Port > 65535 {
			return nil, i18n.New("入站需要明确的监听IP及1至65535端口", "Inbound needs an explicit listen IP and a port from 1 to 65535")
		}
		for _, protocol := range in.Sniffing.DestOverride {
			if protocol != "http" && protocol != "tls" && protocol != "quic" && protocol != "fakedns" {
				return nil, i18n.New("嗅探协议无效", "Invalid sniffing protocol")
			}
		}
		if err := ValidateSniffing(in.Sniffing); err != nil {
			return nil, err
		}
		byID[in.ID] = in
	}
	users := map[string][]User{}
	seenClients, seenCredentials := map[string]bool{}, map[string]bool{}
	for _, client := range clients {
		if !recordID.MatchString(client.ID) || seenClients[client.ID] {
			return nil, i18n.New("客户端标识无效或重复", "Invalid or duplicate client ID")
		}
		seenClients[client.ID] = true
		linked := LinkedInbounds(client.InboundID, client.InboundIDs)
		if len(linked) == 0 || (client.InboundID != "" && client.InboundID != linked[0]) {
			return nil, i18n.New("客户端关联入站无效", "Invalid client inbound association")
		}
		if client.ReverseTag != "" && len(linked) != 1 {
			return nil, i18n.New("反向代理客户端只能关联一个入站", "Reverse proxy clients may link only one inbound")
		}
		seen := map[string]bool{}
		for _, inboundID := range linked {
			if seen[inboundID] {
				return nil, i18n.New("客户端关联入站重复", "Duplicate client inbound association")
			}
			seen[inboundID] = true
			in, exists := byID[inboundID]
			if !exists {
				return nil, i18n.New("客户端关联了不存在的入站", "Client is linked to a missing inbound")
			}
			if !client.Enabled || !in.Enabled || (client.ExpiresAt != nil && !client.ExpiresAt.After(now)) || (client.QuotaBytes > 0 && client.UsedBytes >= client.QuotaBytes) {
				continue
			}
			user := User{Email: "client-" + client.ID}
			if in.Preset == "hy2" {
				if !ValidHY2Auth(client.Auth) {
					return nil, i18n.New("HY2客户端需要16至256字节无空白/控制符的认证值", "HY2 clients need a 16–256 byte auth value without spaces or control characters")
				}
				user.Auth = client.Auth
			} else {
				if !ValidUUID(client.UUID) {
					return nil, i18n.New("VLESS客户端UUID格式无效", "Invalid VLESS client UUID")
				}
				user.ID = strings.ToLower(client.UUID)
				if in.Preset == "reality" && !in.NoVision {
					user.Flow = "xtls-rprx-vision"
				}
			}
			if client.ReverseTag != "" {
				if in.Preset == "hy2" || !ReverseTagPattern.MatchString(client.ReverseTag) {
					return nil, i18n.New("反向代理标签只用于VLESS入站，且只能含字母、数字、点、下划线和连字符", "Reverse tags apply only to VLESS inbounds and may contain only letters, digits, dots, underscores and hyphens")
				}
				user.Reverse = &UserReverse{Tag: client.ReverseTag}
			}
			identity := in.ID + ":" + user.ID + user.Auth
			if seenCredentials[identity] {
				return nil, i18n.New("同入站存在重复客户端认证值", "Duplicate client credentials in the same inbound")
			}
			seenCredentials[identity] = true
			users[in.ID] = append(users[in.ID], user)
		}
	}

	result := []CompiledInbound{}
	for _, in := range inbounds {
		if !in.Enabled || len(users[in.ID]) == 0 {
			continue // 无有效用户时不输出监听，避免协议的空用户配置退化为无认证。
		}
		settings, err := stream(in)
		if err != nil {
			return nil, i18n.Wrap("入站"+in.ID+"：", "Inbound "+in.ID+": ", err)
		}
		for _, other := range result {
			if (other.Protocol == "hysteria") != (in.Preset == "hy2") || other.Port != in.Port {
				continue
			}
			a, b := net.ParseIP(other.Listen), net.ParseIP(in.Listen)
			if a.Equal(b) || a.IsUnspecified() || b.IsUnspecified() {
				return nil, i18n.New("同传输监听地址/端口冲突；此处不探测系统占用", "Listen address/port conflict for the same transport (system usage is not probed here)")
			}
		}
		compiled := CompiledInbound{Tag: "in-" + in.ID, Listen: in.Listen, Port: in.Port, Protocol: "vless", Settings: ProtocolSettings{Clients: users[in.ID], Decryption: "none"}, Stream: settings, Sniffing: in.Sniffing}
		compiled.Sniffing.DestOverride = append([]string{}, in.Sniffing.DestOverride...)
		compiled.Sniffing.IPsExcluded = append([]string{}, in.Sniffing.IPsExcluded...)
		compiled.Sniffing.DomainsExcluded = append([]string{}, in.Sniffing.DomainsExcluded...)
		if in.Preset == "hy2" {
			compiled.Protocol, compiled.Settings.Decryption, compiled.Settings.Version = "hysteria", "", 2
		}
		result = append(result, compiled)
	}
	return result, nil
}
