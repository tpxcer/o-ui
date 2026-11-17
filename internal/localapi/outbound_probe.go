package localapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"independent-panel/internal/engine"
	"independent-panel/internal/i18n"
)

// 只启动目标及其链式依赖，避免测试时启动无关出站、反向桥接或自动探测。
func outboundProbeConfig(data json.RawMessage, tag, socket string) (map[string]any, string, error) {
	if err := validateSettingsDraft(data); err != nil {
		return nil, "", err
	}
	var draft settingsDraft
	_ = json.Unmarshal(data, &draft)
	var outbounds []map[string]any
	_ = json.Unmarshal([]byte(draft.Outbounds), &outbounds)
	byTag := map[string]map[string]any{}
	for _, outbound := range outbounds {
		name, _ := outbound["tag"].(string)
		if name == "" || byTag[name] != nil {
			return nil, "", i18n.New("出站tag缺失或重复", "Outbound tag missing or duplicated")
		}
		byTag[name] = outbound
	}
	var selected []map[string]any
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		outbound := byTag[name]
		if outbound == nil || visiting[name] || len(selected) >= 64 {
			return i18n.New("测试出站不存在，或链式出站依赖缺失、循环或过多", "Test outbound is missing, or its chain has missing, cyclic or too many dependencies")
		}
		if visited[name] {
			return nil
		}
		switch outbound["protocol"] {
		case "freedom", "vless", "vmess", "trojan", "shadowsocks", "socks", "http", "hysteria", "wireguard":
		default:
			return i18n.New("该出站协议不支持连接测试", "This outbound protocol does not support connection tests")
		}
		settings, _ := outbound["settings"].(map[string]any)
		if settings["reverse"] != nil {
			return i18n.New("反向桥接出站不支持独立连接测试", "Reverse bridge outbounds do not support isolated connection tests")
		}
		visiting[name] = true
		selected = append(selected, outbound)
		proxy, _ := outbound["proxySettings"].(map[string]any)
		stream, _ := outbound["streamSettings"].(map[string]any)
		sockopt, _ := stream["sockopt"].(map[string]any)
		for _, dependency := range []any{proxy["tag"], sockopt["dialerProxy"]} {
			if dependency == nil {
				continue
			}
			dep, ok := dependency.(string)
			if !ok || dep == "" {
				return i18n.New("链式出站标识无效", "Invalid chained outbound tag")
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[name], visited[name] = false, true
		return nil
	}
	if err := visit(tag); err != nil {
		return nil, "", err
	}
	encoded, _ := json.Marshal(selected)
	draft.Outbounds, draft.Rules, draft.Balancers = string(encoded), "[]", "[]"
	var advanced map[string]any
	_ = json.Unmarshal([]byte(draft.Advanced), &advanced)
	delete(advanced, "observatory")
	delete(advanced, "burstObservatory")
	encoded, _ = json.Marshal(advanced)
	draft.Advanced = string(encoded)
	encoded, _ = json.Marshal(draft)
	config, err := compileSettings(encoded)
	if err != nil {
		return nil, "", err
	}
	target, _ := url.Parse(draft.ProbeURL)
	port := 80
	if target.Scheme == "https" {
		port = 443
	}
	if text := target.Port(); text != "" {
		port, err = strconv.Atoi(text)
	}
	if err != nil || port < 1 || port > 65535 {
		return nil, "", i18n.New("测试URL端口无效", "Invalid test URL port")
	}
	config["log"] = map[string]any{"loglevel": "none", "access": "none"}
	delete(config, "stats")
	delete(config, "policy")
	config["inbounds"] = []any{map[string]any{
		"tag": "outbound-probe", "listen": socket, "protocol": "dokodemo-door",
		"settings": map[string]any{"address": target.Hostname(), "port": port, "network": "tcp"},
	}}
	config["routing"] = map[string]any{"domainStrategy": "AsIs", "rules": []any{map[string]any{
		"type": "field", "inboundTag": []string{"outbound-probe"}, "outboundTag": tag,
	}}}
	return config, target.String(), nil
}

func probeHTTPRequest(ctx context.Context, target, socket string) (int64, error) {
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 32 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, i18n.New("测试URL无效", "Invalid test URL")
	}
	started := time.Now()
	response, err := client.Do(request)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		var certificateError *tls.CertificateVerificationError
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, i18n.New("连接测试超时", "Connection test timed out")
		}
		if errors.As(err, &certificateError) {
			return 0, i18n.New("测试目标的TLS证书验证失败", "Test target TLS certificate verification failed")
		}
		return 0, i18n.New("连接测试失败，请检查出站地址、认证和网络", "Connection test failed; check outbound address, authentication and network")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return 0, fmt.Errorf(i18n.T("测试目标返回HTTP %d", "Test target returned HTTP %d"), response.StatusCode)
	}
	return elapsed, nil
}

func (rt *Runtime) probeOutbound(ctx context.Context, data json.RawMessage, tag string) (int64, int, error) {
	if !rt.probeMu.TryLock() {
		return 0, 429, i18n.New("已有出站连接测试进行中，请稍后重试", "An outbound connection test is already running; retry shortly")
	}
	defer rt.probeMu.Unlock()
	select {
	case <-rt.done:
		return 0, 503, i18n.New("面板正在关闭，无法启动连接测试", "Panel is shutting down; cannot start a connection test")
	default:
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	go func() {
		select {
		case <-rt.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	// 短路径兼容Unix套接字长度限制；目录0700，配置0600，测试结束即清理。
	directory, err := os.MkdirTemp("/tmp", "oup-")
	if err != nil {
		return 0, 500, i18n.New("无法创建连接测试临时目录", "Could not create the connection test temporary directory")
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "probe.sock")
	config, target, err := outboundProbeConfig(data, tag, socket)
	if err != nil {
		return 0, 400, err
	}
	version := rt.manager.Selected()
	binary, err := rt.manager.Binary(version)
	if err != nil {
		return 0, 503, i18n.New("请先安装并选择可用的Xray核心", "Install and select an available Xray core first")
	}
	encoded, _ := json.Marshal(config)
	file := filepath.Join(directory, "config.json")
	if os.WriteFile(file, encoded, 0600) != nil {
		return 0, 500, i18n.New("无法写入连接测试临时配置", "Could not write the connection test temporary configuration")
	}
	var testEngine engine.Engine
	defer testEngine.Close()
	if testEngine.Apply(ctx, engine.Candidate{Binary: binary, Version: version, File: file, Inbounds: 1}) != nil {
		return 0, 400, i18n.New("测试配置预检或启动失败，请检查出站参数", "Test configuration preflight or startup failed; check outbound parameters")
	}
	elapsed, err := probeHTTPRequest(ctx, target, socket)
	if err != nil {
		return 0, 502, err
	}
	return elapsed, 200, nil
}

func registerOutboundProbe(mux *http.ServeMux, rt *Runtime) {
	mux.HandleFunc("POST /api/xray/outbound-test", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		var request struct {
			Data json.RawMessage `json:"data"`
			Tag  string          `json:"tag"`
		}
		if !readJSON(w, r, &request, 2<<20) {
			return
		}
		elapsed, status, err := rt.probeOutbound(r.Context(), request.Data, request.Tag)
		if err != nil {
			respond(w, status, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]any{"latencyMs": elapsed})
	})
}
