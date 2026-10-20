package localapi

import "testing"

func TestParseOutboundStats(t *testing.T) {
	got := parseOutboundStats([]byte(`{"stat":[{"name":"outbound>>>direct>>>traffic>>>uplink","value":"10"},{"name":"outbound>>>direct>>>traffic>>>downlink","value":20},{"name":"user>>>client-a>>>traffic>>>uplink","value":5},{"name":"outbound>>>block>>>traffic>>>downlink"}]}`))
	if got["direct"].Uplink != 10 || got["direct"].Downlink != 20 || got["block"].Downlink != 0 || len(got) != 2 {
		t.Fatal(got)
	}
	if len(parseOutboundStats([]byte("not json"))) != 0 {
		t.Fatal("invalid output must yield no counters")
	}
}
