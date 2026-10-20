package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/store"
)

func TestRuntimeCredentialsPersistAndStayPrivate(t *testing.T) {
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
	root := t.TempDir()
	rt, err := NewRuntime(m, s, root)
	if err != nil {
		t.Fatal(err)
	}
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	first, err := rt.credentials(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if first.Clients["client-one"].UUID == "" || first.Inbounds["in-one"].PrivateKey == "" {
		t.Fatal("missing generated identity")
	}
	if _, err := NewRuntime(m, s, root); err == nil {
		t.Fatal("second controller accepted")
	}
	_ = rt.Close()
	rt, err = NewRuntime(m, s, root)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	draft.Clients[0].Name = "renamed"
	second, err := rt.credentials(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("identity changed after edit/reopen")
	}
	visible, _ := json.Marshal(draft)
	if strings.Contains(string(visible), first.Clients["client-one"].UUID) || strings.Contains(string(visible), first.Inbounds["in-one"].PrivateKey) {
		t.Fatal("private identity leaked into workspace")
	}
}

func TestRuntimeDirectoryRejectsSymlink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntime(nil, nil, link); err == nil {
		t.Fatal("symlink directory accepted")
	}
}

func TestRuntimeRejectsMissingParameters(t *testing.T) {
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	ids := identities{Clients: map[string]proxyIdentity{"client-one": {UUID: "c871af3d-e670-4e62-ad8e-c735dc650101"}}, Inbounds: map[string]realityIdentity{}}
	if _, err := buildInbounds(draft, ids, nil, time.Now()); err == nil {
		t.Fatal("missing reality accepted")
	}
}

func TestMihomoCompatibilityPersistsAndCompiles(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	draft.Inbounds[0].Connection = &connectionDraft{Target: "example.test:443", ServerName: "example.test", Path: "/", Mode: "auto", MihomoCompatibility: true}
	data, _ := json.Marshal(draft)
	if _, err = s.SaveDocument(context.Background(), "workspace", 0, data); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	doc, err := s.ReadDocument(context.Background(), "workspace")
	if err != nil || json.Unmarshal(doc.Data, &draft) != nil || !draft.Inbounds[0].Connection.MihomoCompatibility {
		t.Fatal("compatibility lost after store reopen")
	}
	ids := identities{Clients: map[string]proxyIdentity{"client-one": {UUID: "c871af3d-e670-4e62-ad8e-c735dc650101"}}, Inbounds: map[string]realityIdentity{"in-one": {PrivateKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("X", 32))), ShortID: "0123456789abcdef"}}}
	for _, preset := range []string{"reality", "xhttp-reality"} {
		draft.Inbounds[0].Preset = preset
		for _, enabled := range []bool{true, false} {
			draft.Inbounds[0].Connection.MihomoCompatibility = enabled
			compiled, err := buildInbounds(draft, ids, nil, time.Now())
			if err != nil || len(compiled) != 1 {
				t.Fatal("compatibility build failed")
			}
			if (compiled[0].Stream.Reality.MinClientVer == "1.8.2") != enabled {
				t.Fatal("compatibility flag not compiled")
			}
		}
	}
}

func TestRuntimeSnapshotConflictBeforeWriting(t *testing.T) {
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
	if _, err := rt.candidate(context.Background(), runtimeRequest{Version: "26.3.27"}); err != store.ErrConflict {
		t.Fatal("changed version not rejected")
	}
	doc, err := s.ReadDocument(context.Background(), "proxy-identities")
	if err != nil || doc.Revision != 0 {
		t.Fatal("conflict generated new identity")
	}
}

func TestCertificateFileBoundaries(t *testing.T) {
	dir := t.TempDir()
	if _, err := readPEM(dir); err == nil {
		t.Fatal("directory accepted")
	}
	large := filepath.Join(dir, "large.pem")
	if err := os.WriteFile(large, make([]byte, (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPEM(large); err == nil {
		t.Fatal("oversized certificate accepted")
	}
	pipe := filepath.Join(dir, "pipe.pem")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPEM(pipe); err == nil {
		t.Fatal("pipe accepted")
	}
}

func TestCompileSettingsProtectsManagedFields(t *testing.T) {
	settings, err := compileSettings(json.RawMessage("null"))
	if err != nil {
		t.Fatal(err)
	}
	if settings["inbounds"] != nil || settings["api"] != nil {
		t.Fatal("settings injected control/listener")
	}
	data := json.RawMessage(`{"freedomStrategy":"AsIs","routingStrategy":"AsIs","probeURL":"https://example.test/","statistics":true,"logLevel":"warning","accessLog":false,"dnsLog":false,"rules":"[]","outbounds":"[{\"tag\":\"direct\",\"protocol\":\"freedom\"}]","balancers":"[]","dns":"{\"servers\":[\"localhost\"]}","advanced":"{}"}`)
	var draft settingsDraft
	if err := json.Unmarshal(data, &draft); err != nil {
		t.Fatal(err)
	}
	for _, advanced := range []string{`{"inbounds":[]}`, `{"api":{}}`, `{"log":{"access":"/tmp/unwanted"}}`, `{"transport":[]}`} {
		draft.Advanced = advanced
		data, _ := json.Marshal(draft)
		if _, err := compileSettings(data); err == nil {
			t.Fatal("advanced override accepted")
		}
	}
	draft.Advanced = "{}"
	draft.Outbounds = `[{"tag":"direct","protocol":"wireguard"}]`
	data, _ = json.Marshal(draft)
	if _, err := compileSettings(data); err == nil {
		t.Fatal("system interface protocol accepted")
	}
	draft.Outbounds = `[{"tag":"direct","protocol":"freedom","settings":{"domainStrategy":"ForceIPv6"}}]`
	draft.FreedomStrategy = "UseIPv4"
	data, _ = json.Marshal(draft)
	full, err := compileSettings(data)
	if err != nil {
		t.Fatal(err)
	}
	freedom := full["outbounds"].([]map[string]any)[0]["settings"].(map[string]any)
	if freedom["targetStrategy"] != "UseIPv4" || freedom["domainStrategy"] != nil {
		t.Fatal("freedom strategy mismatch")
	}
	// 反向代理标签只在由入站客户端提供时可作路由目标，且不能与静态出站重名，也不能被负载均衡选择。
	draft.Rules = `[{"type":"field","user":["client-two"],"outboundTag":"r-home"}]`
	data, _ = json.Marshal(draft)
	if _, err := compileSettings(data); err == nil {
		t.Fatal("unknown reverse target accepted")
	}
	if _, err := compileSettings(data, "r-home"); err != nil {
		t.Fatal("reverse target rejected", err)
	}
	if _, err := compileSettings(data, "r-home", "direct"); err == nil {
		t.Fatal("reverse tag duplicating an outbound accepted")
	}
	draft.Rules, draft.Balancers = "[]", `[{"tag":"auto","selector":["r-"]}]`
	data, _ = json.Marshal(draft)
	if _, err := compileSettings(data, "r-home"); err == nil {
		t.Fatal("balancer selected a dynamic reverse outbound")
	}
	draft.Balancers = "[]"
	for _, rule := range []string{`{"type":"field","outboundTag":"direct"}`, `{"type":"field","outboundTag":"direct","network":"bogus"}`, `{"type":"field","outboundTag":"direct","ip":[]}`} {
		draft.Rules = "[" + rule + "]"
		data, _ := json.Marshal(draft)
		if _, err := compileSettings(data); err == nil {
			t.Fatal("invalid routing accepted")
		}
	}
}
