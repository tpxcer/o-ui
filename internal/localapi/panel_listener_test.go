package localapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPanelHTTPSRedirect(t *testing.T) {
	for _, test := range []struct {
		name, method, target, host, location string
		status                               int
	}{
		{"page", "GET", "/demo/panel/", "panel.example:1266", "https://panel.example:1266/demo/panel/", 308},
		{"head", "HEAD", "/demo/clients/?q=a%2Fb", "panel.example:1266", "https://panel.example:1266/demo/clients/?q=a%2Fb", 308},
		{"escaped", "GET", "/demo/a%2Fb?q=1", "panel.example:1266", "https://panel.example:1266/demo/a%2Fb?q=1", 308},
		{"authority-path", "GET", "//evil.example/", "panel.example:1266", "https://panel.example:1266//evil.example/", 308},
		{"wrong-host", "GET", "/demo/panel/", "evil.example", "", 403},
		{"absolute-target", "GET", "http://evil.example/demo/", "panel.example:1266", "", 400},
		{"login", "POST", "/demo/api/auth/login", "panel.example:1266", "", 426},
		{"write", "PUT", "/demo/api/workspace", "panel.example:1266", "", 426},
		{"connect", "CONNECT", "/demo/", "panel.example:1266", "", 426},
	} {
		t.Run(test.name, func(t *testing.T) {
			access := &PanelAccess{Data: PanelAccessDraft{Public: true, Listen: "0.0.0.0:1266", Origin: "https://panel.example:1266", BasePath: "/demo/"}}
			handler := panelHTTPSRedirect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("plaintext reached application") }), access)
			r := httptest.NewRequest(test.method, test.target, nil)
			r.Host = test.host
			r.Header.Set("X-Forwarded-Host", "evil.example")
			r.Header.Set("X-Forwarded-Proto", "https")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status || w.Header().Get("Location") != test.location {
				t.Fatal("unexpected plaintext response", w.Code, w.Header())
			}
			if len(w.Result().Cookies()) != 0 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Connection") != "close" {
				t.Fatal("plaintext response must not issue sessions or stay open")
			}
		})
	}
	t.Run("local-only", func(t *testing.T) {
		access := &PanelAccess{Data: PanelAccessDraft{Listen: "127.0.0.1:1266"}}
		handler := panelHTTPSRedirect(http.NotFoundHandler(), access)
		for _, remote := range []string{"127.0.0.1:3000", "192.0.2.1:3000"} {
			r := httptest.NewRequest("GET", "/panel/", nil)
			r.Host, r.RemoteAddr = "127.0.0.1:1266", remote
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if remote == "127.0.0.1:3000" && (w.Code != 308 || w.Header().Get("Location") != "https://127.0.0.1:1266/panel/") || remote != "127.0.0.1:3000" && w.Code != 403 {
				t.Fatal("local boundary changed", remote, w.Code)
			}
		}
	})
}

func TestPanelProtocolListener(t *testing.T) {
	cert, key := runtimeCertificate(t, t.TempDir(), "127.0.0.1")
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	access := &PanelAccess{Data: PanelAccessDraft{Public: true, Listen: address, Origin: "https://" + address}}
	var requests atomic.Int32
	server := &http.Server{ReadHeaderTimeout: time.Second, TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}}
	server.Handler = panelHTTPSRedirect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Error("plaintext application request")
		}
		requests.Add(1)
		_, _ = io.WriteString(w, "secure-panel")
	}), access)
	mixed := newPanelProtocolListener(listener, server.TLSConfig.Clone())
	done := make(chan error, 1)
	go func() { done <- server.Serve(mixed) }()
	defer server.Close()
	slow, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	plain := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := plain.Get("http://" + address + "/demo/panel/?q=1")
	if err != nil {
		t.Fatal("idle connection blocked redirect", err)
	}
	response.Body.Close()
	if response.StatusCode != 308 || response.Header.Get("Location") != "https://"+address+"/demo/panel/?q=1" || requests.Load() != 0 {
		t.Fatal("plaintext was not redirected separately")
	}
	pool := x509.NewCertPool()
	data, _ := os.ReadFile(cert)
	if !pool.AppendCertsFromPEM(data) {
		t.Fatal("test CA missing")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	secure := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err = secure.Get("https://" + address + "/demo/panel/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "secure-panel" || response.ProtoMajor != 2 || requests.Load() != 1 {
		t.Fatal("TLS/HTTP2 handling changed", response.Proto, string(body), requests.Load())
	}
	response, err = plain.Post("http://"+address+"/api/auth/login", "application/json", strings.NewReader(`{"username":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 426 || requests.Load() != 1 || len(response.Cookies()) != 0 {
		t.Fatal("plaintext login was processed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal("shutdown failed", err)
	}
	if err := <-done; err != http.ErrServerClosed {
		t.Fatal("serve did not stop", err)
	}
	_ = slow.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := slow.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle classification connection survived shutdown")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("idle connection was left open")
	}
}
