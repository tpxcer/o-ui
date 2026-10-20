package localapi

import (
	"encoding/json"
	"independent-panel/internal/i18n"
	"net"

	"independent-panel/internal/config"
	"independent-panel/internal/engine"
)

const proxyConfirmation = "确认开放代理监听"

func (rt *Runtime) nonLoopbackProxy(c engine.Candidate) (bool, error) {
	data, err := rt.snapshotFile(c.File)
	var full struct {
		Inbounds []config.CompiledInbound `json:"inbounds"`
	}
	if err != nil || json.Unmarshal(data, &full) != nil || full.Inbounds == nil || len(full.Inbounds) != c.Inbounds {
		return false, i18n.New("无法核对候选代理监听范围，未启动核心", "Cannot check the candidate proxy listen scope; core not started")
	}
	nonLoopback := false
	for _, in := range full.Inbounds {
		ip := net.ParseIP(in.Listen)
		if ip == nil {
			return false, i18n.New("候选代理监听IP无效，未启动核心", "Candidate proxy listen IP is invalid; core not started")
		}
		nonLoopback = nonLoopback || !ip.IsLoopback()
	}
	return nonLoopback, nil
}

func (rt *Runtime) confirmProxy(c engine.Candidate, confirmation string) error {
	nonLoopback, err := rt.nonLoopbackProxy(c)
	if err != nil {
		return err
	}
	if nonLoopback && confirmation != proxyConfirmation {
		return i18n.New("非回环代理监听需要额外明确确认；管理面板仍仅本机访问", "Non-loopback proxy listening needs an extra explicit confirmation; the management panel stays local")
	}
	return nil
}
