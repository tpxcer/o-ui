package localapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func validWorkspace() json.RawMessage {
	return json.RawMessage(`{"inbounds":[{"id":"in-one","name":"测试入站","preset":"reality","port":18443,"enabled":true,"sniffing":{"enabled":true,"http":true,"tls":true,"quic":true,"routeOnly":true,"fakeDns":false,"metadataOnly":false}}],"clients":[{"id":"client-one","inboundId":"in-one","name":"client","enabled":true,"quotaGB":0,"usedGB":0,"expiresAt":null,"ipLimit":0,"subscription":false}]}`)
}

func TestInboundTrafficResetSavedIndependentlyAndValidated(t *testing.T) {
	for _, value := range []string{"never", "daily", "weekly", "monthly", "hourly", "invalid"} {
		data := strings.Replace(string(validWorkspace()), `"preset":"reality"`, `"trafficReset":"`+value+`","preset":"reality"`, 1)
		err := validateWorkspaceDraft(json.RawMessage(data), nil)
		if (value == "hourly" || value == "invalid") != (err != nil) {
			t.Fatalf("period %s: %v", value, err)
		}
		if err == nil {
			var draft workspaceDraft
			if err = json.Unmarshal([]byte(data), &draft); err != nil || draft.Inbounds[0].TrafficReset != value || draft.Clients[0].QuotaGB != 0 {
				t.Fatal("setting lost or quota changed", err)
			}
			encoded, _ := json.Marshal(draft)
			if !strings.Contains(string(encoded), `"trafficReset":"`+value+`"`) {
				t.Fatal("setting did not round trip")
			}
		}
	}
	if err := validateWorkspaceDraft(validWorkspace(), nil); err != nil {
		t.Fatal("old workspace rejected", err)
	}
}

func TestValidateWorkspaceDraft(t *testing.T) {
	valid := validWorkspace()
	if err := validateWorkspaceDraft(valid, nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, old, new string }{
		{"privileged-port", `18443`, `443`},
		{"unknown-preset", `"reality"`, `"shadowsocks"`},
		{"ip-limit-negative", `"ipLimit":0`, `"ipLimit":-1`},
		{"ip-limit-too-large", `"ipLimit":0`, `"ipLimit":10001`},
		{"fake-usage", `"usedGB":0`, `"usedGB":3`},
		{"negative-quota", `"quotaGB":0`, `"quotaGB":-1`},
		{"broken-link", `"inboundId":"in-one"`, `"inboundId":"missing"`},
		{"null-list", `"clients":[`, `"clients":null,"extra":[`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if validateWorkspaceDraft(json.RawMessage(strings.ReplaceAll(string(valid), test.old, test.new)), nil) == nil {
				t.Fatal("invalid workspace accepted")
			}
		})
	}
	if err := validateWorkspaceDraft(json.RawMessage(strings.Replace(string(valid), `"ipLimit":0`, `"ipLimit":2`, 1)), nil); err != nil {
		t.Fatal("IP limit rejected", err)
	}
	// 删除入站时其客户端一并删除、可批量删除（2026-10-22起）；但剩余客户端不能指向已删除的入站。
	if err := validateWorkspaceDraft(json.RawMessage(`{"inbounds":[],"clients":[]}`), valid); err != nil {
		t.Fatal("deleting an inbound together with its clients rejected", err)
	}
	orphan := strings.Replace(string(valid), `"inboundId":"in-one"`, `"inboundId":"in-gone"`, 1)
	if validateWorkspaceDraft(json.RawMessage(orphan), valid) == nil {
		t.Fatal("client pointing at a deleted inbound accepted")
	}
	if validateWorkspaceDraft(json.RawMessage(`{"inbounds":[],"clients":[]}`), json.RawMessage(`{"inbounds":`)) == nil {
		t.Fatal("corrupted previous draft overwritten")
	}
	var state workspaceDraft
	if err := json.Unmarshal(valid, &state); err != nil {
		t.Fatal(err)
	}
	state.Clients = []clientDraft{}
	withoutClients, _ := json.Marshal(state)
	if err := validateWorkspaceDraft(withoutClients, valid); err != nil {
		t.Fatal("single client delete rejected", err)
	}
	if err := validateWorkspaceDraft(json.RawMessage(`{"inbounds":[],"clients":[]}`), withoutClients); err != nil {
		t.Fatal("single inbound delete rejected", err)
	}
	state.Inbounds = append(state.Inbounds, state.Inbounds[0])
	duplicate, _ := json.Marshal(state)
	if validateWorkspaceDraft(duplicate, nil) == nil {
		t.Fatal("duplicate IDs accepted")
	}
	state.Inbounds[1].ID = "in-two"
	duplicate, _ = json.Marshal(state)
	if validateWorkspaceDraft(duplicate, nil) == nil {
		t.Fatal("duplicate port accepted")
	}
}

