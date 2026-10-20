package localapi

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Cloudflare WARP（参照OUI）：服务器生成WireGuard密钥对，只把公钥发给Cloudflare注册免费设备，
// 用返回的地址、对端公钥与reserved生成tag为warp的WireGuard出站；令牌与私钥只存本机SQLite。
const (
	warpDocument      = "warp"
	warpClientVersion = "a-6.30-3596"
)

var (
	warpAPIBase    = "https://api.cloudflareclient.com/v0a4005"
	warpHTTP       = &http.Client{Timeout: 15 * time.Second}
	warpMu         sync.Mutex
	warpLicenseKey = regexp.MustCompile(`^[A-Za-z0-9]{8}-[A-Za-z0-9]{8}-[A-Za-z0-9]{8}$`)
)

type warpState struct {
	DeviceID   string      `json:"deviceId"`
	Token      string      `json:"token"`
	PrivateKey string      `json:"privateKey"`
	License    string      `json:"license"`
	ClientID   string      `json:"clientId"`
	PeerKey    string      `json:"peerKey"`
	Endpoint   string      `json:"endpoint"`
	IPv4       string      `json:"ipv4"`
	IPv6       string      `json:"ipv6"`
	Account    warpAccount `json:"account"`
}

type warpAccount struct {
	AccountType string `json:"account_type"`
	PremiumData uint64 `json:"premium_data"`
	Quota       uint64 `json:"quota"`
	Usage       uint64 `json:"usage"`
	WarpPlus    bool   `json:"warp_plus"`
	License     string `json:"license"`
}

