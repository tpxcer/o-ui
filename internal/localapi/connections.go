package localapi

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"independent-panel/internal/i18n"
	"net/http"
	"os"
	"strings"

	"independent-panel/internal/config"
	"independent-panel/internal/store"
)

type connectionRequest struct {
	ClientID          string `json:"clientId"`
	WorkspaceRevision int64  `json:"workspaceRevision"`
}

var errConnectionNotApplied = i18n.New("已保存的入站或设置尚未应用，请先预检并应用配置，再读取连接信息", "Saved inbounds or settings are not applied yet; run preflight and apply before reading connection info")

type connectionResult struct {
	Name              string         `json:"name"`
	Preset            string         `json:"preset"`
	Version           string         `json:"version"`
	WorkspaceRevision int64          `json:"workspaceRevision"`
	SettingsRevision  int64          `json:"settingsRevision"`
	Link              string         `json:"link"`
	Outbound          map[string]any `json:"outbound"`
	// 仅TLS预设：证书链能否被本机系统根信任；自签或私有CA为false，手机客户端默认会拒绝。
	CertificateTrusted *bool `json:"certificateTrusted,omitempty"`
}

// publiclyTrusted用系统根（测试可传入roots）校验证书链与服务名，不改变导出链接的校验要求。
func publiclyTrusted(chain []byte, serverName string, roots *x509.CertPool) bool {
	var certs []*x509.Certificate
	for block, rest := pem.Decode(chain); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return false
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return false
	}
	intermediates := x509.NewCertPool()
	for _, cert := range certs[1:] {
		intermediates.AddCert(cert)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{DNSName: serverName, Intermediates: intermediates, Roots: roots})
	return err == nil
}

func (rt *Runtime) connection(ctx context.Context, request connectionRequest) (connectionResult, error) {
	result := connectionResult{}
	c, running := rt.engine.Current()
	if !running {
		return result, i18n.New("独立Xray尚未运行，请先预检并应用已保存配置", "Xray is not running; run preflight and apply the saved configuration first")
	}
	workspace, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		return result, i18n.New("无法读取客户端记录", "Cannot read client records")
	}
	settings, err := rt.state.ReadDocument(ctx, "xray-settings")
	if err != nil {
		return result, i18n.New("无法读取设置修订", "Cannot read the settings revision")
	}
	if workspace.Revision != request.WorkspaceRevision {
		return result, store.ErrConflict
	}
	if workspace.Revision != c.WorkspaceRevision || settings.Revision != c.SettingsRevision {
		return result, errConnectionNotApplied
	}
	var draft workspaceDraft
	if json.Unmarshal(workspace.Data, &draft) != nil {
		return result, i18n.New("客户端记录无效", "Invalid client records")
	}
	var client *clientDraft
	for i := range draft.Clients {
		if draft.Clients[i].ID == request.ClientID {
			client = &draft.Clients[i]
			break
		}
	}
	if client == nil {
		return result, i18n.New("客户端不存在", "Client does not exist")
	}
	data, err := os.ReadFile(c.File)
	var applied struct {
		Inbounds []config.CompiledInbound `json:"inbounds"`
	}
	if err != nil || json.Unmarshal(data, &applied) != nil {
		return result, i18n.New("无法读取已生效配置", "Cannot read the active configuration")
	}
	for _, in := range applied.Inbounds {
		if in.Tag != "in-"+client.InboundID {
			continue
		}
		for _, user := range in.Settings.Clients {
			if user.Email != "client-"+client.ID {
				continue
			}
			serverName := ""
			shareHost := ""
			preset := ""
			for _, entry := range draft.Inbounds {
				if entry.ID == client.InboundID {
					shareHost = entry.ShareHost
					preset = entry.Preset
					in.PortHopping = entry.PortHopping
					if entry.Connection != nil {
						serverName = entry.Connection.ServerName
					}
					break
				}
			}
			if in.Stream.Reality != nil && len(in.Stream.Reality.ServerNames) == 1 {
				serverName = in.Stream.Reality.ServerNames[0]
			}
			if in.Stream.TLS != nil {
				if len(in.Stream.TLS.Certificates) != 1 {
					return result, i18n.New("TLS证书配置无效", "Invalid TLS certificate configuration")
				}
				certificate, err := readPEM(in.Stream.TLS.Certificates[0].File)
				if err != nil {
					return result, i18n.New("无法读取已生效证书", "Cannot read the active certificate")
				}
				block, _ := pem.Decode(certificate)
				if block == nil {
					return result, i18n.New("已生效证书无效", "Active certificate is invalid")
				}
				leaf, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					return result, i18n.New("已生效证书无效", "Active certificate is invalid")
				}
				if serverName == "" {
					for _, name := range leaf.DNSNames {
						if !strings.Contains(name, "*") {
							serverName = name
							break
						}
					}
					if serverName == "" && len(leaf.IPAddresses) > 0 {
						serverName = leaf.IPAddresses[0].String()
					}
				}
				if serverName == "" || leaf.VerifyHostname(serverName) != nil {
					return result, i18n.New("请在入站连接参数填写与TLS证书匹配的服务域名，再应用配置；不跳过证书验证", "Set a server name matching the TLS certificate in the inbound connection settings and apply again; certificate verification is never skipped")
				}
				trusted := publiclyTrusted(certificate, serverName, nil)
				result.CertificateTrusted = &trusted
			}
			outbound, link, err := config.ClientConnection(in, user, serverName, client.Name, shareHost)
			if err != nil {
				return result, err
			}
			return connectionResult{Name: client.Name, Preset: preset, Version: c.Version, WorkspaceRevision: c.WorkspaceRevision, SettingsRevision: c.SettingsRevision, Link: link, Outbound: outbound, CertificateTrusted: result.CertificateTrusted}, nil
		}
	}
	return result, i18n.New("该客户端未在当前生效配置中启用，不提供连接凭据", "This client is not enabled in the active configuration; no credentials are provided")
}

func registerConnections(mux *http.ServeMux, rt *Runtime) {
	mux.HandleFunc("POST /api/runtime/connection", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		var request connectionRequest
		if !readJSON(w, r, &request, 1024) {
			return
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		result, err := rt.connection(r.Context(), request)
		if err != nil {
			code := 422
			if errors.Is(err, store.ErrConflict) || errors.Is(err, errConnectionNotApplied) {
				code = 409
			}
			respond(w, code, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, result)
	})
}
