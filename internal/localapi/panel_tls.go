package localapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"independent-panel/internal/store"
)

type panelTLSDraft struct {
	CertificateFile string `json:"certificateFile"`
	KeyFile         string `json:"keyFile"`
}

// PanelTLS持有启动时读到的同一对证书，节点复用不会另读可能已轮换的源文件。
type PanelTLS struct {
	pair      tls.Certificate
	cert, key []byte
	names     []string
	revision  int64
}

func loadPanelPair(d panelTLSDraft) (*PanelTLS, error) {
	if !filepath.IsAbs(d.CertificateFile) || !filepath.IsAbs(d.KeyFile) || d.CertificateFile == d.KeyFile || strings.ContainsAny(d.CertificateFile+d.KeyFile, "\x00\r\n") {
		return nil, i18n.New("请填写不同的证书/私钥绝对路径", "Enter distinct absolute certificate/key paths")
	}
	cert, err := readPEM(d.CertificateFile)
	if err != nil {
		return nil, i18n.New("无法读取面板证书（限1 MB普通PEM文件）", "Cannot read the panel certificate (regular PEM file up to 1 MB)")
	}
	key, err := readPEM(d.KeyFile)
	if err != nil {
		return nil, i18n.New("无法读取面板私钥（限1 MB普通PEM文件）", "Cannot read the panel private key (regular PEM file up to 1 MB)")
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, i18n.New("面板证书和私钥不匹配或格式无效", "Panel certificate and key do not match or are invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return nil, i18n.New("面板证书尚未生效或已过期", "Panel certificate is not yet valid or has expired")
	}
	pair.Leaf = leaf
	names := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	return &PanelTLS{pair: pair, cert: cert, key: key, names: names}, nil
}

// LoadPanelTLS只在启动时读取；设置损坏或证书无效时拒绝启动，不静默降级HTTP。
func LoadPanelTLS(ctx context.Context, s *store.Store) (*PanelTLS, error) {
	doc, err := s.ReadDocument(ctx, "panel-tls")
	if err != nil {
		return nil, i18n.New("无法读取面板HTTPS设置", "Cannot read panel HTTPS settings")
	}
	if string(doc.Data) == "null" {
		return nil, nil
	}
	var d panelTLSDraft
	if json.Unmarshal(doc.Data, &d) != nil {
		return nil, i18n.New("面板HTTPS设置损坏", "Panel HTTPS settings are corrupted")
	}
	p, err := loadPanelPair(d)
	if err == nil {
		p.revision = doc.Revision
	}
	return p, err
}

func (p *PanelTLS) Config() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{p.pair}}
}

func registerPanelTLS(mux *http.ServeMux, rt *Runtime) {
	status := func(w http.ResponseWriter, r *http.Request) {
		doc, err := rt.state.ReadDocument(r.Context(), "panel-tls")
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("读取面板HTTPS设置失败", "Failed to read panel HTTPS settings")})
			return
		}
		names := []string{}
		activeRevision := int64(0)
		if rt.panelTLS != nil {
			names = append(names, rt.panelTLS.names...)
			activeRevision = rt.panelTLS.revision
		}
		respond(w, 200, map[string]any{"active": rt.panelTLS != nil, "serverNames": names, "activeRevision": activeRevision, "revision": doc.Revision, "data": doc.Data})
	}
	mux.HandleFunc("GET /api/panel/tls", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("面板HTTPS设置尚未启用", "Panel HTTPS settings are not enabled")})
			return
		}
		status(w, r)
	})
	mux.HandleFunc("PUT /api/panel/tls", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("面板HTTPS设置尚未启用", "Panel HTTPS settings are not enabled")})
			return
		}
		var request struct {
			Revision int64         `json:"revision"`
			Data     panelTLSDraft `json:"data"`
		}
		if !readJSON(w, r, &request, 16384) {
			return
		}
		rt.panelMu.Lock()
		defer rt.panelMu.Unlock()
		certificate, err := loadPanelPair(request.Data)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if _, err := LoadPanelAccess(r.Context(), rt.state, "127.0.0.1:5180", certificate); err != nil {
			respond(w, 400, map[string]string{"error": i18n.Wrap("证书与已保存访问设置不兼容：", "Certificate is incompatible with the saved access settings: ", err).Error()})
			return
		}
		data, _ := json.Marshal(request.Data)
		if _, err := rt.state.SaveDocument(r.Context(), "panel-tls", request.Revision, data); err != nil {
			code, message := 500, i18n.T("保存面板HTTPS设置失败，当前连接未改变", "Failed to save panel HTTPS settings; current connection unchanged")
			if errors.Is(err, store.ErrConflict) {
				code, message = 409, store.ErrConflict.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		status(w, r)
	})
}
