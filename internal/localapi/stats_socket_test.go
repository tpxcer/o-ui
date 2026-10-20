package localapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

func TestRuntimeCloseRemovesStatsDirectory(t *testing.T) {
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if rt.statsSocket, err = newStatsAddress(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(rt.statsSocket)
	if err = os.WriteFile(rt.statsSocket, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		_ = os.RemoveAll(dir)
		t.Fatal("面板关闭后应删除本进程统计套接字目录", err)
	}
}

func TestPubliclyTrustedCertificate(t *testing.T) {
	now := time.Now()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "测试CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), DNSNames: []string{"node.example.test"}, IPAddresses: []net.IP{net.ParseIP("192.0.2.10")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if !publiclyTrusted(chain, "node.example.test", roots) || !publiclyTrusted(chain, "192.0.2.10", roots) {
		t.Fatal("受信根签发且名称匹配应判为受信")
	}
	if publiclyTrusted(chain, "other.example.test", roots) || publiclyTrusted(chain, "node.example.test", x509.NewCertPool()) || publiclyTrusted(chain, "node.example.test", nil) || publiclyTrusted([]byte("bad"), "node.example.test", roots) {
		t.Fatal("名称不符、私有CA未被系统信任或无效证书应判为不受信")
	}
}
