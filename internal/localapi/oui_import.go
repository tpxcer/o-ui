package localapi

import (
	"bytes"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/i18n"
	"independent-panel/internal/traffic"
)

// OUI（及3x-ui系）面板的备份就是整个x-ui.db（用户2026-10-23要求能在恢复里直接导入并保留现有链接）。
// 只读取其中的入站、客户端和累计流量，换算成业务备份后走同一套预览、确认、回退副本和恢复流程；
// 面板设置、管理员、Telegram与路由不导入。无法原样换算的入站整条跳过，原因在预览中列出。
var sqliteHeader = []byte("SQLite format 3\x00")

func isOUIBackup(file []byte) bool { return bytes.HasPrefix(file, sqliteHeader) }

type ouiRow struct {
	ID                                                   int64
	Remark, Listen, Protocol, Settings, Stream, Sniffing string
	Port                                                 int
	Enabled                                              bool
	Total, Expiry                                        int64
}

type ouiClient struct {
	ID         string `json:"id"`
	Auth       string `json:"auth"`
	Password   string `json:"password"`
	Email      string `json:"email"`
	Flow       string `json:"flow"`
	Enable     *bool  `json:"enable"`
	ExpiryTime int64  `json:"expiryTime"`
	LimitIP    int    `json:"limitIp"`
	TotalGB    int64  `json:"totalGB"`
}

type ouiStream struct {
	Network  string `json:"network"`
	Security string `json:"security"`
	Reality  struct {
		Target       string   `json:"target"`
		Dest         string   `json:"dest"`
		ServerNames  []string `json:"serverNames"`
		PrivateKey   string   `json:"privateKey"`
		ShortIDs     []string `json:"shortIds"`
		MinClientVer string   `json:"minClientVer"`
		MaxClientVer string   `json:"maxClientVer"`
		Mldsa65Seed  string   `json:"mldsa65Seed"`
	} `json:"realitySettings"`
	TLS struct {
		ServerName   string `json:"serverName"`
		Certificates []struct {
			CertificateFile string          `json:"certificateFile"`
			KeyFile         string          `json:"keyFile"`
			Certificate     json.RawMessage `json:"certificate"`
			Key             json.RawMessage `json:"key"`
		} `json:"certificates"`
	} `json:"tlsSettings"`
	XHTTP map[string]any `json:"xhttpSettings"`
	TCP   struct {
		Header struct {
			Type string `json:"type"`
		} `json:"header"`
	} `json:"tcpSettings"`
	Finalmask json.RawMessage `json:"finalmask"`
}

type ouiResult struct {
	inbound    inboundDraft
	clients    []clientDraft
	emails     map[string]string
	identities map[string]proxyIdentity
	reality    *realityIdentity
	cert       *backupCertificate
	notes      []string
}

// 这些XHTTP参数取Xray默认值时与o-ui一致；客户端侧或仅影响服务端宽松度的参数忽略，其余非默认值无法保留。
var ouiXHTTPDefaults = map[string]string{"xPaddingBytes": "100-1000", "scMaxEachPostBytes": "1000000", "scMaxBufferedPosts": "30", "scStreamUpServerSecs": "20-80"}
var ouiXHTTPIgnored = map[string]bool{"path": true, "mode": true, "host": true, "scMinPostsIntervalMs": true, "noSSEHeader": true, "enableXmux": true, "xmux": true}

