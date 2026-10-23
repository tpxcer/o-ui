package localapi

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

type ouiSample struct {
	id                                                int64
	remark, protocol, settings, stream, sniff, listen string
	port                                              int
	up, down                                          int64
	email                                             string
}

// ouiDatabase按美国VPS上OUI 2026.10.18-1的表结构造一个x-ui.db（只建导入用到的两张表）。
func ouiDatabase(t *testing.T, samples []ouiSample) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE `inbounds` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer,`up` integer,`down` integer,`total` integer,`remark` text,`enable` numeric,`expiry_time` integer,`traffic_reset` text DEFAULT \"never\",`last_traffic_reset_time` integer DEFAULT 0,`device_limit` integer DEFAULT 0,`listen` text,`port` integer,`protocol` text,`settings` text,`stream_settings` text,`tag` text,`sniffing` text,`tg_online_notify` numeric DEFAULT false,`node_id` integer)",
		"CREATE TABLE `client_traffics` (`id` integer PRIMARY KEY AUTOINCREMENT,`inbound_id` integer,`enable` numeric,`email` text,`up` integer,`down` integer,`expiry_time` integer,`total` integer,`reset` integer DEFAULT 0,`last_online` integer DEFAULT 0)",
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range samples {
		if _, err = db.Exec("INSERT INTO inbounds (id, up, down, total, remark, enable, expiry_time, listen, port, protocol, settings, stream_settings, tag, sniffing) VALUES (?, 0, 0, 0, ?, 1, 0, ?, ?, ?, ?, ?, ?, ?)", s.id, s.remark, s.listen, s.port, s.protocol, s.settings, s.stream, "inbound-"+s.remark, s.sniff); err != nil {
			t.Fatal(err)
		}
		if s.email != "" {
			if _, err = db.Exec("INSERT INTO client_traffics (inbound_id, enable, email, up, down, expiry_time, total) VALUES (?, 1, ?, ?, ?, 0, 0)", s.id, s.email, s.up, s.down); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func ouiKey(t *testing.T) string {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.Bytes())
}

func TestImportOUIBackup(t *testing.T) {
	ctx := context.Background()
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	session, err := s.Initialize(ctx, "tester", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	call := func(action string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/backup/"+action, strings.NewReader(string(data)))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	cert, key := runtimeCertificate(t, t.TempDir(), "h.example.test")
	keyHp, keyXl, keyLz := ouiKey(t), ouiKey(t), ouiKey(t)
	sniff := `{"enabled":true,"destOverride":["http","tls","quic"],"metadataOnly":false,"routeOnly":true}`
	reality := func(target, key, minVer string, shortIDs string) string {
		return `"security":"reality","realitySettings":{"show":false,"xver":0,"target":"` + target + `:443","serverNames":["` + target + `","www.` + target + `"],"privateKey":"` + key + `","minClientVer":"` + minVer + `","maxClientVer":"","maxTimediff":0,"shortIds":[` + shortIDs + `],"mldsa65Seed":"","settings":{"publicKey":"","fingerprint":"chrome","serverName":"","spiderX":"/"}}`
	}
	eight := `"cfe1a2b3c4","ab","0123456789abcd","a1b2c3d4e5f6","abcd","0011223344556677","abcdef","01234567"`
	samples := []ouiSample{
		{id: 1, remark: "noflow", protocol: "vless", port: 41001, email: "noflow-user", up: 100, down: 200,
			settings: `{"clients":[{"id":"6F9619FF-8B86-4D11-B42D-00C04FC964FF","flow":"","email":"noflow-user","limitIp":0,"totalGB":0,"expiryTime":0,"enable":true,"tgId":0,"subId":"x","comment":"","reset":0}],"decryption":"none","fallbacks":[]}`,
			stream:   `{"network":"tcp",` + reality("tesla.com", keyHp, "0.0.0", eight) + `,"tcpSettings":{"acceptProxyProtocol":false,"header":{"type":"none"}}}`, sniff: sniff},
		{id: 3, remark: "vision-user", protocol: "vless", port: 41003, email: "vision-user", up: 5, down: 6,
			settings: `{"clients":[{"id":"7c9e6679-7425-40de-944b-e07fc1f90ae7","flow":"xtls-rprx-vision","email":"vision-user","limitIp":2,"totalGB":107374182400,"expiryTime":2000000000000,"enable":true}],"decryption":"none"}`,
			stream:   `{"network":"tcp",` + reality("meta.com", keyXl, "26.5.0", `"0123456789abcdef"`) + `,"tcpSettings":{"header":{"type":"none"}}}`, sniff: sniff},
		{id: 39, remark: "xhttp", protocol: "vless", port: 41039, email: "xhttp-user", up: 7, down: 8,
			settings: `{"clients":[{"id":"9b2f7f2e-0c5e-4f43-8a0e-2a4f8c1d3e5b","flow":"","email":"xhttp-user","limitIp":1,"totalGB":0,"expiryTime":1990000000000,"enable":true}],"decryption":"none"}`,
			stream:   `{"network":"xhttp",` + reality("lovelive-anime.jp", keyLz, "1.0.0", `"a1b2c3d4"`) + `,"xhttpSettings":{"path":"/oui-xh-a1","host":"lovelive-anime.jp","mode":"auto","headers":{},"xPaddingBytes":"100-1000","xPaddingObfsMode":false,"xPaddingKey":"","scMaxEachPostBytes":"1000000","scMinPostsIntervalMs":"30","scMaxBufferedPosts":30,"scStreamUpServerSecs":"20-80","serverMaxHeaderBytes":0,"noSSEHeader":false,"noGRPCHeader":false,"uplinkHTTPMethod":"","enableXmux":false}}`, sniff: sniff},
		{id: 37, remark: "HY2", protocol: "hysteria", port: 41037, email: "hy2-user", up: 9, down: 10,
			settings: `{"clients":[{"auth":"Ab3dE6gH9jK2mN5pQ8sT1vW4yZ7bC0eF","email":"hy2-user","limitIp":0,"totalGB":0,"expiryTime":0,"enable":true}],"version":2}`,
			stream:   `{"network":"hysteria","security":"tls","tlsSettings":{"serverName":"h.example.test","minVersion":"1.3","alpn":["h3"],"certificates":[{"certificateFile":"` + cert + `","keyFile":"` + key + `"}]},"hysteriaSettings":{"version":2}}`, sniff: sniff},
		{id: 50, remark: "vm", protocol: "vmess", port: 40000, settings: `{"clients":[]}`, stream: `{"network":"ws","security":"none"}`},
		{id: 51, remark: "obfs", protocol: "vless", port: 40001,
			settings: `{"clients":[{"id":"1b4e28ba-2fa1-41d2-883f-0016d3cca427","email":"obfs"}],"decryption":"none"}`,
			stream:   `{"network":"xhttp",` + reality("meta.com", ouiKey(t), "", `"ab"`) + `,"xhttpSettings":{"path":"/x","mode":"auto","xPaddingObfsMode":true}}`},
		{id: 52, remark: "low", protocol: "vless", port: 443, settings: `{"clients":[]}`, stream: `{"network":"tcp","security":"reality"}`},
	}
	archive := base64.StdEncoding.EncodeToString(ouiDatabase(t, samples))
	settingsBefore, _ := s.ReadDocument(ctx, "xray-settings")
	if w := call("preview", map[string]string{"archive": archive}); w.Code != 422 || !strings.Contains(w.Body.String(), "分享主机") {
		t.Fatal("OUI import must require a share host", w.Code, w.Body.String())
	}
	w := call("preview", map[string]string{"archive": archive, "shareHost": "203.0.113.7"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var preview backupPreview
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	notes := strings.Join(preview.Notes, "\n")
	if preview.Source != "oui" || preview.Inbounds != 4 || preview.Clients != 4 {
		t.Fatal("preview counts", preview.Source, preview.Inbounds, preview.Clients, notes)
	}
	for _, want := range []string{"未导入入站vm (vmess 40000)", "未导入入站obfs (vless 40001)：XHTTP参数xPaddingObfsMode", "未导入入站low (vless 443)", "XHTTP Host（lovelive-anime.jp）不保留", "最低客户端版本26.5.0改为官方默认"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("missing note %q in:\n%s", want, notes)
		}
	}
	if other := call("preview", map[string]string{"archive": archive, "shareHost": "203.0.113.8"}); !strings.Contains(other.Body.String(), `"fingerprint"`) || strings.Contains(other.Body.String(), preview.Fingerprint) {
		t.Fatal("share host must be part of the fingerprint")
	}
	w = call("restore", map[string]string{"archive": archive, "shareHost": "203.0.113.7", "fingerprint": preview.Fingerprint, "confirmation": "确认覆盖并恢复"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	docs, _ := s.ReadDocuments(ctx, backupDocuments)
	if string(docs["xray-settings"].Data) != string(settingsBefore.Data) {
		t.Fatal("OUI import must keep the current Xray settings")
	}
	var draft workspaceDraft
	var ids identities
	_ = json.Unmarshal(docs["workspace"].Data, &draft)
	_ = json.Unmarshal(docs["proxy-identities"].Data, &ids)
	ledger, _ := backupLedger(docs["traffic"].Data)
	byID := map[string]inboundDraft{}
	for _, in := range draft.Inbounds {
		byID[in.ID] = in
		if in.Listen != "0.0.0.0" || in.ShareHost != "203.0.113.7" || !in.Sniffing.Enabled || !in.Sniffing.RouteOnly || !in.Sniffing.QUIC {
			t.Fatal("endpoint/sniffing", in)
		}
	}
	hp, xl, lz, hy := byID["oui-1"], byID["oui-3"], byID["oui-39"], byID["oui-37"]
	if hp.Preset != "reality" || !hp.Connection.NoVision || !hp.Connection.MihomoCompatibility || hp.Connection.ServerName != "tesla.com" || hp.Connection.Target != "tesla.com:443" {
		t.Fatal("noflow", hp.Connection)
	}
	if xl.Preset != "reality" || xl.Connection.NoVision || xl.Connection.MihomoCompatibility || lz.Preset != "xhttp-reality" || lz.Connection.Path != "/oui-xh-a1" || lz.Connection.Mode != "auto" || !lz.Connection.MihomoCompatibility {
		t.Fatal("xl/LLz", xl.Connection, lz.Connection)
	}
	if hy.Preset != "hy2" || hy.Connection.CertificateFile != cert || hy.Connection.KeyFile != key || hy.Connection.ServerName != "h.example.test" {
		t.Fatal("HY2", hy.Connection)
	}
	clients := map[string]clientDraft{}
	for _, c := range draft.Clients {
		clients[c.ID] = c
	}
	if c := clients["oui-3-1"]; c.Name != "vision-user" || c.IPLimit != 2 || quotaBytes(c.QuotaGB) < 107374182400 || c.ExpiresAt == nil || *c.ExpiresAt != 2000000000000 || !c.Enabled {
		t.Fatal("client xl", c)
	}
	if c := clients["oui-39-1"]; !c.Enabled || c.IPLimit != 1 || c.ExpiresAt == nil || *c.ExpiresAt != 1990000000000 {
		t.Fatal("client lzz", c)
	}
	if ids.Clients["oui-1-1"].UUID != "6f9619ff-8b86-4d11-b42d-00c04fc964ff" || ids.Clients["oui-37-1"].Auth != "Ab3dE6gH9jK2mN5pQ8sT1vW4yZ7bC0eF" || ids.Clients["oui-37-1"].UUID == "" || ids.Clients["oui-1-1"].Auth == "" {
		t.Fatal("client identities")
	}
	if r := ids.Inbounds["oui-1"]; r.PrivateKey != keyHp || r.ShortID != "cfe1a2b3c4" || len(r.ExtraShortIDs) != 7 || strings.Join(r.ExtraServerNames, ",") != "www.tesla.com" || r.MinClientVer != "0.0.0" {
		t.Fatal("Hp identity", r)
	}
	if ledger.Totals["oui-1-1"].Uplink != 100 || ledger.Totals["oui-1-1"].Downlink != 200 || ledger.Totals["oui-37-1"].Total() != 19 {
		t.Fatal("traffic", ledger.Totals)
	}
	// 编译后的服务端：Hp不加流控、8个shortId与2个SNI、最低版本0.0.0；xl保留Vision；LLz为1.0.0。
	compiled, err := buildInbounds(draft, ids, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]config.CompiledInbound{}
	for _, in := range compiled {
		tags[in.Tag] = in
	}
	hpIn, xlIn, lzIn := tags["in-oui-1"], tags["in-oui-3"], tags["in-oui-39"]
	if hpIn.Settings.Clients[0].Flow != "" || len(hpIn.Stream.Reality.ShortIDs) != 8 || len(hpIn.Stream.Reality.ServerNames) != 2 || hpIn.Stream.Reality.MinClientVer != "0.0.0" {
		t.Fatal("compiled Hp", hpIn.Settings.Clients, hpIn.Stream.Reality)
	}
	if xlIn.Settings.Clients[0].Flow != "xtls-rprx-vision" || xlIn.Stream.Reality.MinClientVer != "" || lzIn.Stream.Reality.MinClientVer != "1.0.0" {
		t.Fatal("compiled xl/LLz")
	}
	_, link, err := config.ClientConnection(hpIn, hpIn.Settings.Clients[0], "tesla.com", "noflow", "203.0.113.7")
	if err != nil || !strings.Contains(link, "sid=cfe1a2b3c4") || strings.Contains(link, "flow=") {
		t.Fatal("Hp link", link, err)
	}
	// 关闭Vision流控只用于TCP的VLESS Reality。
	lz.Connection.NoVision = true
	bad := workspaceDraft{Inbounds: []inboundDraft{lz}, Clients: []clientDraft{}}
	data, _ := json.Marshal(bad)
	if validateWorkspaceDraft(data, nil) == nil {
		t.Fatal("noVision accepted on XHTTP")
	}
	// 导入后照常可导出备份（证书文件可读时）。
	if w = call("export", map[string]string{"password": ""}); w.Code != 200 {
		t.Fatal("export after import", w.Code, w.Body.String())
	}
}
