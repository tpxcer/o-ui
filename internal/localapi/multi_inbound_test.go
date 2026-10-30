package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"independent-panel/internal/traffic"
	"strings"
	"testing"
	"time"
)

func TestMultiInboundIPLimitAndManualRollback(t *testing.T) {
	s, rt, _, _ := subscriptionFixture(t)
	ctx := context.Background()
	doc, _ := s.ReadDocument(ctx, "workspace")
	var draft workspaceDraft
	_ = json.Unmarshal(doc.Data, &draft)
	second := draft.Inbounds[0]
	second.ID = "second"
	second.Preset = "hy2"
	second.Port++
	draft.Inbounds = append(draft.Inbounds, second)
	draft.Clients[0].InboundIDs = []string{draft.Inbounds[0].ID, "second"}
	draft.Clients[0].IPLimit = 1
	data, _ := json.Marshal(draft)
	_, _ = s.SaveDocument(ctx, "workspace", doc.Revision, data)
	previousBan, previousSample := runIPBan, sampleOnlineIPs
	t.Cleanup(func() { runIPBan, sampleOnlineIPs = previousBan, previousSample })
	rules := map[string]bool{}
	failUDP := false
	runIPBan = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "flush" {
			clear(rules)
			return "", nil
		}
		if failUDP && args[0] == "ban" && args[3] == "udp" {
			return "test failure", errors.New("test failure")
		}
		key := strings.Join(args[1:], " ")
		if args[0] == "ban" {
			rules[key] = true
		} else {
			delete(rules, key)
		}
		return "", nil
	}
	now := time.Now()
	sampleOnlineIPs = func(context.Context, *Runtime) (map[string][]onlineIP, error) {
		return map[string][]onlineIP{"client-client-one": {{IP: "203.0.113.1", LastSeen: now}, {IP: "203.0.113.1", LastSeen: now}, {IP: "203.0.113.2", LastSeen: now}}}, nil
	}
	failUDP = true
	rt.ipLimitTick(ctx, now)
	record, _, _ := readIPLimit(ctx, s)
	if len(rules) != 0 || len(record.Bans) != 0 {
		t.Fatal("partial automatic ban was not rolled back")
	}
	failUDP = false
	rt.ipLimitTick(ctx, now)
	if len(rt.iplimit.online["client-one"]) != 2 || len(rules) != 2 {
		t.Fatal("IP deduplication or endpoint coverage", len(rules))
	}
	record, revision, _ := readIPLimit(ctx, s)
	if len(record.Bans) != 2 || record.Bans[0].IP != record.Bans[1].IP {
		t.Fatal("automatic ban did not cover both ports")
	}
	doc, _ = s.ReadDocument(ctx, "workspace")
	req := ipBanRequest{ClientID: "client-one", IP: "203.0.113.2", Confirm: true, Revision: revision, WorkspaceRevision: doc.Revision}
	if code, err := rt.changeIPBan(ctx, "unban", req); code != 200 || err != nil || len(rules) != 0 {
		t.Fatal("unban", code, err)
	}
	_, revision, _ = readIPLimit(ctx, s)
	req.IP, req.Minutes, req.Revision = "203.0.113.1", 30, revision
	failUDP = true
	if code, _ := rt.changeIPBan(ctx, "ban", req); code != 502 || len(rules) != 0 {
		t.Fatal("partial firewall change not rolled back", code, len(rules))
	}
	record, _, _ = readIPLimit(ctx, s)
	if len(record.Bans) != 0 {
		t.Fatal("failed operation saved bans")
	}
	failUDP = false
	if code, err := rt.changeIPBan(ctx, "ban", req); code != 200 || err != nil || len(rules) != 2 {
		t.Fatal("manual multi ban", code, err)
	}
}

