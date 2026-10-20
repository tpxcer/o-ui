package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 无网客体只重放宿主从官方取得的原始元数据和完整包；摘要/解压/版本探测仍用原安装器。
func TestOfficialStagedInstallation(t *testing.T) {
	input, root := os.Getenv("PANEL_STAGED_QA_INPUT"), os.Getenv("PANEL_STAGED_QA_DIR")
	if input == "" || root == "" {
		t.Skip("设置PANEL_STAGED_QA_INPUT与全新PANEL_STAGED_QA_DIR后验证官方离线缓存")
	}
	if !filepath.IsAbs(input) || !filepath.IsAbs(root) {
		t.Fatal("QA inputs must be absolute")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("refuses existing QA core directory", err)
	}
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		var selected string
		for _, version := range []string{"26.3.27", DefaultVersion} {
			if r.URL.String() == apiBase+"/releases/tags/v"+version {
				selected = filepath.Join(input, version+".json")
			}
			if r.URL.String() == repository+"/releases/download/v"+version+"/"+m.asset {
				selected = filepath.Join(input, version+"-"+m.asset)
			}
		}
		if selected == "" {
			return nil, errors.New("QA unexpected URL, networking disabled")
		}
		data, err := os.ReadFile(selected)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, version := range []string{"26.3.27", DefaultVersion} {
		if err := m.Install(ctx, version); err != nil {
			t.Fatal("official staged installation failed", version, err)
		}
		if _, err := m.Binary(version); err != nil {
			t.Fatal("official staged core failed validation", err)
		}
	}
	if status := m.Status(); status.Selected != DefaultVersion || len(status.Installed) != 2 {
		t.Fatal("staged core versions/selection mismatch")
	}
}