type warpRegistration struct {
	ID      string      `json:"id"`
	Token   string      `json:"token"`
	Account warpAccount `json:"account"`
	Config  struct {
		ClientID string `json:"client_id"`
		Peers    []struct {
			PublicKey string `json:"public_key"`
			Endpoint  struct {
				Host string `json:"host"`
			} `json:"endpoint"`
		} `json:"peers"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
}

// warpView是返回给网页的状态：不含令牌；许可证只显示末4位；出站含私钥，与Xray设置里的出站JSON同级敏感。
type warpView struct {
	Registered    bool           `json:"registered"`
	AccountType   string         `json:"accountType,omitempty"`
	PremiumData   uint64         `json:"premiumData"`
	Quota         uint64         `json:"quota"`
	Usage         uint64         `json:"usage"`
	LicenseSuffix string         `json:"licenseSuffix,omitempty"`
	Outbound      map[string]any `json:"outbound,omitempty"`
}

func (w warpState) view() warpView {
	if w.DeviceID == "" {
		return warpView{}
	}
	view := warpView{Registered: true, AccountType: w.Account.AccountType, PremiumData: w.Account.PremiumData, Quota: w.Account.Quota, Usage: w.Account.Usage, Outbound: w.outbound()}
	if len(w.License) >= 4 {
		view.LicenseSuffix = w.License[len(w.License)-4:]
	}
	return view
}

// outbound只用用户态网络栈（noKernelTun），不创建系统网卡，也不需要额外权限。
func (w warpState) outbound() map[string]any {
	address := []string{}
	if w.IPv4 != "" {
		address = append(address, w.IPv4+"/32")
	}
	if w.IPv6 != "" {
		address = append(address, w.IPv6+"/128")
	}
	reserved := []int{}
	if decoded, err := base64.StdEncoding.DecodeString(w.ClientID); err == nil {
		for _, b := range decoded {
			reserved = append(reserved, int(b))
		}
	}
	return map[string]any{"tag": "warp", "protocol": "wireguard", "settings": map[string]any{
		"secretKey": w.PrivateKey, "address": address, "reserved": reserved, "mtu": 1280, "domainStrategy": "ForceIP", "noKernelTun": true,
		"peers": []any{map[string]any{"publicKey": w.PeerKey, "endpoint": w.Endpoint, "allowedIPs": []string{"0.0.0.0/0", "::/0"}}},
	}}
}

func readWarp(ctx context.Context, s *store.Store) (warpState, int64, error) {
	doc, err := s.ReadDocument(ctx, warpDocument)
	if err != nil {
		return warpState{}, 0, err
	}
	var state warpState
	if string(doc.Data) != "null" && len(doc.Data) > 0 && json.Unmarshal(doc.Data, &state) != nil {
		return warpState{}, 0, i18n.New("WARP记录损坏", "WARP record is corrupted")
	}
	return state, doc.Revision, nil
}

func warpRequest(ctx context.Context, method, path, token string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, warpAPIBase+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("CF-Client-Version", warpClientVersion)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := warpHTTP.Do(request)
	if err != nil {
		return i18n.New("无法连接Cloudflare WARP接口，请确认服务器能访问api.cloudflareclient.com", "Cannot reach the Cloudflare WARP API; check that the server can reach api.cloudflareclient.com")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		var failure struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		message := ""
		if json.Unmarshal(data, &failure) == nil && len(failure.Errors) > 0 {
			message = strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f {
					return -1
				}
				return r
			}, failure.Errors[0].Message)
			if len(message) > 200 {
				message = message[:200]
			}
		}
		return i18n.Errorf("Cloudflare WARP接口拒绝了请求（%d）%s", "The Cloudflare WARP API rejected the request (%d) %s", response.StatusCode, message)
	}
	if result != nil && json.Unmarshal(data, result) != nil {
		return i18n.New("Cloudflare WARP接口返回无法识别的内容", "The Cloudflare WARP API returned an unrecognized response")
	}
	return nil
}

func validWarpRegistration(reg warpRegistration) bool {
	if reg.ID == "" || reg.Token == "" || len(reg.Config.Peers) == 0 {
		return false
	}
	peer, err := base64.StdEncoding.DecodeString(reg.Config.Peers[0].PublicKey)
	if err != nil || len(peer) != 32 {
		return false
	}
	if _, port, err := net.SplitHostPort(reg.Config.Peers[0].Endpoint.Host); err != nil || port == "" {
		return false
	}
	v4, v6 := net.ParseIP(reg.Config.Interface.Addresses.V4), net.ParseIP(reg.Config.Interface.Addresses.V6)
	if (v4 == nil || v4.To4() == nil) && (v6 == nil || v6.To4() != nil) {
		return false
	}
	client, err := base64.StdEncoding.DecodeString(reg.Config.ClientID)
	return err == nil && len(client) <= 3
}

func registerWarp(mux *http.ServeMux, state *store.Store) {
	fail := func(w http.ResponseWriter, err error) {
		code := 502
		if errors.Is(err, store.ErrConflict) {
			code = 409
		}
		respond(w, code, map[string]string{"error": err.Error()})
	}
	mux.HandleFunc("GET /api/xray/warp", func(w http.ResponseWriter, r *http.Request) {
		warpMu.Lock()
		defer warpMu.Unlock()
		current, revision, err := readWarp(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if r.URL.Query().Get("refresh") == "1" && current.DeviceID != "" {
			var reg warpRegistration
			if err := warpRequest(r.Context(), http.MethodGet, "/reg/"+current.DeviceID, current.Token, nil, &reg); err != nil {
				fail(w, err)
				return
			}
			current.Account = reg.Account
			data, _ := json.Marshal(current)
			if _, err := state.SaveDocument(r.Context(), warpDocument, revision, data); err != nil {
				fail(w, err)
				return
			}
		}
		respond(w, 200, current.view())
	})
	mux.HandleFunc("POST /api/xray/warp/register", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Confirm bool `json:"confirm"`
		}
		if !readJSON(w, r, &request, 1024) {
			return
		}
		warpMu.Lock()
		defer warpMu.Unlock()
		current, revision, err := readWarp(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if !request.Confirm || current.DeviceID != "" {
			respond(w, 409, map[string]string{"error": i18n.T("已有WARP账户（先删除再重新注册），或未确认注册", "A WARP account already exists (delete it before registering again), or the registration was not confirmed")})
			return
		}
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("无法生成WireGuard密钥", "Cannot generate a WireGuard key")})
			return
		}
		var reg warpRegistration
		body := map[string]any{"key": base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), "install_id": "", "fcm_token": "", "tos": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "type": "PC", "model": "o-ui", "locale": "en_US"}
		if err := warpRequest(r.Context(), http.MethodPost, "/reg", "", body, &reg); err != nil {
			fail(w, err)
			return
		}
		if !validWarpRegistration(reg) {
			respond(w, 502, map[string]string{"error": i18n.T("Cloudflare WARP接口返回的配置不完整", "The Cloudflare WARP API returned an incomplete configuration")})
			return
		}
		peer := reg.Config.Peers[0]
		next := warpState{DeviceID: reg.ID, Token: reg.Token, PrivateKey: base64.StdEncoding.EncodeToString(key.Bytes()), License: reg.Account.License, ClientID: reg.Config.ClientID,
			PeerKey: peer.PublicKey, Endpoint: peer.Endpoint.Host, IPv4: reg.Config.Interface.Addresses.V4, IPv6: reg.Config.Interface.Addresses.V6, Account: reg.Account}
		data, _ := json.Marshal(next)
		if _, err := state.SaveDocument(r.Context(), warpDocument, revision, data); err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, next.view())
	})
	mux.HandleFunc("POST /api/xray/warp/license", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			License string `json:"license"`
		}
		if !readJSON(w, r, &request, 1024) {
			return
		}
		if !warpLicenseKey.MatchString(request.License) {
			respond(w, 400, map[string]string{"error": i18n.T("WARP+许可证格式应为xxxxxxxx-xxxxxxxx-xxxxxxxx", "The WARP+ license must look like xxxxxxxx-xxxxxxxx-xxxxxxxx")})
			return
		}
		warpMu.Lock()
		defer warpMu.Unlock()
		current, revision, err := readWarp(r.Context(), state)
		if err != nil || current.DeviceID == "" {
			respond(w, 409, map[string]string{"error": i18n.T("请先创建WARP账户", "Create a WARP account first")})
			return
		}
		var account warpAccount
		if err := warpRequest(r.Context(), http.MethodPut, "/reg/"+current.DeviceID+"/account", current.Token, map[string]string{"license": request.License}, &account); err != nil {
			fail(w, err)
			return
		}
		current.License = request.License
		if account.AccountType != "" {
			current.Account = account
		}
		data, _ := json.Marshal(current)
		if _, err := state.SaveDocument(r.Context(), warpDocument, revision, data); err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, current.view())
	})
	// 只删除本机记录；已加入Xray设置的warp出站需在出站列表里另行删除并保存。
	mux.HandleFunc("DELETE /api/xray/warp", func(w http.ResponseWriter, r *http.Request) {
		warpMu.Lock()
		defer warpMu.Unlock()
		_, revision, err := readWarp(r.Context(), state)
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if _, err := state.SaveDocument(r.Context(), warpDocument, revision, json.RawMessage("null")); err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, warpView{})
	})
}