func TestMultiInboundBackupRestoreKeepsAssociationsAndUsage(t *testing.T) {
	s, rt, _, _ := subscriptionFixture(t)
	ctx := context.Background()
	doc, _ := s.ReadDocument(ctx, "workspace")
	var draft workspaceDraft
	_ = json.Unmarshal(doc.Data, &draft)
	second := draft.Inbounds[0]
	second.ID = "second"
	second.Port++
	draft.Inbounds = append(draft.Inbounds, second)
	draft.Clients[0].InboundIDs = []string{draft.Inbounds[0].ID, "second"}
	data, _ := json.Marshal(draft)
	_, _ = s.SaveDocument(ctx, "workspace", doc.Revision, data)
	if _, err := rt.credentials(ctx, draft); err != nil {
		t.Fatal(err)
	}
	ledger := traffic.Empty()
	ledger.Totals["client-one"] = traffic.Count{Downlink: 100}
	inbound := traffic.Empty()
	inbound.Totals["second"] = traffic.Count{Downlink: 20}
	ledger.Inbounds = &inbound
	data, _ = json.Marshal(ledger)
	_, _ = s.SaveDocument(ctx, "traffic", 0, data)
	_, file, err := rt.exportBackup(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	saved, _, err := openBackup(restoreRequest{Archive: base64.StdEncoding.EncodeToString(file)})
	if err != nil || len(saved.Workspace.Clients[0].inboundIDs()) != 2 {
		t.Fatal("backup associations", err)
	}
	identity, _ := s.ReadDocument(ctx, "proxy-identities")
	ledger.Totals["client-one"] = traffic.Count{Downlink: 200}
	ledger.Inbounds.Totals["second"] = traffic.Count{Downlink: 40}
	data, _ = json.Marshal(ledger)
	_, _ = s.SaveDocument(ctx, "traffic", 1, data)
	current, docs, err := rt.capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = rt.restore(ctx, saved, current, docs, ""); err != nil {
		t.Fatal(err)
	}
	restored, _, err := rt.ledger(ctx)
	if err != nil || restored.Totals["client-one"].Total() != 200 || restored.Inbounds.Totals["second"].Total() != 40 || len(restored.Inbounds.Last) != 0 || len(restored.Inbounds.Periods) != 0 {
		t.Fatal("restored usage regressed or live baseline retained", err)
	}
	after, _ := s.ReadDocument(ctx, "proxy-identities")
	if string(identity.Data) != string(after.Data) {
		t.Fatal("restore changed credentials")
	}
	workspace, _ := s.ReadDocument(ctx, "workspace")
	var got workspaceDraft
	_ = json.Unmarshal(workspace.Data, &got)
	if len(got.Clients[0].inboundIDs()) != 2 {
		t.Fatal("restore lost association")
	}
}

func TestMultiInboundTelegramNamesAndIndependentTraffic(t *testing.T) {
	calls := botFixture(t)
	s, rt, _, _ := subscriptionFixture(t)
	ctx := context.Background()
	settings := saveBotSettings(t, rt)
	doc, _ := s.ReadDocument(ctx, "workspace")
	var draft workspaceDraft
	_ = json.Unmarshal(doc.Data, &draft)
	draft.Inbounds[0].Name = "FIRST-NODE"
	second := draft.Inbounds[0]
	second.ID = "second"
	second.Name = "SECOND-NODE"
	second.Port++
	draft.Inbounds = append(draft.Inbounds, second)
	draft.Clients[0].InboundIDs = []string{draft.Inbounds[0].ID, "second"}
	data, _ := json.Marshal(draft)
	_, _ = s.SaveDocument(ctx, "workspace", doc.Revision, data)
	ledger := traffic.Empty()
	ledger.Totals["client-one"] = traffic.Count{Downlink: 900}
	nodes := traffic.Empty()
	now := time.Now()
	nodes.SampledAt = &now
	nodes.Totals[draft.Inbounds[0].ID] = traffic.Count{Downlink: 100}
	nodes.Totals["second"] = traffic.Count{Downlink: 200}
	ledger.Inbounds = &nodes
	data, _ = json.Marshal(ledger)
	_, _ = s.SaveDocument(ctx, "traffic", 0, data)
	rt.telegramAction(ctx, settings, "1001", "c:client-one", "en")
	text, _ := lastReply(t, calls())
	if !strings.Contains(text, "FIRST-NODE, SECOND-NODE") {
		t.Fatal("Telegram lost linked inbound names")
	}
	rt.telegramAction(ctx, settings, "1001", "ib", "en")
	text, _ = lastReply(t, calls())
	if strings.Contains(text, "900 B") || !strings.Contains(text, "100 B") || !strings.Contains(text, "200 B") {
		t.Fatal("Telegram duplicated shared account usage")
	}
}
