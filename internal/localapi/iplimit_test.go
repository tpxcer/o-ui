package localapi

import (
	"context"
	"encoding/json"
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
	call("POST", "/api/runtime/iplimit/unban", `{"clientId":"client-one","ip":"203.0.113.9"}`, 404)
	call("POST", "/api/runtime/iplimit/unban", `{"clientId":"client-one","ip":"203.0.113.2"}`, 200)
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
