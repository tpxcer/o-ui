package localapi

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"independent-panel/internal/core"
)

const warpFixturePeer = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="

// 只用本地模拟的Cloudflare接口，不注册真实WARP设备。
func TestWarpRegistrationFlow(t *testing.T) {
	var sentKey atomic.Value
	var fail atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"errors":[{"message":"Invalid license\u0007"}]}`)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/reg":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Header.Get("CF-Client-Version") == "" || r.Header.Get("Authorization") != "" || body["model"] != "o-ui" || body["name"] != nil {
				t.Error("unexpected registration request", r.Header, body)
			}
			sentKey.Store(body["key"])
			_, _ = io.WriteString(w, `{"id":"device-1","token":"secret-token","account":{"account_type":"free","license":"aaaaaaaa-bbbbbbbb-cccc1234"},"config":{"client_id":"AQID","peers":[{"public_key":"`+warpFixturePeer+`","endpoint":{"host":"engage.cloudflareclient.com:2408"}}],"interface":{"addresses":{"v4":"172.16.0.2","v6":"2606:4700:110:8a36::1"}}}}`)
		case r.Method == "PUT" && r.URL.Path == "/reg/device-1/account":
			if r.Header.Get("Authorization") != "Bearer secret-token" {
				t.Error("license update without device token")
			}
			_, _ = io.WriteString(w, `{"account_type":"unlimited","premium_data":1000,"quota":1000,"usage":10,"warp_plus":true}`)
		case r.Method == "GET" && r.URL.Path == "/reg/device-1":
			_, _ = io.WriteString(w, `{"id":"device-1","account":{"account_type":"unlimited","premium_data":2000,"quota":2000,"usage":20}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	previous := warpAPIBase
	warpAPIBase = api.URL
	defer func() { warpAPIBase = previous }()

	_, _, handler, cookie := subscriptionFixture(t)
	call := func(method, path, body string, want int) warpView {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret-token") || strings.Contains(w.Body.String(), "aaaaaaaa") || strings.ContainsRune(w.Body.String(), 7) {
			t.Fatal("device token, full license or control characters leaked", w.Body.String())
		}
		var view warpView
		_ = json.Unmarshal(w.Body.Bytes(), &view)
		return view
	}
	if call("GET", "/api/xray/warp", "", 200).Registered {
		t.Fatal("registered before creating an account")
	}
	call("POST", "/api/xray/warp/register", `{}`, 409)
	view := call("POST", "/api/xray/warp/register", `{"confirm":true}`, 200)
	settings := view.Outbound["settings"].(map[string]any)
	private, _ := base64.StdEncoding.DecodeString(settings["secretKey"].(string))
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil || base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()) != sentKey.Load() {
		t.Fatal("outbound private key does not match the registered public key")
	}
	data, _ := json.Marshal(view.Outbound)
	for _, part := range []string{`"tag":"warp"`, `"protocol":"wireguard"`, `"noKernelTun":true`, `"reserved":[1,2,3]`, `"address":["172.16.0.2/32","2606:4700:110:8a36::1/128"]`, `"endpoint":"engage.cloudflareclient.com:2408"`, `"publicKey":"` + warpFixturePeer + `"`} {
		if !strings.Contains(string(data), part) {
			t.Fatalf("missing %s in %s", part, data)
		}
	}
	if !view.Registered || view.AccountType != "free" || view.LicenseSuffix != "1234" {
		t.Fatal("registration view", view)
	}
	call("POST", "/api/xray/warp/register", `{"confirm":true}`, 409)
	call("POST", "/api/xray/warp/license", `{"license":"not-a-license"}`, 400)
	if view = call("POST", "/api/xray/warp/license", `{"license":"dddddddd-eeeeeeee-ffff5678"}`, 200); view.AccountType != "unlimited" || view.LicenseSuffix != "5678" || view.PremiumData != 1000 {
		t.Fatal("license view", view)
	}
	if view = call("GET", "/api/xray/warp?refresh=1", "", 200); view.PremiumData != 2000 || view.Usage != 20 {
		t.Fatal("refresh view", view)
	}
	fail.Store(true)
	call("GET", "/api/xray/warp?refresh=1", "", 502)
	fail.Store(false)
	if call("DELETE", "/api/xray/warp", "", 200).Registered || call("GET", "/api/xray/warp", "", 200).Registered {
		t.Fatal("WARP record not removed")
	}
	if !call("POST", "/api/xray/warp/register", `{"confirm":true}`, 200).Registered {
		t.Fatal("cannot register again after deleting")
	}
}

func TestWarpOutboundSettings(t *testing.T) {
	outbound := warpState{DeviceID: "d", PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), ClientID: "AQID", PeerKey: warpFixturePeer, Endpoint: "engage.cloudflareclient.com:2408", IPv4: "172.16.0.2"}.outbound()
	encoded, _ := json.Marshal([]any{map[string]any{"tag": "direct", "protocol": "freedom"}, outbound})
	draft := map[string]any{"freedomStrategy": "AsIs", "routingStrategy": "AsIs", "probeURL": "https://example.test/", "statistics": true, "logLevel": "warning", "accessLog": false, "dnsLog": false, "rules": `[{"type":"field","domain":["geosite:openai"],"outboundTag":"warp"}]`, "outbounds": string(encoded), "balancers": "[]", "dns": `{"servers":["localhost"]}`, "advanced": "{}"}
	data, _ := json.Marshal(draft)
	full, err := compileSettings(data)
	if err != nil {
		t.Fatal("WARP outbound rejected", err)
	}
	outbound["settings"].(map[string]any)["noKernelTun"] = false
	encoded, _ = json.Marshal([]any{outbound})
	draft["outbounds"], draft["rules"] = string(encoded), "[]"
	data, _ = json.Marshal(draft)
	if _, err := compileSettings(data); err == nil {
		t.Fatal("kernel WireGuard interface accepted")
	}
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		return
	}
	m, err := core.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	binary, err := m.Binary(runtimeQAVersion(t))
	if err != nil {
		t.Fatal(err)
	}
	full["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": runtimePort(t), "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}
	data, _ = json.Marshal(full)
	file := filepath.Join(t.TempDir(), "warp.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "run", "-test", "-config", file).CombinedOutput(); err != nil {
		t.Fatalf("official core rejected WARP outbound: %s", output)
	}
}
