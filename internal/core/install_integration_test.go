package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 显式验收入口：只允许新目录，不覆盖既有核心或自动清理文件。
func TestOfficialCoreInstallation(t *testing.T) {
	root := os.Getenv("PANEL_INSTALL_QA_DIR")
	if root == "" {
		t.Skip("设置PANEL_INSTALL_QA_DIR为全新绝对路径后验证官方下载")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("requires new absolute directory")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("refuses existing or unavailable directory", err)
	}
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, version := range []string{"26.3.27", DefaultVersion} {
		if err := m.Install(ctx, version); err != nil {
			t.Fatal("official install failed", version, err)
		}
		if _, err := m.Binary(version); err != nil {
			t.Fatal("installed binary validation failed", version, err)
		}
	}
	if status := m.Status(); status.Selected != DefaultVersion || len(status.Installed) != 2 || status.Runtime != "not-configured" {
		t.Fatal("installed versions or fixed selection mismatch")
	}
	t.Log("两个官方版本下载与校验通过；固定默认保留，未启动代理")
}

func TestOfficialCandidateCoreInstallation(t *testing.T) {
	root := os.Getenv("PANEL_CANDIDATE_QA_DIR")
	if root == "" {
		t.Skip("设置PANEL_CANDIDATE_QA_DIR为全新绝对路径后验证获准候选26.10.17")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("requires new absolute directory")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("refuses existing or unavailable directory", err)
	}
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := m.Install(ctx, "26.10.17"); err != nil {
		t.Fatal("official candidate install failed", err)
	}
	if _, err := m.Binary("26.10.17"); err != nil {
		t.Fatal("candidate binary validation failed", err)
	}
	status := m.Status()
	if status.DefaultVersion != DefaultVersion || status.Selected != "26.10.17" || len(status.Installed) != 1 || status.Runtime != "not-configured" {
		t.Fatal("candidate directory or unchanged product default mismatch")
	}
	t.Log("仅临时目录安装并校验官方26.10.17，产品默认保留；未启动代理")
}