// readOUI把上传的数据库写进运行目录下的私有临时文件（0600），只读打开，读完即删。
func readOUI(file []byte, dir string) ([]ouiRow, map[string]traffic.Count, error) {
	f, err := os.CreateTemp(dir, "oui-import-*.db")
	if err != nil {
		return nil, nil, err
	}
	path := f.Name()
	defer os.Remove(path)
	_, err = f.Write(file)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, nil, err
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	unreadable := i18n.New("无法读取OUI备份里的入站，请确认上传的是OUI/3x-ui面板导出的数据库（x-ui.db）", "Cannot read inbounds from the OUI backup; make sure you uploaded the database (x-ui.db) exported by an OUI/3x-ui panel")
	rows, err := db.Query("SELECT id, COALESCE(remark,''), COALESCE(listen,''), COALESCE(protocol,''), COALESCE(settings,''), COALESCE(stream_settings,''), COALESCE(sniffing,''), COALESCE(port,0), COALESCE(enable,0) != 0, COALESCE(total,0), COALESCE(expiry_time,0) FROM inbounds ORDER BY id")
	if err != nil {
		return nil, nil, unreadable
	}
	defer rows.Close()
	result := []ouiRow{}
	for rows.Next() {
		var row ouiRow
		if rows.Scan(&row.ID, &row.Remark, &row.Listen, &row.Protocol, &row.Settings, &row.Stream, &row.Sniffing, &row.Port, &row.Enabled, &row.Total, &row.Expiry) != nil {
			return nil, nil, unreadable
		}
		result = append(result, row)
	}
	if rows.Err() != nil {
		return nil, nil, unreadable
	}
	used := map[string]traffic.Count{}
	counts, err := db.Query("SELECT COALESCE(email,''), COALESCE(up,0), COALESCE(down,0) FROM client_traffics")
	if err != nil {
		return nil, nil, i18n.New("无法读取OUI备份里的客户端流量", "Cannot read client traffic from the OUI backup")
	}
	defer counts.Close()
	for counts.Next() {
		var email string
		var up, down int64
		if counts.Scan(&email, &up, &down) != nil {
			return nil, nil, i18n.New("无法读取OUI备份里的客户端流量", "Cannot read client traffic from the OUI backup")
		}
		used[email] = traffic.Count{Uplink: uint64(max(up, 0)), Downlink: uint64(max(down, 0))}
	}
	return result, used, counts.Err()
}

func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// pemText接受Xray的两种写法：按行拆开的字符串数组或整段字符串。
func pemText(raw json.RawMessage) []byte {
	var lines []string
	if json.Unmarshal(raw, &lines) == nil {
		return []byte(strings.Join(lines, "\n"))
	}
	var text string
	_ = json.Unmarshal(raw, &text)
	return []byte(text)
}

func versionLess(a, b string) bool {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := range x {
		m, _ := strconv.Atoi(x[i])
		n, _ := strconv.Atoi(y[i])
		if m != n {
			return m < n
		}
	}
	return false
}

func emptyValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	case json.Number:
		return v.String() == "0"
	case map[string]any:
		return len(v) == 0
	case []any:
		return len(v) == 0
	}
	return false
}

