package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"independent-panel/internal/config"
	"independent-panel/internal/store"
)

type PanelAccessDraft struct {
	Public   bool   `json:"public"`
	Listen   string `json:"listen"`
	Origin   string `json:"origin"`
	BasePath string `json:"basePath,omitempty"`
}

// 面板访问路径（参照OUI的Web路径）：/ 或 /一段字母数字下划线短横线/；订阅/sub/始终在根路径，分享订阅不暴露面板路径。
var panelBasePath = regexp.MustCompile(`^/(?:[A-Za-z0-9_-]{1,64}/)?$`)

func validPanelBasePath(path string) bool {
	switch strings.ToLower(path) {
	case "/api/", "/sub/", "/assets/":
		return false
	}
	return path == "" || panelBasePath.MatchString(path)
}

// basePath返回规范化路径；空值视为根路径。
func (d PanelAccessDraft) basePath() string {
	if d.BasePath == "" {
		return "/"
	}
	return d.BasePath
}

// PanelAccess为启动快照；保存新设置不会改变当前监听或请求边界。
type PanelAccess struct {
	Data     PanelAccessDraft
	Revision int64
}

func validatePanelAccess(d PanelAccessDraft, certificate *PanelTLS) error {
	if !validPanelBasePath(d.BasePath) {
		return i18n.New("访问路径须为 / 或 /字母数字下划线短横线/（1–64位），不能是 /api/、/sub/、/assets/", "The access path must be / or /letters-digits-underscores-hyphens/ (1–64), and cannot be /api/, /sub/ or /assets/")
	}
	address, err := netip.ParseAddrPort(d.Listen)
	if err != nil || address.Addr().Zone() != "" || address.Addr().Is4In6() || address.Addr().IsMulticast() || address.Addr().String() == "255.255.255.255" || address.Port() < 1024 {
		return i18n.New("面板监听须为字面IPv4或带方括号IPv6及1024–65535端口，不含区域标识", "Panel listen must be a literal IPv4 or bracketed IPv6 with port 1024–65535, without zone")
	}
	if !d.Public {
		if !address.Addr().IsLoopback() || d.Origin != "" {
			return i18n.New("仅本机模式必须使用回环监听，HTTPS访问来源留空", "Local-only mode must listen on loopback with the HTTPS origin left empty")
		}
		return nil
	}
	origin, err := url.Parse(d.Origin)
	if err != nil || origin.Scheme != "https" || origin.Opaque != "" || origin.User != nil || origin.Host == "" || d.Origin != "https://"+origin.Host || origin.Host != strings.ToLower(origin.Host) || !config.ValidShareHost(origin.Hostname()) {
		return i18n.New("请填写完整小写HTTPS访问来源（https://域名或IP:端口），不含路径、凭据、查询或片段", "Enter a complete lowercase HTTPS origin (https://domain-or-IP:port) without path, credentials, query or fragment")
	}
	port := origin.Port()
	if port == "" {
		port = "443"
	}
	if port != strconv.Itoa(int(address.Port())) {
		return i18n.New("HTTPS访问端口必须与直接监听端口相同；当前不支持反向代理或端口转发", "The HTTPS port must equal the listen port; reverse proxies and port forwarding are not supported")
	}
	if certificate == nil || certificate.pair.Leaf == nil || certificate.pair.Leaf.VerifyHostname(origin.Hostname()) != nil {
		return i18n.New("启用非本机访问需要有效且匹配HTTPS访问域名或IP的面板证书", "Remote access requires a valid panel certificate matching the HTTPS domain or IP")
	}
	return nil
}

func LoadPanelAccess(ctx context.Context, state *store.Store, fallback string, certificate *PanelTLS) (*PanelAccess, error) {
	doc, err := state.ReadDocument(ctx, "panel-access")
	if err != nil {
		return nil, i18n.New("无法读取面板访问设置", "Cannot read panel access settings")
	}
	access := &PanelAccess{Data: PanelAccessDraft{Listen: fallback}, Revision: doc.Revision}
	if doc.Revision > 0 && string(doc.Data) == "null" {
		return nil, i18n.New("已保存的面板访问设置为空，拒绝回退默认监听", "Saved panel access settings are empty; refusing to fall back to the default listen address")
	}
	if string(doc.Data) != "null" {
		access.Data = PanelAccessDraft{}
		if decodeStrict(doc.Data, &access.Data) != nil {
			return nil, i18n.New("面板访问设置损坏，拒绝回退默认监听", "Panel access settings are corrupted; refusing to fall back to the default listen address")
		}
	}
	if err := validatePanelAccess(access.Data, certificate); err != nil {
		return nil, err
	}
	if access.Data.Public {
		initialized, err := state.Initialized(ctx)
		if err != nil || !initialized {
			return nil, i18n.New("请先在仅本机模式初始化管理员，再启用HTTPS非本机访问", "Set up the administrator in local-only mode before enabling remote HTTPS access")
		}
	}
	return access, nil
}

// 只由已校验的启动快照构造；不从请求Host或转发头自动扩大允许来源。
func (access *PanelAccess) hosts() []string {
	hosts := []string{access.Data.Listen}
	if access.Data.Public {
		origin, _ := url.Parse(access.Data.Origin)
		hosts = []string{origin.Host}
	} else {
		_, port, _ := net.SplitHostPort(access.Data.Listen)
		hosts = append(hosts, "localhost:"+port, "127.0.0.1:5178", "localhost:5178")
	}
	return hosts
}

func registerPanelAccess(mux *http.ServeMux, rt *Runtime, active *PanelAccess) {
	status := func(w http.ResponseWriter, r *http.Request) {
		doc, err := rt.state.ReadDocument(r.Context(), "panel-access")
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取面板访问设置失败", "Failed to read panel access settings")})
			return
		}
		respond(w, 200, map[string]any{"active": active.Data, "activeRevision": active.Revision, "revision": doc.Revision, "data": doc.Data})
	}
	mux.HandleFunc("GET /api/panel/access", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("面板访问设置尚未启用", "Panel access settings are not enabled")})
			return
		}
		status(w, r)
	})
	mux.HandleFunc("PUT /api/panel/access", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("面板访问设置尚未启用", "Panel access settings are not enabled")})
			return
		}
		var request struct {
			Revision     int64            `json:"revision"`
			Data         PanelAccessDraft `json:"data"`
			Confirmation string           `json:"confirmation"`
		}
		if !readJSON(w, r, &request, 4096) {
			return
		}
		rt.panelMu.Lock()
		defer rt.panelMu.Unlock()
		if request.Data.Public && request.Confirmation != "确认启用HTTPS非本机访问" {
			respond(w, 409, map[string]string{"error": i18n.T("请明确确认非本机管理/订阅将增加暴露面；持有订阅链接者可取得代理凭据", "Confirm that remote management/subscriptions increase exposure; anyone with a subscription link can get proxy credentials")})
			return
		}
		certificate, err := LoadPanelTLS(r.Context(), rt.state)
		if err != nil && request.Data.Public {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := validatePanelAccess(request.Data, certificate); err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		data, _ := json.Marshal(request.Data)
		if _, err := rt.state.SaveDocument(r.Context(), "panel-access", request.Revision, data); err != nil {
			code, message := 500, i18n.T("保存访问设置失败，当前监听未改变；请重新读取确认结果", "Failed to save access settings; listen address unchanged, reload to confirm the result")
			if errors.Is(err, store.ErrConflict) {
				code, message = 409, store.ErrConflict.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		status(w, r)
	})
}
