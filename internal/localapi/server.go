// Package localapi 默认仅本机访问；显式非本机模式须使用校验后的HTTPS启动快照。
package localapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/i18n"
	"independent-panel/internal/monitor"
	"independent-panel/internal/store"
)

const sessionCookie = "independent_panel_session"

func Handler(manager *core.Manager, state *store.Store, dist string, hosts []string, runtime ...*Runtime) http.Handler {
	var rt *Runtime
	if len(runtime) > 0 {
		rt = runtime[0]
	}
	listen := "127.0.0.1:5180"
	if len(hosts) > 0 {
		listen = hosts[0]
	}
	return handler(manager, state, dist, hosts, rt, &PanelAccess{Data: PanelAccessDraft{Listen: listen}})
}

func PanelHandler(manager *core.Manager, state *store.Store, dist string, access *PanelAccess, rt *Runtime) http.Handler {
	return handler(manager, state, dist, access.hosts(), rt, access)
}

func handler(manager *core.Manager, state *store.Store, dist string, hosts []string, rt *Runtime, access *PanelAccess) http.Handler {
	allowed := map[string]bool{}
	for _, host := range hosts {
		allowed[host] = true
	}
	mux := http.NewServeMux()
	registerRuntime(mux, rt)
	registerBackup(mux, rt)
	registerPanelTLS(mux, rt)
	registerPanelAccess(mux, rt, access)
	registerPanelGeneral(mux, rt, access)
	registerTwoFactor(mux, state)
	registerSubscriptions(mux, rt)
	registerServerPlan(mux, rt)
	registerPublicInfo(mux, rt)
	registerPanelVersion(mux)
	registerSubscriptionSettings(mux, state)
	registerPanelRestart(mux)
	registerApplySaved(mux, rt)
	registerOutboundTraffic(mux, rt)
	registerFreePort(mux, state)
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil || rt.logs == nil {
			respond(w, 503, map[string]string{"error": i18n.T("诊断日志尚未启动", "Diagnostics log is not running")})
			return
		}
		respond(w, 200, rt.logs.Snapshot())
	})
	mux.HandleFunc("GET /api/runtime/metrics", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil || rt.metrics == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心指标采集尚未启动", "Core metrics sampling is not running")})
			return
		}
		respond(w, 200, rt.metrics.Snapshot())
	})
	mux.HandleFunc("GET /api/system/status", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil || rt.monitor == nil {
			respond(w, 503, map[string]string{"error": i18n.T("系统采集尚未启动", "System sampling is not running")})
			return
		}
		// 网速只算面板业务；网卡名单独给出，供服务器套餐选择计量网卡。
		snapshot, interfaces := rt.monitor.Snapshot(), []string{}
		if snapshot.Network != nil {
			interfaces = snapshot.Network.Interfaces
		}
		snapshot.Network, snapshot.History = rt.business.network()
		respond(w, 200, struct {
			monitor.Snapshot
			Interfaces []string `json:"interfaces"`
		}{snapshot, interfaces})
	})
	mux.HandleFunc("GET /api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		initialized, err := state.Initialized(r.Context())
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("无法读取登录状态", "Cannot read sign-in status")})
			return
		}
		session, authErr := state.Authenticate(r.Context(), token(r))
		if authErr != nil && !errors.Is(authErr, store.ErrSession) {
			respond(w, 500, map[string]string{"error": i18n.T("登录服务暂不可用", "Sign-in service temporarily unavailable")})
			return
		}
		twoFactor, err := state.TwoFactorEnabled(r.Context())
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("登录服务暂不可用", "Sign-in service temporarily unavailable")})
			return
		}
		respond(w, 200, map[string]any{"initialized": initialized, "authenticated": session.Username != "", "username": session.Username, "twoFactor": twoFactor})
	})
	credentials := func(setup bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Username string `json:"username"`
				Password string `json:"password"`
				Code     string `json:"code,omitempty"`
			}
			if !readJSON(w, r, &request, 2048) {
				return
			}
			var session store.Session
			var err error
			if setup {
				session, err = state.Initialize(r.Context(), request.Username, request.Password)
			} else {
				session, err = state.LoginWithCode(r.Context(), request.Username, request.Password, request.Code)
			}
			if err != nil {
				code, message := 500, i18n.T("登录服务暂不可用", "Sign-in service temporarily unavailable")
				switch {
				case errors.Is(err, store.ErrInitialized):
					code, message = 409, err.Error()
				case errors.Is(err, store.ErrCredentials), errors.Is(err, store.ErrTwoFactorCredentials):
					code, message = 401, err.Error()
				case errors.Is(err, store.ErrLimited):
					code, message = 429, err.Error()
					w.Header().Set("Retry-After", "900")
				case setup && store.ValidateAdministrator(request.Username, request.Password) != nil:
					code, message = 400, err.Error()
				}
				respond(w, code, map[string]string{"error": message})
				return
			}
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: session.Token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(time.Until(session.ExpiresAt).Round(time.Second) / time.Second), Expires: session.ExpiresAt})
			respond(w, 200, map[string]any{"authenticated": true, "username": session.Username, "expiresAt": session.ExpiresAt})
		}
	}
	mux.HandleFunc("POST /api/auth/setup", credentials(true))
	mux.HandleFunc("POST /api/auth/login", credentials(false))
	mux.HandleFunc("POST /api/auth/password", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
			NewUsername     string `json:"newUsername,omitempty"`
			Code            string `json:"code,omitempty"`
		}
		if !readJSON(w, r, &request, 2048) {
			return
		}
		if request.NewPassword == "" && request.NewUsername == "" {
			respond(w, 400, map[string]string{"error": i18n.T("请填写新用户名或新密码", "Enter a new username or a new password")})
			return
		}
		if err := state.ChangeAccountWithCode(r.Context(), token(r), request.CurrentPassword, request.NewUsername, request.NewPassword, request.Code); err != nil {
			code, message := 500, i18n.T("密码修改失败，请重试；若请求超时，请尝试新密码登录确认结果", "Password change failed; retry, and if the request timed out try signing in with the new password to confirm")
			switch {
			case errors.Is(err, store.ErrSession):
				code, message = 401, err.Error()
			case errors.Is(err, store.ErrCurrentPassword), errors.Is(err, store.ErrTwoFactorCode):
				code, message = 403, err.Error()
			case errors.Is(err, store.ErrLimited):
				code, message = 429, err.Error()
				w.Header().Set("Retry-After", "900")
			case errors.Is(err, store.ErrSamePassword), request.NewPassword != "" && store.ValidatePassword(request.NewPassword) != nil, request.NewUsername != "" && store.ValidateUsername(request.NewUsername) != nil:
				code, message = 400, err.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
		respond(w, 200, map[string]bool{"authenticated": false, "passwordChanged": true})
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		var request struct{}
		if !readJSON(w, r, &request, 64) {
			return
		}
		if err := state.Logout(r.Context(), token(r)); err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("退出失败，请重试", "Sign-out failed; please retry")})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
		respond(w, 200, map[string]bool{"authenticated": false})
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		doc, err := state.ReadDocument(r.Context(), "xray-settings")
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取设置失败", "Failed to read settings")})
			return
		}
		respond(w, 200, doc)
	})
	mux.HandleFunc("GET /api/workspace", func(w http.ResponseWriter, r *http.Request) {
		doc, err := state.ReadDocument(r.Context(), "workspace")
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取入站草稿失败", "Failed to read inbound draft")})
			return
		}
		respond(w, 200, doc)
	})
	mux.HandleFunc("PUT /api/workspace", func(w http.ResponseWriter, r *http.Request) {
		if rt != nil {
			rt.mu.Lock()
			defer rt.mu.Unlock()
		}
		var request store.Document
		if !readJSON(w, r, &request, 512*1024) {
			return
		}
		old, err := state.ReadDocument(r.Context(), "workspace")
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取入站草稿失败", "Failed to read inbound draft")})
			return
		}
		if old.Revision != request.Revision {
			respond(w, 409, map[string]string{"error": store.ErrConflict.Error()})
			return
		}
		if err = validateWorkspaceDraft(request.Data, old.Data); err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		doc, err := saveSubscriptionWorkspace(r.Context(), state, old, request.Data)
		if err != nil {
			code, message := 500, i18n.T("保存入站草稿失败，未改变已保存数据", "Failed to save inbound draft; saved data unchanged")
			if errors.Is(err, store.ErrConflict) {
				code, message = 409, err.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		result := rt.saved(r.Context(), doc)
		result.Firewall = syncFirewall(r.Context(), old.Data, doc.Data)
		respond(w, 200, result)
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		if rt != nil {
			rt.mu.Lock()
			defer rt.mu.Unlock()
		}
		var request store.Document
		if !readJSON(w, r, &request, 128*1024) {
			return
		}
		if err := validateSettingsDraft(request.Data); err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		doc, err := state.SaveDocument(r.Context(), "xray-settings", request.Revision, request.Data)
		if err != nil {
			code, message := 500, i18n.T("保存设置失败，未改变已保存数据", "Failed to save settings; saved data unchanged")
			if errors.Is(err, store.ErrConflict) {
				code, message = 409, err.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		respond(w, 200, rt.saved(r.Context(), doc))
	})
	mux.HandleFunc("GET /api/core/status", func(w http.ResponseWriter, r *http.Request) {
		status := manager.Status()
		if rt != nil {
			status.Runtime = "stopped"
			if rt.engine.Status().Running {
				status.Runtime = "running"
			}
		}
		respond(w, http.StatusOK, status)
	})
	mux.HandleFunc("GET /api/core/releases", func(w http.ResponseWriter, r *http.Request) {
		result, err := manager.Releases(r.Context())
		if err != nil {
			respond(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		respond(w, http.StatusOK, map[string]any{"releases": result})
	})
	mux.HandleFunc("POST /api/core/install", func(w http.ResponseWriter, r *http.Request) {
		var selectionLock sync.Locker
		if rt != nil {
			selectionLock = &rt.mu
		}
		var request struct {
			Version string `json:"version"`
		}
		if !readJSON(w, r, &request, 1024) {
			return
		}
		if _, err := core.NormalizeVersion(request.Version); err != nil {
			respond(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := manager.InstallWithSelectionLock(r.Context(), request.Version, selectionLock); err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, core.ErrBusy) {
				status = http.StatusConflict
			}
			respond(w, status, map[string]string{"error": err.Error()})
			return
		}
		status := manager.Status()
		if rt != nil {
			status.Runtime = "stopped"
			if rt.engine.Status().Running {
				status.Runtime = "running"
			}
		}
		respond(w, http.StatusOK, status)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusNotFound, map[string]string{"error": i18n.T("接口不存在", "API not found")})
	})
	// 安装器选择的面板默认语言（O_UI_DEFAULT_LANG=zh|en）写入首页；访客在页面保存的选择仍优先。
	if lang := os.Getenv("O_UI_DEFAULT_LANG"); lang == "zh" || lang == "en" {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			page, err := os.ReadFile(filepath.Join(dist, "index.html"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(bytes.Replace(page, []byte("<head>"), []byte(`<head><meta name="o-ui-default-lang" content="`+lang+`">`), 1))
		})
	}
	mux.Handle("/", http.FileServer(http.Dir(dist)))
	base := access.Data.basePath()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = &langWriter{ResponseWriter: w, lang: i18n.RequestLang(r)}
		// 面板只在访问路径下提供；订阅保留根路径/sub/，其余路径一律404，不提示面板位置。
		if base != "/" && r.URL.Path != "/sub" && !strings.HasPrefix(r.URL.Path, "/sub/") {
			if r.URL.Path == strings.TrimSuffix(base, "/") {
				http.Redirect(w, r, base, http.StatusFound)
				return
			}
			if !strings.HasPrefix(r.URL.Path, base) {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path, r.URL.RawPath = "/"+strings.TrimPrefix(r.URL.Path, base), ""
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		subscription := r.URL.Path == "/sub" || strings.HasPrefix(r.URL.Path, "/sub/")
		if strings.HasPrefix(r.URL.Path, "/api/") || subscription {
			w.Header().Set("Cache-Control", "no-store")
		}
		if subscription {
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		remote, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(remote)
		if err != nil || ip == nil || (!access.Data.Public && !ip.IsLoopback()) || !allowed[r.Host] || (access.Data.Public && r.TLS == nil) {
			respond(w, http.StatusForbidden, map[string]string{"error": i18n.T("访问来源或HTTPS连接不符合本次启动的面板访问设置", "Source or HTTPS connection does not match this run's panel access settings")})
			return
		}
		if access.Data.Public {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
			if r.URL.Path == "/api/auth/setup" {
				respond(w, http.StatusForbidden, map[string]string{"error": i18n.T("管理员只能在仅本机模式初始化", "The administrator can only be set up in local-only mode")})
				return
			}
		}
		if subscription && (r.Method != http.MethodGet || !subscriptionPath.MatchString(r.URL.Path) || r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path) {
			respond(w, 404, map[string]string{"error": i18n.T("订阅不存在或已失效", "Subscription does not exist or has expired")})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || subscription {
			origin := r.Header.Get("Origin")
			scheme := "http://"
			if r.TLS != nil {
				scheme = "https://"
			}
			if (origin != "" && origin != scheme+r.Host) || (r.Method != http.MethodGet && origin == "") || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				respond(w, http.StatusForbidden, map[string]string{"error": i18n.T("拒绝跨来源核心操作", "Cross-origin operations are refused")})
				return
			}
			public := (r.Method == "GET" && r.URL.Path == "/api/auth/status") || (r.Method == "POST" && (r.URL.Path == "/api/auth/setup" || r.URL.Path == "/api/auth/login"))
			if !public && !subscription {
				if _, err := state.Authenticate(r.Context(), token(r)); err != nil {
					if !errors.Is(err, store.ErrSession) {
						respond(w, 500, map[string]string{"error": i18n.T("登录服务暂不可用", "Sign-in service temporarily unavailable")})
						return
					}
					respond(w, 401, map[string]string{"error": store.ErrSession.Error()})
					return
				}
			}
		}
		if rt != nil && rt.logs != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut) {
			result := &operationWriter{ResponseWriter: w}
			mux.ServeHTTP(result, r)
			status := result.status
			if status == 0 {
				status = http.StatusOK
			}
			rt.logs.Operation(r.Method+" "+r.URL.Path, status)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type operationWriter struct {
	http.ResponseWriter
	status int
}

func (w *operationWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *operationWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}
func (w *operationWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// langWriter携带本次请求语言，respond据此把已登记的中文换成英文。
type langWriter struct {
	http.ResponseWriter
	lang string
}

func (w *langWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func languageOf(w http.ResponseWriter) string {
	for w != nil {
		if l, ok := w.(*langWriter); ok {
			return l.lang
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		w = u.Unwrap()
	}
	return "zh"
}

func token(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func readJSON(w http.ResponseWriter, r *http.Request, value any, limit int64) bool {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		respond(w, 415, map[string]string{"error": i18n.T("只接受JSON请求", "Only JSON requests are accepted")})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		respond(w, 400, map[string]string{"error": i18n.T("请求格式无效或内容过长", "Invalid request format or content too long")})
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, value any) {
	if languageOf(w) == "en" {
		if translated, err := i18n.TranslateJSON(value); err == nil {
			value = translated
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
