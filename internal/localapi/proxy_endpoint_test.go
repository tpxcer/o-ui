package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

func endpointDraft(t *testing.T, listen, share string) workspaceDraft {
	t.Helper()
	var raw map[string]any
	_ = json.Unmarshal(validWorkspace(), &raw)
	in := raw["inbounds"].([]any)[0].(map[string]any)
	in["listen"], in["shareHost"] = listen, share
	in["connection"] = map[string]any{"target": "example.test:443", "serverName": "example.test", "path": "/", "mode": "auto"}
	data, _ := json.Marshal(raw)
	if err := validateWorkspaceDraft(data, nil); err != nil {
		t.Fatal("valid proxy endpoint rejected", err)
	}
	var draft workspaceDraft
	_ = json.Unmarshal(data, &draft)
	return draft
}

func TestProxyEndpointDraftCompile(t *testing.T) {
	ids := identities{Clients: map[string]proxyIdentity{"client-one": {UUID: "c871af3d-e670-4e62-ad8e-c735dc650101"}}, Inbounds: map[string]realityIdentity{"in-one": {PrivateKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("X", 32))), ShortID: "0123456789abcdef"}}}
	for _, listen := range []string{"127.0.0.1", "::1", "0.0.0.0", "::", "192.0.2.10"} {
		t.Run(listen, func(t *testing.T) {
			draft := endpointDraft(t, listen, "proxy.example.test")
			compiled, err := buildInbounds(draft, ids, nil, time.Now())
			if err != nil || len(compiled) != 1 || compiled[0].Listen != listen {
				t.Fatal("proxy listener lost during compile", err)
			}
		})
	}
}

func TestProxyEndpointConfirmationHistoryAndFiltering(t *testing.T) {
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
	for _, listen := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "0.0.0.0", "::", "192.0.2.1"} {
		t.Run(listen, func(t *testing.T) {
			draft := endpointDraft(t, listen, "proxy.example.test")
			second := draft.Clients[0]
			second.ID, second.Name = "second", "second"
			draft.Clients = append(draft.Clients, second)
			ids, err := rt.credentials(context.Background(), draft)
			if err != nil {
				t.Fatal(err)
			}
			inbounds, err := buildInbounds(draft, ids, nil, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(rt.root, strings.ReplaceAll(listen, ":", "_")+".json")
			policyFile := file + ".policy"
			data, _ := json.Marshal(map[string]any{"inbounds": inbounds})
			if err = writePrivate(file, data); err != nil {
				t.Fatal(err)
			}
			expiry := time.Now().Add(-time.Second).UnixMilli()
			draft.Clients[0].ExpiresAt = &expiry
			data, _ = json.Marshal(policyFor(draft))
			if err = writePrivate(policyFile, data); err != nil {
				t.Fatal(err)
			}
			c := engine.Candidate{Version: "26.7.28", File: file, PolicyFile: policyFile, Inbounds: 1}
			public := listen == "0.0.0.0" || listen == "::" || listen == "192.0.2.1"
			for _, phrase := range []string{"", "true", "确认恢复上次应用", "确认重启当前配置", proxyConfirmation} {
				if err := rt.confirmProxy(c, phrase); (err == nil) != (!public || phrase == proxyConfirmation) {
					t.Fatal("confirmation bypass or loopback regression", err)
				}
			}
			full, changed, err := rt.filtered(context.Background(), c, time.Now())
			var filtered []config.CompiledInbound
			if err != nil || !changed || json.Unmarshal(full["inbounds"], &filtered) != nil || len(filtered) != 1 || filtered[0].Listen != inbounds[0].Listen || len(filtered[0].Settings.Clients) != 1 || filtered[0].Settings.Clients[0].Email != "client-second" {
				t.Fatal("limit reload changed listener", err)
			}
			record, err := rt.snapshot(c)
			if err != nil {
				t.Fatal(err)
			}
			doc, _ := s.ReadDocument(context.Background(), appliedDocument)
			data, _ = json.Marshal(record)
			if _, err = s.SaveDocument(context.Background(), appliedDocument, doc.Revision, data); err != nil {
				t.Fatal(err)
			}
			summary := rt.historySummary(context.Background())
			if !summary.Available || summary.NonLoopbackProxy != public || rt.engine.Status().Running {
				t.Fatal("history lost proxy scope or started a listener")
			}
		})
	}
	if err := rt.confirmProxy(engine.Candidate{File: filepath.Join(rt.root, "missing")}, proxyConfirmation); err == nil {
		t.Fatal("unreadable config bypassed confirmation")
	}
}
