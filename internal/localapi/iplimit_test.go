package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseOnlineIPs(t *testing.T) {
	got := parseOnlineIPs([]byte(`{"users":[{"email":"client-a","ips":[{"ip":"203.0.113.7","lastSeen":"1700000000"},{"ip":"[2001:db8::1]","last_seen":1700000010},{"ip":"not-an-ip","lastSeen":"1"}]},{"email":"","ips":[{"ip":"203.0.113.9"}]}]}`))
	want := map[string][]onlineIP{"client-a": {{IP: "203.0.113.7", LastSeen: time.Unix(1700000000, 0)}, {IP: "2001:db8::1", LastSeen: time.Unix(1700000010, 0)}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if len(parseOnlineIPs([]byte("not json"))) != 0 || len(parseOnlineIPs([]byte("{}"))) != 0 {
		t.Fatal("invalid output should yield nothing")
	}
}

func TestDecideIPLimit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(ago time.Duration) time.Time { return now.Add(-ago) }
	first := map[string]time.Time{"a": at(time.Hour), "b": at(time.Minute), "c": at(time.Second)}
	for _, c := range []struct {
		name       string
		limit      int
		online     []onlineIP
		skip       map[string]bool
		keep, bans []string
	}{
		{"under limit", 2, []onlineIP{{"a", now}, {"b", now}}, nil, []string{"a", "b"}, nil},
		{"earliest wins", 1, []onlineIP{{"b", now}, {"a", now}}, nil, []string{"a"}, []string{"b"}},
		{"network switch: stale incumbent loses", 1, []onlineIP{{"a", at(3 * time.Minute)}, {"c", now}}, nil, []string{"c"}, []string{"a"}},
		{"banned and exempt do not count", 1, []onlineIP{{"a", now}, {"b", now}, {"c", now}}, map[string]bool{"a": true}, []string{"b"}, []string{"c"}},
		{"ties by IP", 1, []onlineIP{{"y", now}, {"x", now}}, nil, []string{"x"}, []string{"y"}},
	} {
		keep, bans := decideIPLimit(c.limit, c.online, first, c.skip, now)
		if !reflect.DeepEqual(keep, c.keep) || !reflect.DeepEqual(bans, c.bans) {
			t.Fatalf("%s: keep %v ban %v", c.name, keep, bans)
		}
	}
}

func TestIPLimitTickBansUnbansAndRebuilds(t *testing.T) {
	s, rt, handler, cookie := subscriptionFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	calls, helperMissing := []string{}, false
	online := map[string][]onlineIP{}
	previousBan, previousSample := runIPBan, sampleOnlineIPs
	t.Cleanup(func() { runIPBan, sampleOnlineIPs = previousBan, previousSample })
	runIPBan = func(_ context.Context, args ...string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if helperMissing {
			return "", errIPLimitHelper
		}
		calls = append(calls, strings.Join(args, " "))
		return "ok", nil
	}
	sampleOnlineIPs = func(context.Context, *Runtime) (map[string][]onlineIP, error) {
		copied := map[string][]onlineIP{}
		for k, v := range online {
			copied[k] = append([]onlineIP(nil), v...)
		}
		return copied, nil
	}
	take := func() []string { mu.Lock(); defer mu.Unlock(); out := calls; calls = nil; return out }
	setLimit := func(limit string) {
		doc, _ := s.ReadDocument(ctx, "workspace")
		data := strings.Replace(strings.Replace(string(doc.Data), `"ipLimit":0`, `"ipLimit":`+limit, 1), `"ipLimit":1`, `"ipLimit":`+limit, 1)
		if _, err := s.SaveDocument(ctx, "workspace", doc.Revision, json.RawMessage(data)); err != nil {
			t.Fatal(err)
		}
	}
	record := func() ipLimitRecord { r, _, _ := readIPLimit(ctx, s); return r }
	setLimit("1")
	t0 := time.Now().Truncate(time.Second)
	online["client-client-one"] = []onlineIP{{"203.0.113.1", t0}}
	rt.ipLimitTick(ctx, t0)
	if got := take(); !reflect.DeepEqual(got, []string{"flush"}) {
		t.Fatal("first tick should only flush the chain", got)
	}
	t1 := t0.Add(10 * time.Second)
	online["client-client-one"] = []onlineIP{{"203.0.113.2", t1}, {"203.0.113.1", t1}}
	rt.ipLimitTick(ctx, t1)
	if got := take(); !reflect.DeepEqual(got, []string{"ban 203.0.113.2 18443 tcp"}) {
		t.Fatal("newcomer should be banned on the inbound port", got)
	}
	if bans := record().Bans; len(bans) != 1 || !bans[0].Until.Equal(t1.Add(ipBanDuration)) {
		t.Fatal("ban record", bans)
	}
	rt.ipLimitTick(ctx, t1.Add(10*time.Second))
	if got := take(); len(got) != 0 {
		t.Fatal("an already banned IP must not be banned again", got)
	}
	// 面板重启：先清空本链，再按记录重建未到期的封禁。
	rt.iplimit = newIPLimiter()
	rt.ipLimitTick(ctx, t1.Add(20*time.Second))
	if got := take(); !reflect.DeepEqual(got, []string{"flush", "ban 203.0.113.2 18443 tcp"}) {
		t.Fatal("restart should rebuild bans", got)
	}
	// 页面查询与手动解封：解封后30分钟内放行，不再计入上限。
	call := func(method, path, body string, want int) map[string]any {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var result map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	view := call("GET", "/api/runtime/iplimit", "", 200)
	client := view["clients"].(map[string]any)["client-one"].(map[string]any)
	if len(client["online"].([]any)) != 2 || len(client["banned"].([]any)) != 1 {
		t.Fatal("view", view)
	}
	unban := func(ip string, want int) {
		doc, _ := s.ReadDocument(ctx, "workspace")
		_, revision, _ := readIPLimit(ctx, s)
		body, _ := json.Marshal(ipBanRequest{ClientID: "client-one", IP: ip, Confirm: true, Revision: revision, WorkspaceRevision: doc.Revision})
		call("POST", "/api/runtime/iplimit/unban", string(body), want)
	}
	unban("203.0.113.9", 404)
	unban("203.0.113.2", 200)
	if got := take(); !reflect.DeepEqual(got, []string{"unban 203.0.113.2 18443 tcp"}) {
		t.Fatal("manual unban", got)
	}
	rt.ipLimitTick(ctx, t1.Add(time.Minute))
	if got := take(); len(got) != 0 || len(record().Exempt) != 1 {
		t.Fatal("exempt IP must not be banned again", got, record())
	}
	// 换网：旧IP 3分钟没有新连接，新IP在活动，新IP接替、旧IP被封。
	t2 := t1.Add(31 * time.Minute)
	online["client-client-one"] = []onlineIP{{"203.0.113.1", t2.Add(-3 * time.Minute)}, {"203.0.113.3", t2}}
	rt.ipLimitTick(ctx, t2)
	if got := take(); !reflect.DeepEqual(got, []string{"ban 203.0.113.1 18443 tcp"}) {
		t.Fatal("network switch", got)
	}
	// 到期自动解封。
	online["client-client-one"] = []onlineIP{{"203.0.113.3", t2.Add(11 * time.Minute)}}
	rt.ipLimitTick(ctx, t2.Add(11*time.Minute))
	if got := take(); !reflect.DeepEqual(got, []string{"unban 203.0.113.1 18443 tcp"}) || len(record().Bans) != 0 {
		t.Fatal("expiry", got)
	}
	// 取消上限后，已有封禁立即解除。
	online["client-client-one"] = []onlineIP{{"203.0.113.3", t2}, {"203.0.113.4", t2}}
	rt.ipLimitTick(ctx, t2.Add(12*time.Minute))
	take()
	setLimit("0")
	rt.ipLimitTick(ctx, t2.Add(13*time.Minute))
	if got := take(); !reflect.DeepEqual(got, []string{"unban 203.0.113.4 18443 tcp"}) || len(record().Bans) != 0 {
		t.Fatal("removing the limit should unban", got)
	}
	// 没有防火墙助手时只提示，不记录假封禁。
	setLimit("1")
	helperMissing = true
	rt.ipLimitTick(ctx, t2.Add(14*time.Minute))
	if len(record().Bans) != 0 || !strings.Contains(rt.iplimit.issue, "防火墙助手") {
		t.Fatal("missing helper", record(), rt.iplimit.issue)
	}
}

// 所有防火墙动作由内存规则表接管，不触碰宿主机规则。
func TestManualIPBansSharedScopeDurationAndFailures(t *testing.T) {
	s, rt, handler, cookie := subscriptionFixture(t)
	ctx := context.Background()
	previousBan, previousSample := runIPBan, sampleOnlineIPs
	t.Cleanup(func() { runIPBan, sampleOnlineIPs = previousBan, previousSample })
	rules := map[string]bool{}
	fail := false
	calls := 0
	runIPBan = func(_ context.Context, args ...string) (string, error) {
		calls++
		if fail {
			return "test-only failure", errors.New("fake failure")
		}
		if args[0] == "flush" {
			clear(rules)
			return "ok", nil
		}
		key := strings.Join(args[1:], " ")
		if args[0] == "ban" {
			rules[key] = true
		} else {
			delete(rules, key)
		}
		return "ok", nil
	}
	online := map[string][]onlineIP{"client-client-one": {{IP: "203.0.113.7", LastSeen: time.Now()}, {IP: "2001:db8::7", LastSeen: time.Now()}}}
	sampleOnlineIPs = func(context.Context, *Runtime) (map[string][]onlineIP, error) { return online, nil }
	doc, _ := s.ReadDocument(ctx, "workspace")
	var draft workspaceDraft
	_ = json.Unmarshal(doc.Data, &draft)
	draft.Clients[0].IPLimit = 0
	sibling := draft.Clients[0]
	sibling.ID = "shared-client"
	draft.Clients = append(draft.Clients, sibling)
	otherIn := draft.Inbounds[0]
	otherIn.ID = "other-in"
	otherIn.Port = 19443
	draft.Inbounds = append(draft.Inbounds, otherIn)
	other := sibling
	other.ID = "other-client"
	other.InboundID = otherIn.ID
	draft.Clients = append(draft.Clients, other)
	data, _ := json.Marshal(draft)
	_, err := s.SaveDocument(ctx, "workspace", doc.Revision, data)
	if err != nil {
		t.Fatal(err)
	}
	rt.ipLimitTick(ctx, time.Now())
	request := func(id, ip string, minutes int) ipBanRequest {
		doc, _ := s.ReadDocument(ctx, "workspace")
		_, revision, _ := readIPLimit(ctx, s)
		return ipBanRequest{ClientID: id, IP: ip, Minutes: minutes, Confirm: true, Revision: revision, WorkspaceRevision: doc.Revision}
	}
	call := func(action string, req ipBanRequest, want int, authenticated bool) map[string]json.RawMessage {
		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/api/runtime/iplimit/"+action, strings.NewReader(string(body)))
		r.Host, r.RemoteAddr = "127.0.0.1:5180", "127.0.0.1:1234"
		r.Header.Set("Origin", "http://127.0.0.1:5180")
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", action, w.Code, want, w.Body.String())
		}
		var result map[string]json.RawMessage
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	req := request("client-one", "203.0.113.7", 60)
	before := calls
	call("ban", req, 401, false)
	invalid := req
	invalid.Confirm = false
	call("ban", invalid, 400, true)
	invalid = req
	invalid.Minutes = -1
	call("ban", invalid, 400, true)
	invalid = req
	invalid.IP = "203.0.113.7;id"
	call("ban", invalid, 400, true)
	invalid = req
	invalid.IP = "203.0.113.99"
	call("ban", invalid, 422, true)
	invalid = req
	invalid.WorkspaceRevision--
	call("ban", invalid, 409, true)
	if calls != before {
		t.Fatal("rejected requests touched firewall")
	}
	result := call("ban", req, 200, true)
	var clients map[string]ipLimitClientView
	_ = json.Unmarshal(result["clients"], &clients)
	if len(clients["shared-client"].Banned) != 1 || len(clients["other-client"].Banned) != 0 {
		t.Fatal("shared port scope", clients)
	}
	rec, _, _ := readIPLimit(ctx, s)
	if len(rec.Bans) != 1 || !rec.Bans[0].Manual || rec.Bans[0].Permanent || time.Until(rec.Bans[0].Until) < 59*time.Minute || !rules["203.0.113.7 18443 tcp"] {
		t.Fatal("timed manual ban with no limit", rec, rules)
	}
	call("ban", req, 409, true) // 同一确认不能重复提交覆盖时长。
	call("ban", request("shared-client", "203.0.113.7", 0), 200, true)
	rec, _, _ = readIPLimit(ctx, s)
	if len(rec.Bans) != 1 || !rec.Bans[0].Permanent {
		t.Fatal("duration replacement", rec)
	}
	rt.iplimit = newIPLimiter()
	rt.ipLimitTick(ctx, time.Now().Add(8*24*time.Hour))
	if !rules["203.0.113.7 18443 tcp"] {
		t.Fatal("permanent ban lost after restart")
	}
	call("unban", request("client-one", "203.0.113.7", 0), 200, true)
	rec, _, _ = readIPLimit(ctx, s)
	if len(rec.Bans) != 0 || len(rec.Exempt) != 2 || len(rules) != 0 {
		t.Fatal("shared unban and exemptions", rec, rules)
	}
	// 重新封禁撤销共享端口的临时豁免；其他端口不受影响。
	call("ban", request("client-one", "203.0.113.7", 10), 200, true)
	rec, _, _ = readIPLimit(ctx, s)
	if len(rec.Exempt) != 0 {
		t.Fatal("manual ban left exemption")
	}
	fail = true
	call("unban", request("client-one", "203.0.113.7", 0), 502, true)
	rt.ipLimitTick(ctx, time.Now().Add(11*time.Minute))
	rec, _, _ = readIPLimit(ctx, s)
	if len(rec.Bans) != 1 || rt.iplimit.issue == "" || !rules["203.0.113.7 18443 tcp"] {
		t.Fatal("failed expiry forgotten", rec)
	}
	fail = false
	rt.ipLimitTick(ctx, time.Now().Add(11*time.Minute+10*time.Second))
	rec, _, _ = readIPLimit(ctx, s)
	if len(rec.Bans) != 0 || len(rules) != 0 {
		t.Fatal("expiry retry", rec, rules)
	}
	fail = true
	call("ban", request("client-one", "2001:db8::7", 30), 502, true)
	rec, _, _ = readIPLimit(ctx, s)
	if len(rec.Bans) != 0 {
		t.Fatal("failed ban recorded as successful")
	}
	fail = false
	call("ban", request("client-one", "2001:db8::7", 30), 200, true)
	if !rules["2001:db8::7 18443 tcp"] {
		t.Fatal("IPv6 ban missing")
	}
	// 修改入站端口后，旧端口封禁被撤销，不能移到新端口。
	doc, _ = s.ReadDocument(ctx, "workspace")
	_ = json.Unmarshal(doc.Data, &draft)
	draft.Inbounds[0].Port = 20443
	data, _ = json.Marshal(draft)
	_, err = s.SaveDocument(ctx, "workspace", doc.Revision, data)
	if err != nil {
		t.Fatal(err)
	}
	rt.ipLimitTick(ctx, time.Now())
	if len(rules) != 0 {
		t.Fatal("stale endpoint retained", rules)
	}
}

func TestManualIPBanSaveFailureRestoresRule(t *testing.T) {
	s, rt, _, _ := subscriptionFixture(t)
	previous := runIPBan
	t.Cleanup(func() { runIPBan = previous })
	rt.iplimit.online["client-one"] = []onlineIP{{IP: "203.0.113.7", LastSeen: time.Now()}}
	doc, _ := s.ReadDocument(context.Background(), "workspace")
	calls := []string{}
	runIPBan = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "ban" {
			_ = s.Close()
		}
		return "ok", nil
	}
	code, err := rt.changeIPBan(context.Background(), "ban", ipBanRequest{ClientID: "client-one", IP: "203.0.113.7", Minutes: 10, Confirm: true, WorkspaceRevision: doc.Revision})
	if code != 500 || err == nil || !strings.Contains(err.Error(), "已恢复原规则") || !reflect.DeepEqual(calls, []string{"ban 203.0.113.7 18443 tcp", "unban 203.0.113.7 18443 tcp"}) {
		t.Fatal(code, err, calls)
	}
}
