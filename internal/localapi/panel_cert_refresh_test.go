package localapi

import (
	"context"
	"os"
	"testing"

	"independent-panel/internal/config"
	"independent-panel/internal/store"
)

// 已应用快照里复用面板证书的TLS入站，恢复时换成当前面板证书；手动证书、已是当前证书、
// 面板证书不覆盖原域名或面板没有启用HTTPS时保持原样，快照文件本身不改。
func TestRefreshPanelCertificates(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	workspace := []byte(`{"inbounds":[{"id":"one","preset":"hy2","connection":{"usePanelCertificate":true}},{"id":"two","preset":"xhttp-tls","connection":{"certificateFile":"/manual/cert.pem","keyFile":"/manual/key.pem"}}],"clients":[]}`)
	if _, err = s.SaveDocument(ctx, "workspace", 0, workspace); err != nil {
		t.Fatal(err)
	}
	oldCert, oldKey := runtimeCertificate(t, t.TempDir())
	manualCert, manualKey := runtimeCertificate(t, t.TempDir())
	panelCert, panelKey := runtimeCertificate(t, t.TempDir())
	p, err := loadPanelPair(panelTLSDraft{panelCert, panelKey})
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{state: s, root: t.TempDir(), panelTLS: p}
	snapshot := func(first string) []config.CompiledInbound {
		return []config.CompiledInbound{
			{Tag: "in-one", Stream: config.StreamSettings{TLS: &config.TLSSettings{Certificates: []config.Certificate{{File: first, Key: oldKey}}}}},
			{Tag: "in-two", Stream: config.StreamSettings{TLS: &config.TLSSettings{Certificates: []config.Certificate{{File: manualCert, Key: manualKey}}}}},
		}
	}
	dir := ""
	resumeDir := func() (string, error) {
		if dir == "" {
			created, err := os.MkdirTemp(rt.root, "resume-")
			if err != nil {
				return "", err
			}
			dir = created
		}
		return dir, nil
	}

	inbounds := snapshot(oldCert)
	if n, err := rt.refreshPanelCertificates(ctx, inbounds, resumeDir); err != nil || n != 1 {
		t.Fatal("one panel-certificate inbound should be refreshed", n, err)
	}
	used := inbounds[0].Stream.TLS.Certificates[0]
	cert, _ := os.ReadFile(used.File)
	key, _ := os.ReadFile(used.Key)
	if string(cert) != string(p.cert) || string(key) != string(p.key) || dir == "" || used.File[:len(dir)] != dir {
		t.Fatal("refreshed inbound must use a private copy of the current panel certificate", used)
	}
	if inbounds[1].Stream.TLS.Certificates[0].File != manualCert {
		t.Fatal("manual certificates must stay unchanged")
	}
	if original, _ := os.ReadFile(oldCert); string(original) == string(p.cert) {
		t.Fatal("the snapshot certificate file itself must not be rewritten")
	}

	inbounds = snapshot(panelCert)
	inbounds[0].Stream.TLS.Certificates[0].Key = panelKey
	if n, _ := rt.refreshPanelCertificates(ctx, inbounds, resumeDir); n != 0 {
		t.Fatal("an inbound already on the current panel certificate needs no copy")
	}

	otherCert, otherKey := runtimeCertificate(t, t.TempDir(), "other.test")
	if rt.panelTLS, err = loadPanelPair(panelTLSDraft{otherCert, otherKey}); err != nil {
		t.Fatal(err)
	}
	inbounds = snapshot(oldCert)
	if n, _ := rt.refreshPanelCertificates(ctx, inbounds, resumeDir); n != 0 || inbounds[0].Stream.TLS.Certificates[0].File != oldCert {
		t.Fatal("a panel certificate for other names must not replace the snapshot")
	}

	rt.panelTLS = nil
	inbounds = snapshot(oldCert)
	if n, err := rt.refreshPanelCertificates(ctx, inbounds, resumeDir); err != nil || n != 0 {
		t.Fatal("without panel HTTPS nothing is refreshed", n, err)
	}
}