func TestMihomoCompatibilityDraftValidation(t *testing.T) {
	for _, preset := range []string{"reality", "xhttp-reality", "hy2", "xhttp-tls"} {
		var draft workspaceDraft
		_ = json.Unmarshal(validWorkspace(), &draft)
		draft.Inbounds[0].Preset = preset
		draft.Inbounds[0].Connection = &connectionDraft{MihomoCompatibility: true}
		data, _ := json.Marshal(draft)
		err := validateWorkspaceDraft(data, nil)
		if (err == nil) != strings.Contains(preset, "reality") {
			t.Fatal("compatibility accepted for wrong security layer")
		}
	}
	for _, value := range []string{`"true"`, `1`, `{}`} {
		data := strings.Replace(string(validWorkspace()), `"sniffing":`, `"connection":{"mihomoCompatibility":`+value+`},"sniffing":`, 1)
		if validateWorkspaceDraft(json.RawMessage(data), nil) == nil {
			t.Fatal("non-boolean compatibility flag accepted")
		}
	}
}

func TestReverseTagDraftValidation(t *testing.T) {
	valid := string(validWorkspace())
	withTag := strings.Replace(valid, `"subscription":false}`, `"subscription":false,"reverseTag":"r-home"}`, 1)
	if err := validateWorkspaceDraft(json.RawMessage(withTag), nil); err != nil {
		t.Fatal("VLESS reverse tag rejected", err)
	}
	for name, draft := range map[string]string{
		"bad-chars": strings.Replace(withTag, `"r-home"`, `"r home"`, 1),
		"too-long":  strings.Replace(withTag, `"r-home"`, `"`+strings.Repeat("r", 65)+`"`, 1),
		"hy2":       strings.Replace(withTag, `"preset":"reality"`, `"preset":"hy2"`, 1),
		"duplicate": strings.Replace(withTag, `]}`, `,{"id":"client-two","inboundId":"in-one","name":"two","enabled":true,"quotaGB":0,"usedGB":0,"expiresAt":null,"ipLimit":0,"subscription":false,"reverseTag":"r-home"}]}`, 1),
	} {
		if validateWorkspaceDraft(json.RawMessage(draft), nil) == nil {
			t.Fatal("invalid reverse tag accepted:", name)
		}
	}
}

func TestMultiInboundWorkspaceValidation(t *testing.T) {
	var draft workspaceDraft
	_ = json.Unmarshal(validWorkspace(), &draft)
	other := draft.Inbounds[0]
	other.ID = "second"
	other.Port++
	draft.Inbounds = append(draft.Inbounds, other)
	for _, ids := range [][]string{nil, {"in-one", "second"}, {}, {"second"}, {"in-one", "in-one"}, {"in-one", "missing"}} {
		draft.Clients[0].InboundIDs = ids
		data, _ := json.Marshal(draft)
		if ids != nil && len(ids) == 0 {
			data = []byte(strings.Replace(string(data), `"inboundId":"in-one"`, `"inboundId":"in-one","inboundIds":[]`, 1))
		}
		valid := ids == nil || len(ids) == 2 && ids[1] == "second"
		if (validateWorkspaceDraft(data, nil) == nil) != valid {
			t.Fatalf("association validation: %v", ids)
		}
	}
	draft.Clients[0].InboundIDs = []string{"in-one", "second"}
	previous, _ := json.Marshal(draft)
	draft.Clients[0].InboundIDs = nil
	data, _ := json.Marshal(draft)
	if validateWorkspaceDraft(data, previous) == nil {
		t.Fatal("legacy writer silently dropped associations")
	}
}
