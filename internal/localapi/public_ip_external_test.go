package localapi

import (
	"context"
	"net"
	"os"
	"testing"
)

// 显式开启才向ipinfo.io实际查询；不打印本机出口地址。
func TestPublicInfoExternal(t *testing.T) {
	if os.Getenv("PANEL_PUBLIC_IP_EXTERNAL_QA") != "1" {
		t.Skip("设置PANEL_PUBLIC_IP_EXTERNAL_QA=1后才实际查询外部公网信息服务")
	}
	ctx, cancel := context.WithTimeout(context.Background(), publicLookupLimit)
	defer cancel()
	v, err := fetchIPInfo(ctx, "tcp4")
	if err != nil {
		t.Fatal("IPv4公网信息查询失败", err)
	}
	if ip := net.ParseIP(v.IP); ip == nil || ip.To4() == nil || v.Location == "" || v.Org == "" {
		t.Fatal("IPv4公网信息字段不完整")
	}
	if _, err = fetchIPInfo(ctx, "tcp6"); err != nil {
		t.Log("本机无可用IPv6出口或查询失败，不视为失败")
	}
}
