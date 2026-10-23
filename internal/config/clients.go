package config

import (
	"crypto/ecdh"
	"encoding/base64"
	"independent-panel/internal/i18n"
	"net"
	"net/url"
	"strconv"
)

// ClientConnection只导出客户端所需字段，绝不序列化服务端stream或私钥。
func ClientConnection(in CompiledInbound, user User, serverName, name string, shareHosts ...string) (map[string]any, string, error) {
	shareHost := ""
	if len(shareHosts) > 0 {
		shareHost = shareHosts[0]
	}
	_, address, err := ProxyEndpoint(in.Listen, shareHost)
	if err != nil || len(shareHosts) > 1 || in.Port < 1024 || in.Port > 65535 || !validHost(serverName) {
		return nil, "", i18n.New("连接地址、端口或服务域名无效", "Invalid connection address, port or server name")
	}
	stream := map[string]any{"network": in.Stream.Network, "security": in.Stream.Security}
	query := url.Values{"sni": {serverName}}
	u := url.URL{Host: net.JoinHostPort(address, strconv.Itoa(in.Port)), Fragment: name}
	out := map[string]any{"tag": "proxy", "protocol": in.Protocol, "streamSettings": stream}
	if in.Protocol == "hysteria" {
		if user.Auth == "" || in.Stream.Security != "tls" {
			return nil, "", i18n.New("HY2连接参数缺失", "HY2 connection parameters are missing")
		}
		out["settings"] = map[string]any{"version": 2, "address": address, "port": in.Port}
		stream["hysteriaSettings"] = map[string]any{"version": 2, "auth": user.Auth}
		u.Scheme, u.User, u.Path = "hysteria2", url.User(user.Auth), "/"
		if in.PortHopping != "" {
			if _, _, ok := ParsePortRange(in.PortHopping); !ok {
				return nil, "", i18n.New("HY2端口跳跃范围无效", "Invalid HY2 port hopping range")
			}
			stream["finalmask"] = map[string]any{"quicParams": map[string]any{"udpHop": map[string]any{"ports": in.PortHopping}}}
			query.Set("mport", in.PortHopping)
		}
	} else if in.Protocol == "vless" && uuidPattern.MatchString(user.ID) {
		client := map[string]any{"id": user.ID, "encryption": "none"}
		query.Set("encryption", "none")
		query.Set("type", in.Stream.Network)
		query.Set("security", in.Stream.Security)
		if user.Flow != "" {
			client["flow"] = user.Flow
			query.Set("flow", user.Flow)
		}
		out["settings"] = map[string]any{"vnext": []any{map[string]any{"address": address, "port": in.Port, "users": []any{client}}}}
		u.Scheme, u.User = "vless", url.User(user.ID)
	} else {
		return nil, "", i18n.New("客户端协议或认证参数无效", "Invalid client protocol or authentication parameters")
	}
	if in.Stream.Security == "reality" {
		r := in.Stream.Reality
		// 服务端可接受多个shortId（如从OUI导入），链接固定用第一个。
		if r == nil || len(r.ShortIDs) == 0 {
			return nil, "", i18n.New("REALITY连接参数缺失", "REALITY connection parameters are missing")
		}
		data, err := base64.RawURLEncoding.DecodeString(r.PrivateKey)
		if err != nil {
			return nil, "", i18n.New("REALITY密钥无效", "Invalid REALITY key")
		}
		key, err := ecdh.X25519().NewPrivateKey(data)
		if err != nil {
			return nil, "", i18n.New("REALITY密钥无效", "Invalid REALITY key")
		}
		public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
		stream["realitySettings"] = map[string]any{"fingerprint": "chrome", "serverName": serverName, "password": public, "shortId": r.ShortIDs[0], "spiderX": "/"}
		query.Set("fp", "chrome")
		query.Set("pbk", public)
		query.Set("sid", r.ShortIDs[0])
		query.Set("spx", "/")
	} else if in.Stream.Security == "tls" && in.Stream.TLS != nil {
		tlsSettings := map[string]any{"serverName": serverName, "alpn": in.Stream.TLS.ALPN, "minVersion": in.Stream.TLS.MinVersion}
		// XHTTP-TLS与REALITY一样显式模拟Chrome握手：官方26.7.28留空时也按chrome，但其他或较旧的客户端未必；HY2走QUIC不用这类指纹。
		if in.Protocol != "hysteria" {
			tlsSettings["fingerprint"] = "chrome"
			query.Set("fp", "chrome")
		}
		stream["tlsSettings"] = tlsSettings
	} else {
		return nil, "", i18n.New("客户端安全参数无效", "Invalid client security parameters")
	}
	if x := in.Stream.XHTTP; x != nil {
		stream["xhttpSettings"] = map[string]string{"path": x.Path, "mode": x.Mode}
		query.Set("path", x.Path)
		query.Set("mode", x.Mode)
	}
	u.RawQuery = query.Encode()
	return out, u.String(), nil
}