// convertOUI换算一条入站；返回的error是整条跳过的原因，notes是保留连接但有改动的说明。
func convertOUI(row ouiRow, shareHost string, now time.Time) (ouiResult, error) {
	r := ouiResult{emails: map[string]string{}, identities: map[string]proxyIdentity{}}
	if row.Protocol != "vless" && row.Protocol != "hysteria" {
		return r, i18n.Errorf("协议%s暂不支持", "protocol %s is not supported", row.Protocol)
	}
	if row.Port < 1024 || row.Port > 65535 {
		return r, i18n.Errorf("端口%d不在o-ui允许的1024至65535范围内", "port %d is outside o-ui's 1024–65535 range", row.Port)
	}
	var settings struct {
		Clients    []ouiClient       `json:"clients"`
		Decryption string            `json:"decryption"`
		Fallbacks  []json.RawMessage `json:"fallbacks"`
	}
	var stream ouiStream
	decoder := json.NewDecoder(strings.NewReader(row.Stream))
	decoder.UseNumber()
	if json.Unmarshal([]byte(row.Settings), &settings) != nil || decoder.Decode(&stream) != nil {
		return r, i18n.New("入站设置无法解析", "inbound settings cannot be parsed")
	}
	listen := row.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	if _, _, err := config.ProxyEndpoint(listen, shareHost); err != nil {
		return r, err
	}
	in := inboundDraft{ID: "oui-" + strconv.FormatInt(row.ID, 10), Name: firstRunes(strings.TrimSpace(row.Remark), 128), Port: row.Port, Listen: listen, ShareHost: shareHost, Enabled: row.Enabled, Connection: &connectionDraft{}}
	if in.Name == "" {
		in.Name = fmt.Sprintf("%s-%d", row.Protocol, row.Port)
	}
	c := in.Connection
	flows := map[string]bool{}
	for _, client := range settings.Clients {
		flows[client.Flow] = true
	}
	switch {
	case row.Protocol == "hysteria" && stream.Security == "tls":
		in.Preset = "hy2"
		if mask := strings.TrimSpace(string(stream.Finalmask)); mask != "" && mask != "null" && mask != "{}" {
			return r, i18n.New("HY2的finalmask（端口跳跃/混淆）暂不支持导入，请在o-ui里重新设置", "HY2 finalmask (port hopping/obfuscation) cannot be imported; set it up again in o-ui")
		}
	case row.Protocol == "vless" && stream.Network == "tcp" && stream.Security == "reality":
		in.Preset = "reality"
		if kind := stream.TCP.Header.Type; kind != "" && kind != "none" {
			return r, i18n.Errorf("TCP伪装头%s暂不支持", "TCP header %s is not supported", kind)
		}
		if len(flows) > 1 || (len(flows) == 1 && !flows[""] && !flows["xtls-rprx-vision"]) {
			return r, i18n.New("客户端流控不一致或不是xtls-rprx-vision", "client flows differ or are not xtls-rprx-vision")
		}
		c.NoVision = flows[""]
	case row.Protocol == "vless" && stream.Network == "xhttp" && (stream.Security == "reality" || stream.Security == "tls"):
		in.Preset = "xhttp-" + stream.Security
		if len(flows) > 1 || (len(flows) == 1 && !flows[""]) {
			return r, i18n.New("XHTTP客户端设置了流控", "XHTTP clients have a flow set")
		}
		x := stream.XHTTP
		c.Path, c.Mode = "/", "auto"
		if path, _ := x["path"].(string); path != "" {
			c.Path = path
		}
		if mode, _ := x["mode"].(string); mode != "" {
			c.Mode = mode
		}
		if !strings.HasPrefix(c.Path, "/") || strings.ContainsAny(c.Path, "?#") || (c.Mode != "auto" && c.Mode != "packet-up" && c.Mode != "stream-up" && c.Mode != "stream-one") {
			return r, i18n.New("XHTTP路径或模式无效", "invalid XHTTP path or mode")
		}
		keys := []string{}
		for key, value := range x {
			if !ouiXHTTPIgnored[key] && !emptyValue(value) && ouiXHTTPDefaults[key] != fmt.Sprint(value) {
				keys = append(keys, key)
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			return r, i18n.Errorf("XHTTP参数%s不是默认值，o-ui无法保留", "XHTTP parameters %s are not defaults and cannot be kept", strings.Join(keys, ", "))
		}
		if host, _ := x["host"].(string); host != "" {
			r.notes = append(r.notes, i18n.Errorf("XHTTP Host（%s）不保留：服务端不再校验Host，旧客户端照常可连", "XHTTP Host (%s) is not kept: the server no longer checks Host, so existing clients still connect", host).Error())
		}
	default:
		return r, i18n.Errorf("%s + %s + %s暂不支持", "%s + %s + %s is not supported", row.Protocol, stream.Network, stream.Security)
	}
	if row.Protocol == "vless" && settings.Decryption != "" && settings.Decryption != "none" {
		return r, i18n.New("VLESS Encryption暂不支持", "VLESS Encryption is not supported")
	}
	if stream.Security == "reality" {
		rs := stream.Reality
		c.Target = rs.Target
		if c.Target == "" {
			c.Target = rs.Dest
		}
		names := []string{}
		for _, name := range rs.ServerNames {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
		key, err := base64.RawURLEncoding.DecodeString(rs.PrivateKey)
		if err == nil {
			_, err = ecdh.X25519().NewPrivateKey(key)
		}
		if err != nil || len(names) == 0 || len(rs.ShortIDs) == 0 {
			return r, i18n.New("REALITY私钥、SNI或shortId缺失/无效", "REALITY private key, SNI or shortId is missing or invalid")
		}
		for _, id := range rs.ShortIDs {
			if !config.ValidShortID(id) {
				return r, i18n.New("含o-ui无法保留的shortId（如空值）", "contains a shortId o-ui cannot keep (such as an empty one)")
			}
		}
		if rs.Mldsa65Seed != "" {
			return r, i18n.New("REALITY ML-DSA-65签名暂不支持", "REALITY ML-DSA-65 signing is not supported")
		}
		c.ServerName = names[0]
		r.reality = &realityIdentity{PrivateKey: rs.PrivateKey, ShortID: rs.ShortIDs[0], ExtraShortIDs: rs.ShortIDs[1:], ExtraServerNames: names[1:]}
		// 官方26.7.28未填写时默认最低26.3.27；OUI显式填写更低的值时用Mihomo兼容模式原样保留，例如0.0.0也放行报1.8.1的上游sing-box。
		if v := rs.MinClientVer; v != "" {
			if !config.ValidClientVersion(v) {
				return r, i18n.New("REALITY最低客户端版本无效", "invalid REALITY minimum client version")
			}
			if versionLess(v, "26.3.27") {
				c.MihomoCompatibility = true
				if v != "1.8.2" {
					r.reality.MinClientVer = v
				}
			} else if v != "26.3.27" {
				r.notes = append(r.notes, i18n.Errorf("REALITY最低客户端版本%s改为官方默认26.3.27", "REALITY minimum client version %s becomes the official default 26.3.27", v).Error())
			}
		}
		if rs.MaxClientVer != "" {
			r.notes = append(r.notes, i18n.Errorf("REALITY最高客户端版本%s不保留", "REALITY maximum client version %s is not kept", rs.MaxClientVer).Error())
		}
	} else {
		c.ServerName = stream.TLS.ServerName
		if len(stream.TLS.Certificates) == 0 {
			return r, i18n.New("缺少TLS证书", "TLS certificate is missing")
		}
		cert := stream.TLS.Certificates[0]
		if cert.CertificateFile != "" || cert.KeyFile != "" {
			c.CertificateFile, c.KeyFile = cert.CertificateFile, cert.KeyFile
			_, certErr := readPEM(cert.CertificateFile)
			_, keyErr := readPEM(cert.KeyFile)
			if certErr != nil || keyErr != nil {
				r.notes = append(r.notes, i18n.Errorf("o-ui现在读不到证书文件%s或%s（不存在或没有权限），应用前请改用面板证书或调整权限", "o-ui cannot read the certificate file %s or %s right now (missing or no permission); switch to the panel certificate or fix the permissions before applying", cert.CertificateFile, cert.KeyFile).Error())
			}
		} else {
			r.cert = &backupCertificate{Certificate: pemText(cert.Certificate), Key: pemText(cert.Key)}
		}
	}
	var sniff struct {
		Enabled         bool     `json:"enabled"`
		DestOverride    []string `json:"destOverride"`
		RouteOnly       bool     `json:"routeOnly"`
		MetadataOnly    bool     `json:"metadataOnly"`
		IPsExcluded     []string `json:"ipsExcluded"`
		DomainsExcluded []string `json:"domainsExcluded"`
	}
	if row.Sniffing != "" && json.Unmarshal([]byte(row.Sniffing), &sniff) != nil {
		return r, i18n.New("嗅探设置无法解析", "sniffing settings cannot be parsed")
	}
	s := &in.Sniffing
	s.Enabled, s.RouteOnly, s.MetadataOnly = sniff.Enabled, sniff.RouteOnly, sniff.MetadataOnly
	for _, name := range sniff.DestOverride {
		s.HTTP, s.TLS, s.QUIC, s.FakeDNS = s.HTTP || name == "http", s.TLS || name == "tls", s.QUIC || name == "quic", s.FakeDNS || name == "fakedns"
	}
	if config.ValidateSniffing(config.Sniffing{IPsExcluded: sniff.IPsExcluded, DomainsExcluded: sniff.DomainsExcluded}) == nil {
		s.IPsExcluded, s.DomainsExcluded = sniff.IPsExcluded, sniff.DomainsExcluded
	} else {
		r.notes = append(r.notes, i18n.New("嗅探排除规则含o-ui不支持的写法，未导入", "sniffing exclusions use forms o-ui does not support and were not imported").Error())
	}
	if len(settings.Fallbacks) > 0 {
		r.notes = append(r.notes, i18n.New("回落（fallbacks）不保留", "fallbacks are not kept").Error())
	}
	if row.Total > 0 || row.Expiry > 0 {
		r.notes = append(r.notes, i18n.New("入站级别的流量上限/到期不保留（客户端的照常导入）", "inbound-level traffic quota/expiry is not kept (client-level ones are imported)").Error())
	}
	for index, client := range settings.Clients {
		id := fmt.Sprintf("%s-%d", in.ID, index+1)
		identity := proxyIdentity{}
		if in.Preset == "hy2" {
			identity.Auth = client.Auth
			if identity.Auth == "" {
				identity.Auth = client.Password
			}
			if !config.ValidHY2Auth(identity.Auth) {
				return r, i18n.Errorf("客户端%s的HY2认证值o-ui无法保留", "client %s has an HY2 auth value o-ui cannot keep", client.Email)
			}
		} else {
			if !config.ValidUUID(client.ID) {
				return r, i18n.Errorf("客户端%s的UUID无效", "client %s has an invalid UUID", client.Email)
			}
			identity.UUID = strings.ToLower(client.ID)
		}
		draft := clientDraft{ID: id, InboundID: in.ID, Name: strings.TrimSpace(client.Email), Enabled: client.Enable == nil || *client.Enable, IPLimit: min(max(client.LimitIP, 0), 10000)}
		if client.TotalGB > 0 {
			draft.QuotaGB = float64(client.TotalGB) / 1e9
		}
		if client.ExpiryTime != 0 {
			expires := client.ExpiryTime
			if expires < 0 {
				// OUI负值表示首次使用后计时的时长，这里改为从导入时起算。
				expires = now.UnixMilli() - expires
				r.notes = append(r.notes, i18n.Errorf("客户端%s的“首次使用后计时”到期改为从导入时起算", "client %s: the \"start on first use\" expiry now counts from the import", client.Email).Error())
			}
			draft.ExpiresAt = &expires
		}
		r.clients = append(r.clients, draft)
		r.emails[id] = client.Email
		r.identities[id] = identity
	}
	r.inbound = in
	return r, nil
}

// importOUI把OUI数据库换算成业务备份：入站/客户端ID按OUI编号固定（重复导入时预览显示为覆盖），
// 缺少的另一半身份（VLESS的认证值、HY2的UUID）按本面板规则随机补齐；Xray设置由恢复时保留当前值。
func importOUI(file []byte, shareHost, dir string, now time.Time) (businessBackup, error) {
	b := businessBackup{Format: "independent-xray-panel", Version: 1, CreatedAt: now.UTC(), Workspace: workspaceDraft{Inbounds: []inboundDraft{}, Clients: []clientDraft{}}, Settings: json.RawMessage("null"), Traffic: traffic.Empty(), Certificates: map[string]backupCertificate{}, source: "oui", notes: []string{}}
	b.Identities = identities{Clients: map[string]proxyIdentity{}, Inbounds: map[string]realityIdentity{}}
	if !config.ValidShareHost(shareHost) {
		return b, i18n.New("请填写有效的分享主机（本机公网IP或直接解析到本机的域名）", "Enter a valid share host (this server's public IP or a domain that resolves directly to it)")
	}
	rows, used, err := readOUI(file, dir)
	if err != nil {
		return b, err
	}
	names := map[string]bool{}
	for _, row := range rows {
		label := fmt.Sprintf("%s (%s %d)", row.Remark, row.Protocol, row.Port)
		r, err := convertOUI(row, shareHost, now)
		if err != nil {
			b.notes = append(b.notes, i18n.Wrap(fmt.Sprintf("未导入入站%s：", label), fmt.Sprintf("Inbound %s not imported: ", label), err).Error())
			continue
		}
		for _, note := range r.notes {
			b.notes = append(b.notes, i18n.T(fmt.Sprintf("入站%s：%s", label, note), fmt.Sprintf("Inbound %s: %s", label, i18n.Translate(note, "en"))))
		}
		b.Workspace.Inbounds = append(b.Workspace.Inbounds, r.inbound)
		if r.reality != nil {
			b.Identities.Inbounds[r.inbound.ID] = *r.reality
		}
		if r.cert != nil {
			b.Certificates[r.inbound.ID] = *r.cert
		}
		for _, client := range r.clients {
			base := firstRunes(client.Name, 60)
			if base == "" {
				base = client.ID
			}
			client.Name = base
			for n := 2; names[client.Name]; n++ {
				client.Name = fmt.Sprintf("%s-%d", base, n)
			}
			names[client.Name] = true
			b.Workspace.Clients = append(b.Workspace.Clients, client)
			b.Identities.Clients[client.ID] = r.identities[client.ID]
			if count, ok := used[r.emails[client.ID]]; ok {
				b.Traffic.Totals[client.ID] = count
			}
		}
	}
	if len(b.Workspace.Inbounds) == 0 {
		return b, i18n.Errorf("OUI备份里没有可导入的入站（共%d条，均不支持）", "The OUI backup has no importable inbounds (%d in total, none supported)", len(rows))
	}
	generated := identities{Clients: map[string]proxyIdentity{}, Inbounds: map[string]realityIdentity{}}
	if _, err = completeIdentities(&generated, workspaceDraft{Clients: b.Workspace.Clients}); err != nil {
		return b, err
	}
	for id, extra := range generated.Clients {
		identity := b.Identities.Clients[id]
		if identity.UUID == "" {
			identity.UUID = extra.UUID
		}
		if identity.Auth == "" {
			identity.Auth = extra.Auth
		}
		b.Identities.Clients[id] = identity
	}
	return b, validateBackup(b)
}
