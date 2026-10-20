package localapi

import (
	"errors"
	"net/http"
	"net/url"

	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
)

// 两步验证（TOTP）：服务器生成密钥并校验一次验证码后才启用；接口均需已登录。
func registerTwoFactor(mux *http.ServeMux, state *store.Store) {
	fail := func(w http.ResponseWriter, err error) {
		code, message := 500, i18n.T("两步验证服务暂不可用", "Two-factor service temporarily unavailable")
		switch {
		case errors.Is(err, store.ErrSession):
			code, message = 401, err.Error()
		case errors.Is(err, store.ErrTwoFactorCode):
			code, message = 403, err.Error()
		case errors.Is(err, store.ErrTwoFactorState):
			code, message = 409, err.Error()
		case errors.Is(err, store.ErrLimited):
			code, message = 429, err.Error()
			w.Header().Set("Retry-After", "900")
		}
		respond(w, code, map[string]string{"error": message})
	}
	mux.HandleFunc("GET /api/auth/2fa", func(w http.ResponseWriter, r *http.Request) {
		enabled, err := state.TwoFactorEnabled(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]bool{"enabled": enabled})
	})
	mux.HandleFunc("POST /api/auth/2fa/setup", func(w http.ResponseWriter, r *http.Request) {
		var request struct{}
		if !readJSON(w, r, &request, 64) {
			return
		}
		secret, username, err := state.BeginTwoFactor(r.Context(), token(r))
		if err != nil {
			fail(w, err)
			return
		}
		query := url.Values{"secret": {secret}, "issuer": {"o-ui"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
		respond(w, 200, map[string]string{"secret": secret, "uri": "otpauth://totp/" + url.PathEscape("o-ui:"+username) + "?" + query.Encode()})
	})
	for path, enable := range map[string]bool{"enable": true, "disable": false} {
		mux.HandleFunc("POST /api/auth/2fa/"+path, func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Code string `json:"code"`
			}
			if !readJSON(w, r, &request, 256) {
				return
			}
			var err error
			if enable {
				err = state.EnableTwoFactor(r.Context(), token(r), request.Code)
			} else {
				err = state.DisableTwoFactor(r.Context(), token(r), request.Code)
			}
			if err != nil {
				fail(w, err)
				return
			}
			respond(w, 200, map[string]bool{"enabled": enable})
		})
	}
}
