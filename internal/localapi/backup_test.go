package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

func TestBusinessBackupAPI(t *testing.T) {
	ctx := context.Background()
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	stateRoot := t.TempDir()
	s, err := store.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	session, err := s.Initialize(ctx, "tester", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	call := func(action string, body any, authenticated bool, origin string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/backup/"+action, strings.NewReader(string(data)))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	origin := "http://127.0.0.1:5180"
	password := "isolated-backup-passphrase-924"
	for _, action := range []string{"export", "preview", "restore"} {
		if w := call(action, map[string]string{"password": password}, false, origin); w.Code != 401 {
			t.Fatal("unauth", action, w.Code)
		}
		if w := call(action, map[string]string{"password": password}, true, "https://evil.test"); w.Code != 403 {
			t.Fatal("origin", action, w.Code)
		}
	}
	cert, key := runtimeCertificate(t, t.TempDir())
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	draft.Inbounds[0].Preset = "xhttp-tls"
	draft.Inbounds[0].Listen, draft.Inbounds[0].ShareHost = "::", "proxy.example.test"
	draft.Inbounds[0].Connection = &connectionDraft{CertificateFile: cert, KeyFile: key, ServerName: "runtime-qa.test", Path: "/test", Mode: "auto"}
	data, _ := json.Marshal(draft)
	if _, err = s.SaveDocument(ctx, "workspace", 0, data); err != nil {
		t.Fatal(err)
	}
	if _, err = rt.credentials(ctx, draft); err != nil {
		t.Fatal(err)
	}
	ledger := traffic.Empty()
	ledger.Totals["client-one"] = traffic.Count{Uplink: 101, Downlink: 202}
	ledger, err = traffic.Merge(ledger, "backup-fixture", map[string]traffic.Count{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ledger.Active = false
	data, _ = json.Marshal(ledger)
	if _, err = s.SaveDocument(ctx, "traffic", 0, data); err != nil {
		t.Fatal(err)
	}
	before, _ := s.ReadDocuments(ctx, backupDocuments)
	// 旧运行历史不进入业务包，恢复时必须同事务失效，不能重新启用旧用户。
	if _, err = s.SaveDocument(ctx, appliedDocument, 0, json.RawMessage(`{"format":1,"pending":true}`)); err != nil {
		t.Fatal(err)
	}
	before, _ = s.ReadDocuments(ctx, backupDocuments)
	// 业务包不带链接密钥；恢复后换代，旧链接不随相同业务身份重新生效。
	grants := subscriptionState{Version: 1, Generation: 1, Clients: map[string]subscriptionToken{"client-one": {Hash: strings.Repeat("a", 64), Generation: 1}}}
	grantData, _ := json.Marshal(grants)
	if _, err = s.SaveDocument(ctx, subscriptionsDocument, 0, grantData); err != nil {
		t.Fatal(err)
	}
	before, _ = s.ReadDocuments(ctx, backupDocuments)
	w := call("export", map[string]string{"password": password}, true, origin)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("export", w.Code, w.Body.String())
	}
	var exported struct{ Archive, Filename string }
	_ = json.Unmarshal(w.Body.Bytes(), &exported)
	request := restoreRequest{Archive: exported.Archive, Password: password}
	b, file, err := openBackup(request)
	if err != nil {
		t.Fatal(err)
	}
	if b.Traffic.Totals["client-one"].Total() != 303 || len(b.Certificates) != 1 || b.Workspace.Inbounds[0].Connection.CertificateFile != "" || b.Workspace.Inbounds[0].Listen != "::" || b.Workspace.Inbounds[0].ShareHost != "proxy.example.test" {
		t.Fatal("missing data/path")
	}
	plain, _ := json.Marshal(b)
	if strings.Contains(string(plain), strings.Repeat("a", 64)) || strings.Contains(string(plain), `"subscriptions"`) {
		t.Fatal("subscription grants included in business backup")
	}
	if strings.Contains(w.Body.String(), "password_hash") || strings.Contains(string(file), b.Identities.Clients["client-one"].Auth) {
		t.Fatal("sensitive output")
	}
	for _, bad := range []restoreRequest{{Archive: request.Archive, Password: "incorrect-backup-passphrase"}, {Archive: "invalid", Password: password}, {Archive: request.Archive}} {
		if w = call("preview", bad, true, origin); w.Code != 422 {
			t.Fatal("invalid preview", w.Code)
		}
	}
	// 口令可选（用户2026-10-22要求）：不填口令导出不加密的.json，不填口令即可校验。
	var plainExport struct{ Archive, Filename string }
	if w = call("export", map[string]string{"password": ""}, true, origin); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &plainExport) != nil || !strings.HasSuffix(plainExport.Filename, ".json") {
		t.Fatal("plain export", w.Code, plainExport.Filename)
	}
	if pb, _, err := openBackup(restoreRequest{Archive: plainExport.Archive}); err != nil || pb.Traffic.Totals["client-one"].Total() != 303 || len(pb.Certificates) != 1 {
		t.Fatal("plain backup should open without a password", err)
	}
	unchanged, _ := s.ReadDocuments(ctx, backupDocuments)
	a, _ := json.Marshal(before)
	after, _ := json.Marshal(unchanged)
	if string(a) != string(after) {
		t.Fatal("failed preview modified documents")
	}
	w = call("preview", request, true, origin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var preview backupPreview
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	request.Fingerprint, request.Confirmation = preview.Fingerprint, "确认覆盖并恢复"
	// 预览后修改任何相关文档，恢复必须拒绝，不覆盖新用量。
	ledger.Totals["client-one"] = traffic.Count{Uplink: 999, Downlink: 1000}
	data, _ = json.Marshal(ledger)
	if _, err = s.SaveDocument(ctx, "traffic", 1, data); err != nil {
		t.Fatal(err)
	}
	if w = call("restore", request, true, origin); w.Code != 409 {
		t.Fatal("stale preview", w.Code)
	}
	// 包内路径不参与提取，未知版本、缺失身份与错误证书在解密后仍拒绝。
	for _, mutate := range []func(*businessBackup){
		func(b *businessBackup) { b.Version = 999 },
		func(b *businessBackup) { b.Workspace.Inbounds[0].Connection.KeyFile = "/tmp/should-not-write" },
		func(b *businessBackup) { delete(b.Identities.Clients, "client-one") },
		func(b *businessBackup) { b.Certificates["../escape"] = b.Certificates["in-one"] },
		func(b *businessBackup) { b.Traffic.Totals = nil },
	} {
		bad, _, _ := openBackup(restoreRequest{Archive: exported.Archive, Password: password})
		mutate(&bad)
		if validateBackup(bad) == nil {
			t.Fatal("malicious backup accepted")
		}
	}
	// 当前额外记录在预览中明确列出；没有确认不能恢复/批量移除。
	draft.Inbounds = append(draft.Inbounds, inboundDraft{ID: "extra-in", Name: "额外入站", Preset: "reality", Port: 20443})
	draft.Clients = append(draft.Clients, clientDraft{ID: "extra-client", InboundID: "extra-in", Name: "额外客户端"})
	data, _ = json.Marshal(draft)
	if _, err = s.SaveDocument(ctx, "workspace", 1, data); err != nil {
		t.Fatal(err)
	}
	w = call("preview", request, true, origin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	if len(preview.RemovedClients) != 1 || len(preview.RemovedInbounds) != 1 || len(preview.ReplacedClients) != 1 {
		t.Fatal("inaccurate scope")
	}
	request.Fingerprint, request.Confirmation = preview.Fingerprint, ""
	if w = call("restore", request, true, origin); w.Code != 409 {
		t.Fatal("unconfirmed restore")
	}
	request.Confirmation = "确认覆盖并恢复"
	// 先注入回退副本无法保存的错误，必须保留全部文档。
	root := rt.root
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err = os.WriteFile(blocked, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.root = blocked
	rt.mu.Unlock()
	scopeBefore, _ := s.ReadDocuments(ctx, backupDocuments)
	if w = call("restore", request, true, origin); w.Code != 422 {
		t.Fatal("rollback failure ignored", w.Code)
	}
	scopeAfter, _ := s.ReadDocuments(ctx, backupDocuments)
	a, _ = json.Marshal(scopeBefore)
	after, _ = json.Marshal(scopeAfter)
	if string(a) != string(after) {
		t.Fatal("rollback save failure changed data")
	}
	rt.mu.Lock()
	rt.root = root
	rt.mu.Unlock()
	// 回退副本保存成功后才能提交恢复。
	w = call("restore", request, true, origin)
	if w.Code != 200 {
		t.Fatal("restore", w.Code, w.Body.String())
	}
	var restored struct{ RollbackFile, RollbackArchive string }
	_ = json.Unmarshal(w.Body.Bytes(), &restored)
	rollback, _, err := openBackup(restoreRequest{Archive: restored.RollbackArchive, Password: password})
	if err != nil || len(rollback.Workspace.Clients) != 2 || rollback.Traffic.Totals["client-one"].Uplink != 999 {
		t.Fatal("rollback", err)
	}
	docs, _ := s.ReadDocuments(ctx, backupDocuments)
	if string(docs[appliedDocument].Data) != "null" {
		t.Fatal("business restore retained old runtime history")
	}
	restoredGrants, err := readSubscriptions(docs[subscriptionsDocument])
	if err != nil || restoredGrants.Generation <= 1 || restoredGrants.Clients["client-one"].Generation == restoredGrants.Generation {
		t.Fatal("business restore retained active subscription grants", err)
	}
	var next workspaceDraft
	_ = json.Unmarshal(docs["workspace"].Data, &next)
	if len(next.Clients) != 1 || docs["workspace"].Revision != 3 || string(docs["proxy-identities"].Data) != string(before["proxy-identities"].Data) || next.Inbounds[0].Listen != "::" || next.Inbounds[0].ShareHost != "proxy.example.test" {
		t.Fatal("identity/revision/workspace not restored")
	}
	nextLedger, err := backupLedger(docs["traffic"].Data)
	if err != nil || nextLedger.Active || !nextLedger.Incomplete || nextLedger.Epoch != "" || len(nextLedger.Last) != 0 || nextLedger.Totals["client-one"].Total() != 1999 || len(nextLedger.Periods) != 0 {
		t.Fatal("usage regressed", err)
	}
	c := next.Inbounds[0].Connection
	if !strings.HasPrefix(c.CertificateFile, rt.root+string(os.PathSeparator)) || c.CertificateFile == cert {
		t.Fatal("certificate path reused")
	}
	for _, path := range []string{c.CertificateFile, c.KeyFile, filepath.Join(filepath.Dir(c.KeyFile), restored.RollbackFile)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("file permission", err)
		}
	}
	info, _ := os.Stat(filepath.Dir(c.KeyFile))
	if info.Mode().Perm() != 0700 {
		t.Fatal("directory permission")
	}
	certBefore, _ := os.ReadFile(cert)
	certAfter, _ := os.ReadFile(c.CertificateFile)
	if string(certBefore) != string(certAfter) {
		t.Fatal("certificate changed")
	}
	if _, err = s.Authenticate(ctx, session.Token); err != nil {
		t.Fatal("current account/session changed", err)
	}
	if rt.engine.Status().Running || m.Selected() != "26.7.28" {
		t.Fatal("started/switched core")
	}
	decoded, _ := base64.StdEncoding.DecodeString(restored.RollbackArchive)
	stored, _ := os.ReadFile(filepath.Join(filepath.Dir(c.KeyFile), restored.RollbackFile))
	if string(decoded) != string(stored) {
		t.Fatal("rollback not durably saved")
	}
	rollbackRequest := restoreRequest{Archive: restored.RollbackArchive, Password: password, Confirmation: "确认覆盖并恢复"}
	w = call("preview", rollbackRequest, true, origin)
	if w.Code != 200 {
		t.Fatal("rollback preview", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	rollbackRequest.Fingerprint = preview.Fingerprint
	if w = call("restore", rollbackRequest, true, origin); w.Code != 200 {
		t.Fatal("rollback restore", w.Code, w.Body.String())
	}
	rollbackDoc, _ := s.ReadDocument(ctx, "workspace")
	_ = json.Unmarshal(rollbackDoc.Data, &next)
	if len(next.Clients) != 2 || next.Clients[1].ID != "extra-client" {
		t.Fatal("rollback did not recover extra records")
	}
	if err = rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedWorkspace, _ := reopened.ReadDocument(ctx, "workspace")
	var persistedWorkspace workspaceDraft
	if json.Unmarshal(reopenedWorkspace.Data, &persistedWorkspace) != nil || persistedWorkspace.Inbounds[0].Listen != "::" || persistedWorkspace.Inbounds[0].ShareHost != "proxy.example.test" {
		t.Fatal("proxy endpoint lost after database reopen")
	}
	restoredIdentity, _ := reopened.ReadDocument(ctx, "proxy-identities")
	var reopenedIDs identities
	_ = json.Unmarshal(restoredIdentity.Data, &reopenedIDs)
	if reopenedIDs.Clients["client-one"] != b.Identities.Clients["client-one"] {
		t.Fatal("identity lost after reopen")
	}
	trafficDoc, _ := reopened.ReadDocument(ctx, "traffic")
	persisted, err := backupLedger(trafficDoc.Data)
	if err != nil || persisted.Totals["client-one"].Total() != 1999 || !persisted.Incomplete {
		t.Fatal("ledger lost after reopen", err)
	}
	if session, err := reopened.Authenticate(ctx, session.Token); err != nil || session.Username != "tester" {
		t.Fatal("account lost after reopen", err)
	}
}
