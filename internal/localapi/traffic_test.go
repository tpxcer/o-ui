package localapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

func TestQuotaAndDeadlineBoundaries(t *testing.T) {
	now := time.Now()
	deadline := now.UnixMilli()
	if quotaBytes(0) != 0 || quotaBytes(0.0000000011) != 2 || !blocked(clientLimit{Quota: 10}, traffic.Count{Uplink: 6, Downlink: 4}, now) || !blocked(clientLimit{ExpiresAt: &deadline}, traffic.Count{}, now) || blocked(clientLimit{}, traffic.Count{Uplink: 100}, now) {
		t.Fatal("limit boundary")
	}
	if validateWorkspaceDraft(json.RawMessage(strings.ReplaceAll(string(validWorkspace()), `"quotaGB":0`, `"quotaGB":0.0000000001`)), nil) == nil {
		t.Fatal("sub-byte quota accepted as unlimited")
	}
}

func TestTrafficSnapshotFiltersWithoutDraftOrCounterReset(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rt := &Runtime{state: s, root: t.TempDir()}
	now := time.Now()
	expired := now.Add(-time.Second).UnixMilli()
	policy := runtimePolicy{Clients: map[string]clientLimit{"a": {Quota: 10}, "b": {ExpiresAt: &expired}, "c": {}}}
	ledger := traffic.Empty()
	ledger.Totals["a"] = traffic.Count{Uplink: 5, Downlink: 5}
	data, _ := json.Marshal(ledger)
	if _, err = s.SaveDocument(context.Background(), "traffic", 0, data); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	c := engine.Candidate{File: filepath.Join(stage, "config.json"), PolicyFile: filepath.Join(stage, "policy.json")}
	data, _ = json.Marshal(policy)
	if err = os.WriteFile(c.PolicyFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(map[string]any{"inbounds": []config.CompiledInbound{{Tag: "fixture", Settings: config.ProtocolSettings{Clients: []config.User{{Email: "client-a"}, {Email: "client-b"}, {Email: "client-c"}}}}}, "outbounds": []any{map[string]string{"tag": "saved-outbound"}}})
	if err = os.WriteFile(c.File, data, 0600); err != nil {
		t.Fatal(err)
	}
	full, changed, err := rt.filtered(context.Background(), c, now)
	if err != nil || !changed {
		t.Fatal("filter", err)
	}
	var inbounds []config.CompiledInbound
	_ = json.Unmarshal(full["inbounds"], &inbounds)
	if len(inbounds) != 1 || len(inbounds[0].Settings.Clients) != 1 || inbounds[0].Settings.Clients[0].Email != "client-c" || !strings.Contains(string(full["outbounds"]), "saved-outbound") {
		t.Fatal("snapshot changed unrelated data")
	}
	stored, _, err := rt.ledger(context.Background())
	if err != nil || stored.Totals["a"].Total() != 10 {
		t.Fatal("filter reset use")
	}
	if err = os.WriteFile(c.PolicyFile, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = rt.filtered(context.Background(), c, now); err == nil {
		t.Fatal("missing applied policy accepted")
	}
}

func TestCorruptTrafficDoesNotBecomeEmpty(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.SaveDocument(context.Background(), "traffic", 0, json.RawMessage(`{"totals":null,"last":{}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = (&Runtime{state: s}).ledger(context.Background()); err == nil {
		t.Fatal("corrupt ledger reset")
	}
	if _, err = s.SaveDocument(context.Background(), "traffic", 1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = (&Runtime{state: s}).ledger(context.Background()); err == nil {
		t.Fatal("empty object silently became fresh ledger")
	}
}

func TestUncleanLedgerKeepsWarningAcrossRuntimeReopen(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ledger := traffic.Empty()
	ledger.Active = true
	ledger.Epoch = "fixture"
	now := time.Now()
	ledger.SampledAt = &now
	ledger.Totals["one"] = traffic.Count{Uplink: 42}
	data, _ := json.Marshal(ledger)
	if _, err = s.SaveDocument(context.Background(), "traffic", 0, data); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(nil, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	after, _, err := rt.ledger(context.Background())
	if err != nil || !after.Incomplete || after.Active || after.Totals["one"].Uplink != 42 || rt.issue == "" {
		t.Fatal("unclean stop silently reset", err)
	}
}

func TestStatsDisabledDoesNotOutputCollectors(t *testing.T) {
	full, err := compileSettings(json.RawMessage(`{"freedomStrategy":"AsIs","routingStrategy":"AsIs","probeURL":"https://example.test/","statistics":false,"logLevel":"warning","accessLog":false,"dnsLog":false,"rules":"[]","outbounds":"[{\"tag\":\"direct\",\"protocol\":\"freedom\"}]","balancers":"[]","dns":"{\"servers\":[\"localhost\"]}","advanced":"{}"}`))
	if err != nil || full["stats"] != nil || full["policy"] != nil {
		t.Fatal("stats off ignored", err)
	}
}
