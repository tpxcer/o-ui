package core

import (
	"context"
	"testing"
)

func TestLatestStableIgnoresPrereleaseAndOrder(t *testing.T) {
	releases := []Release{{Version: "26.10.17", Prerelease: true}, {Version: "26.3.27"}, {Version: "26.7.28", Prerelease: true}, {Version: "25.12.8"}, {Version: "26.3.9"}}
	if v, err := LatestStable(releases); err != nil || v != "26.3.27" {
		t.Fatal("latest stable mismatch", v, err)
	}
	if _, err := LatestStable([]Release{{Version: "26.10.17", Prerelease: true}}); err == nil {
		t.Fatal("prerelease-only list accepted")
	}
	if _, err := LatestStable(nil); err == nil {
		t.Fatal("empty list accepted")
	}
}

// o-ui统计接口用Unix套接字，v26.5.3之前的官方核心启动即退出：不允许安装或选择。
func TestSupportedMinimumVersion(t *testing.T) {
	for version, want := range map[string]bool{"26.3.27": false, "26.4.25": false, "26.5.3": true, "26.7.28": true, "26.10.31": true, "27.1.1": true, "25.12.8": false, "bad": false} {
		if Supported(version) != want {
			t.Fatal("supported mismatch", version)
		}
	}
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	before := m.Status().Selected
	if err := m.Install(context.Background(), "v26.3.27"); err != errUnsupported || m.Status().Selected != before {
		t.Fatal("unsupported version must be refused before any download or selection", err)
	}
}
