package localapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

func historyFixture(t *testing.T, rt *Runtime) (appliedSnapshot, store.Document) {
	t.Helper()
	dir, err := os.MkdirTemp(rt.root, "candidate-")
	if err != nil {
		t.Fatal(err)
	}
	cert, key := runtimeCertificate(t, dir)
	c := engine.Candidate{Version: "26.7.28", WorkspaceRevision: 1, SettingsRevision: 2, Inbounds: 1, File: filepath.Join(dir, "config.json"), PolicyFile: filepath.Join(dir, "policy.json")}
	data, _ := json.Marshal(map[string]any{"inbounds": []config.CompiledInbound{{Tag: "in-one", Listen: "127.0.0.1", Stream: config.StreamSettings{TLS: &config.TLSSettings{Certificates: []config.Certificate{{File: cert, Key: key}}}}, Settings: config.ProtocolSettings{Clients: []config.User{{Email: "client-a"}, {Email: "client-b"}}}}}})
	if err = writePrivate(c.File, data); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(-time.Minute).UnixMilli()
	data, _ = json.Marshal(runtimePolicy{Clients: map[string]clientLimit{"a": {ExpiresAt: &expiry}, "b": {}}})
	if err = writePrivate(c.PolicyFile, data); err != nil {
		t.Fatal(err)
	}
	record, err := rt.snapshot(c)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(record)
	doc, err := rt.state.SaveDocument(context.Background(), appliedDocument, 0, data)
	if err != nil {
		t.Fatal(err)
	}
	return record, doc
}

func TestAppliedHistoryReopenAndPrivateSummary(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := t.TempDir()
	rt, err := NewRuntime(nil, s, root)
	if err != nil {
		t.Fatal(err)
	}
	record, doc := historyFixture(t, rt)
	if err = rt.Close(); err != nil {
		t.Fatal(err)
	}
	rt, err = NewRuntime(nil, s, root)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	summary := rt.historySummary(context.Background())
	if !summary.Available || summary.Revision != doc.Revision || summary.Version != "26.7.28" || summary.WorkspaceRevision != 1 || summary.SettingsRevision != 2 || rt.engine.Status().Running {
		t.Fatal("history lost or auto-started")
	}
	visible, _ := json.Marshal(summary)
	if strings.Contains(string(visible), root) || strings.Contains(string(visible), "candidate") || strings.Contains(string(visible), "files") {
		t.Fatal("private snapshot leaked")
	}
	full, changed, err := rt.filtered(context.Background(), record.Candidate, time.Now())
	var applied []config.CompiledInbound
	if err != nil || !changed || json.Unmarshal(full["inbounds"], &applied) != nil || len(applied) != 1 || len(applied[0].Settings.Clients) != 1 || applied[0].Settings.Clients[0].Email != "client-b" {
		t.Fatal("history ignored expiry", err)
	}
	ledger := traffic.Empty()
	ledger.Totals["b"] = traffic.Count{Uplink: 10}
	data, _ := json.Marshal(ledger)
	if _, err = s.SaveDocument(context.Background(), "traffic", 1, data); err != nil {
		t.Fatal(err)
	}
	if _, err = rt.resumeCandidate(context.Background(), doc.Revision+1); err != store.ErrConflict {
		t.Fatal("stale history accepted", err)
	}
}

func TestAppliedHistoryRejectsChangedOrUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"config", "policy", "certificate", "permission", "symlink", "directory-symlink", "pending", "format", "outside", "missing-hash"} {
		t.Run(kind, func(t *testing.T) {
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			rt, err := NewRuntime(nil, s, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			record, doc := historyFixture(t, rt)
			switch kind {
			case "config":
				err = os.WriteFile(record.Candidate.File, []byte("{}"), 0600)
			case "policy":
				err = os.Rename(record.Candidate.PolicyFile, record.Candidate.PolicyFile+".moved")
			case "certificate":
				for path := range record.Files {
					if strings.HasSuffix(path, "cert.pem") {
						err = os.WriteFile(path, []byte("changed"), 0600)
					}
				}
			case "permission":
				err = os.Chmod(record.Candidate.File, 0644)
			case "symlink":
				err = os.Rename(record.Candidate.File, record.Candidate.File+".moved")
				if err == nil {
					err = os.Symlink(record.Candidate.File+".moved", record.Candidate.File)
				}
			case "directory-symlink":
				dir := filepath.Dir(record.Candidate.File)
				err = os.Rename(dir, dir+".moved")
				if err == nil {
					err = os.Symlink(dir+".moved", dir)
				}
			case "pending":
				record.Pending = true
			case "format":
				record.Format = 999
			case "outside":
				record.Files[filepath.Join(t.TempDir(), "outside")] = strings.Repeat("0", 64)
			case "missing-hash":
				delete(record.Files, record.Candidate.File)
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(record)
			if _, err = s.SaveDocument(context.Background(), appliedDocument, doc.Revision, data); err != nil {
				t.Fatal(err)
			}
			summary := rt.historySummary(context.Background())
			if summary.Available || summary.Issue == "" || strings.Contains(summary.Issue, rt.root) {
				t.Fatal("unsafe history accepted or private path leaked")
			}
		})
	}
}

func TestResumeAPIAuthenticationAndConfirmation(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt, err := NewRuntime(nil, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	session, err := s.Initialize(context.Background(), "historyqa", "history-api-test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(nil, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	for _, item := range []struct {
		path, method, origin, body string
		auth                       bool
		want                       int
	}{
		{"history", "GET", "", "", false, 401}, {"resume", "POST", "http://127.0.0.1:5180", "{}", false, 401},
		{"resume-preflight", "POST", "http://127.0.0.1:5180", "{}", false, 401},
		{"history", "GET", "https://evil.test", "", true, 403},
		{"resume", "POST", "http://127.0.0.1:5180", "{}", true, 409},
		{"resume", "POST", "http://127.0.0.1:5180", `{"revision":0,"confirmation":"确认恢复上次应用"}`, true, 422},
		{"history", "GET", "", "", true, 200},
	} {
		r := httptest.NewRequest(item.method, "http://127.0.0.1:5180/api/runtime/"+item.path, strings.NewReader(item.body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", item.origin)
		r.Header.Set("Content-Type", "application/json")
		if item.auth {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != item.want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("history API boundary", item.path, w.Code, item.want)
		}
	}
}

func TestAutoResumeIntentFollowsStop(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	root := t.TempDir()
	rt, err := NewRuntime(m, s, root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := rt.ResumeOnStart(ctx); started || err != nil {
		t.Fatal("resumed without running intent", err)
	}
	historyFixture(t, rt)
	rt.mu.Lock()
	err = rt.setAutoResume(ctx, true)
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.Close(); err != nil {
		t.Fatal(err)
	}
	// 面板退出保留标记；精确核心缺失时按原校验失败并保持停止，标记留待下次启动。
	rt, err = NewRuntime(m, s, root)
	if err != nil {
		t.Fatal(err)
	}
	started, err := rt.ResumeOnStart(ctx)
	if started || err == nil || !strings.Contains(err.Error(), "精确官方核心缺失") || rt.engine.Status().Running || !rt.autoResume.resume || !strings.Contains(rt.issue, "自动恢复") {
		t.Fatal("failed auto resume started core or lost intent", err)
	}
	session, err := s.Initialize(ctx, "resumeqa", "auto-resume-test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:5180/api/runtime/stop", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Origin", "http://127.0.0.1:5180")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
	w := httptest.NewRecorder()
	Handler(nil, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("manual stop failed", w.Code)
	}
	if err = rt.Close(); err != nil {
		t.Fatal(err)
	}
	doc, err := s.ReadDocument(ctx, autoResumeDocument)
	if err != nil || string(doc.Data) != `{"resume":false}` {
		t.Fatal("manual stop kept auto resume intent", err)
	}
	rt, err = NewRuntime(m, s, root)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := rt.ResumeOnStart(ctx); started || err != nil || rt.engine.Status().Running {
		t.Fatal("manually stopped core resumed on start", err)
	}
	if err = rt.Close(); err != nil {
		t.Fatal(err)
	}
	// 损坏或含未知字段的标记按不恢复处理，不阻止面板启动。
	if _, err = s.SaveDocument(ctx, autoResumeDocument, doc.Revision, json.RawMessage(`{"resume":true,"extra":1}`)); err != nil {
		t.Fatal(err)
	}
	rt, err = NewRuntime(m, s, root)
	if err != nil {
		t.Fatal("corrupted intent blocked startup", err)
	}
	defer rt.Close()
	if rt.autoResume.resume {
		t.Fatal("corrupted intent accepted")
	}
}

func TestOfficialHistoryExpiredCertificate(t *testing.T) {
	root := os.Getenv("PANEL_RUNTIME_QA_CORE")
	if root == "" {
		t.Skip("设置PANEL_RUNTIME_QA_CORE后验证恢复前的证书过期拒绝")
	}
	m, err := core.NewManager(root)
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
	defer rt.Close()
	record, doc := historyFixture(t, rt)
	dir := filepath.Dir(record.Candidate.File)
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || pair.Leaf == nil {
		t.Fatal("invalid test certificate", err)
	}
	pair.Leaf.NotBefore, pair.Leaf.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, pair.Leaf, pair.Leaf, pair.PrivateKey.(crypto.Signer).Public(), pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	// 摘要仍正确，须因有效期拒绝，而不是只测试文件损坏。
	record, err = rt.snapshot(record.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(record)
	doc, err = s.SaveDocument(context.Background(), appliedDocument, doc.Revision, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.resumeCandidate(context.Background(), doc.Revision); err == nil || !strings.Contains(err.Error(), "证书无效或已过期") || rt.engine.Status().Running {
		t.Fatal("expired historical certificate accepted", err)
	}
}

func TestResumeHistoryRequiresExactInstalledCore(t *testing.T) {
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
	defer rt.Close()
	_, doc := historyFixture(t, rt)
	if _, err := rt.resumeCandidate(context.Background(), doc.Revision); err == nil || !strings.Contains(err.Error(), "精确官方核心缺失或校验失败") || rt.engine.Status().Running {
		t.Fatal("missing exact official core accepted", err)
	}
}
