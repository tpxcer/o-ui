package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/monitor"
	"independent-panel/internal/store"
)

func TestServerPlanValidation(t *testing.T) {
	valid := `{"node":"LA","name":"KVM 2G","resources":"RAM 2 GB, Disk 40 GB","quotaGB":2000,"resetDay":31,"billing":"both","interfaces":["eth0"]}`
	if _, err := validateServerPlan(json.RawMessage(valid)); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"未知字段":  `{"node":"","name":"","resources":"","quotaGB":0,"resetDay":1,"billing":"both","interfaces":[],"x":1}`,
		"缺网卡数组": `{"node":"","name":"","resources":"","quotaGB":0,"resetDay":1,"billing":"both"}`,
		"重置日":   `{"node":"","name":"","resources":"","quotaGB":0,"resetDay":32,"billing":"both","interfaces":[]}`,
		"负额度":   `{"node":"","name":"","resources":"","quotaGB":-1,"resetDay":1,"billing":"both","interfaces":[]}`,
		"超额度":   `{"node":"","name":"","resources":"","quotaGB":1000001,"resetDay":1,"billing":"both","interfaces":[]}`,
		"计费方向":  `{"node":"","name":"","resources":"","quotaGB":0,"resetDay":1,"billing":"down","interfaces":[]}`,
		"网卡名":   `{"node":"","name":"","resources":"","quotaGB":0,"resetDay":1,"billing":"both","interfaces":["eth0;rm"]}`,
		"重复网卡":  `{"node":"","name":"","resources":"","quotaGB":0,"resetDay":1,"billing":"both","interfaces":["eth0","eth0"]}`,
		"控制字符":  `{"node":"a\u0007","name":"","resources":"","quotaGB":0,"resetDay":1,"billing":"both","interfaces":[]}`,
		"过长":    `{"node":"` + strings.Repeat("长", 121) + `","name":"","resources":"","quotaGB":0,"resetDay":1,"billing":"both","interfaces":[]}`,
	} {
		if _, err := validateServerPlan(json.RawMessage(text)); err == nil {
			t.Fatal(name, "应拒绝")
		}
	}
}

func TestPlanPeriodBoundaries(t *testing.T) {
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.Local) }
	for _, v := range []struct {
		now         time.Time
		day         int
		start, next time.Time
	}{
		{at(2026, 10, 19, 12), 1, at(2026, 10, 1, 0), at(2026, 11, 1, 0)},
		{at(2026, 10, 1, 0), 1, at(2026, 10, 1, 0), at(2026, 11, 1, 0)},
		{at(2026, 3, 5, 0), 31, at(2026, 2, 28, 0), at(2026, 3, 31, 0)},
		{at(2026, 2, 28, 1), 31, at(2026, 2, 28, 0), at(2026, 3, 31, 0)},
		{at(2027, 1, 10, 0), 15, at(2026, 12, 15, 0), at(2027, 1, 15, 0)},
	} {
		start := periodStart(v.now, v.day)
		if !start.Equal(v.start) || !nextReset(start, v.day).Equal(v.next) {
			t.Fatal(v.now, v.day, start, nextReset(start, v.day))
		}
	}
	if next := nextReset(at(2026, 10, 1, 0), 25); !next.Equal(at(2026, 10, 25, 0)) {
		t.Fatal("修改重置日后下次重置应为当月新重置日", next)
	}
}

type fakeCounters struct {
	at   time.Time
	boot uint64
	list []monitor.Counter
}

func (f *fakeCounters) read() (time.Time, uint64, []monitor.Counter) {
	return f.at, f.boot, append([]monitor.Counter(nil), f.list...)
}

func TestPlanAccountObserve(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	now := start.Add(48 * time.Hour)
	f := &fakeCounters{at: now, boot: uint64(start.Add(-time.Hour).Unix()), list: []monitor.Counter{{Name: "eth0", ID: "eth0:2", Sent: 100, Received: 1000}, {Name: "docker0", ID: "docker0:3", Sent: 5, Received: 5}}}
	a := &planAccount{counters: f.read, plan: &serverPlan{ResetDay: 1, Billing: "both", Interfaces: []string{"eth0"}}, traffic: hostTraffic{PeriodStart: start, Inexact: true}}
	step := func(d time.Duration, sent, received uint64) {
		now = now.Add(d)
		f.at = now
		f.list[0].Sent, f.list[0].Received = sent, received
		a.observe(now)
	}
	a.observe(now)
	if a.traffic.Sent != 0 || a.traffic.Received != 0 || len(a.traffic.Counters) != 1 {
		t.Fatal("首次观察只建立基线且只计量指定网卡", a.traffic)
	}
	a.traffic.Inexact = false
	step(10*time.Second, 400, 1600)
	if a.traffic.Sent != 300 || a.traffic.Received != 600 || a.traffic.Inexact {
		t.Fatal("差值累计错误", a.traffic)
	}
	for billing, want := range map[string]uint64{"both": 900, "upload": 300, "max": 600} {
		if got := usedBytes(a.traffic, billing); got != want {
			t.Fatal(billing, got)
		}
	}
	a.traffic.Adjust = -1000
	if usedBytes(a.traffic, "both") != 0 {
		t.Fatal("负校准不能出现负用量")
	}
	a.traffic.Adjust = 0
	f.at = now.Add(-time.Minute)
	a.observe(now.Add(10 * time.Second))
	if a.traffic.Sent != 300 {
		t.Fatal("过期采样不应累计")
	}
	step(10*time.Second, 50, 1700)
	if a.traffic.Sent != 350 || a.traffic.Received != 2300 || !a.traffic.Inexact {
		t.Fatal("计数回退应按重置后计数累计并标记不精确", a.traffic)
	}
	a.traffic.Inexact = false
	f.boot = uint64(now.Add(5 * time.Second).Unix())
	step(10*time.Second, 70, 30)
	if a.traffic.Sent != 420 || a.traffic.Received != 2330 || !a.traffic.Inexact {
		t.Fatal("重新开机后应计入本次开机以来计数并标记不精确", a.traffic)
	}
	a.traffic.Inexact = false
	f.list[0].ID = "eth0:9"
	step(10*time.Second, 80, 40)
	if a.traffic.Sent != 420 || !a.traffic.Inexact {
		t.Fatal("同名重建网卡只建立基线", a.traffic)
	}
	a.plan.Interfaces = []string{"wg9"}
	a.observe(now.Add(time.Second))
	if a.issue == "" {
		t.Fatal("计量网卡缺失应提示")
	}
	a.plan.Interfaces = []string{"eth0"}

	boundary := time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)
	now, f.at = boundary.Add(-5*time.Second), boundary.Add(-5*time.Second)
	a.observe(now)
	a.traffic.Inexact = false
	step(10*time.Second, 180, 140)
	if !a.traffic.PeriodStart.Equal(boundary) || a.traffic.Sent != 100 || a.traffic.Received != 100 || a.traffic.Inexact {
		t.Fatal("连续运行跨重置日应归零并只计新周期", a.traffic)
	}
	next := time.Date(2026, 12, 1, 0, 0, 0, 0, time.Local)
	step(next.Sub(now)+time.Hour, 280, 240)
	if !a.traffic.PeriodStart.Equal(next) || !a.traffic.Inexact {
		t.Fatal("停机跨重置日应标记不精确", a.traffic)
	}
}

func planTestServer(t *testing.T) (*store.Store, string, func(string, string, any) *httptest.ResponseRecorder, *Runtime) {
	t.Helper()
	m, err := core.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	session, err := s.Initialize(context.Background(), "planqa", "test-only-passphrase-924")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(m, s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(m, s, t.TempDir(), []string{"127.0.0.1:5180"}, rt)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		var r *http.Request
		if body == nil {
			r = httptest.NewRequest(method, path, nil)
		} else {
			data, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, strings.NewReader(string(data)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://127.0.0.1:5180")
		}
		r.RemoteAddr, r.Host = "127.0.0.1:1234", "127.0.0.1:5180"
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	return s, dir, call, rt
}

func TestServerPlanAPI(t *testing.T) {
	s, _, call, rt := planTestServer(t)
	var status struct {
		Revision int64       `json:"revision"`
		Data     *serverPlan `json:"data"`
		Usage    *planUsage  `json:"usage"`
		Issue    string      `json:"issue"`
	}
	w := call("GET", "/api/system/plan", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Data != nil || status.Usage != nil || status.Revision != 0 {
		t.Fatal("未配置时不应有用量", w.Code, w.Body.String())
	}
	plan := map[string]any{"node": "LA", "name": "KVM", "resources": "RAM 2 GB", "quotaGB": 2000, "resetDay": 1, "billing": "both", "interfaces": []string{}}
	if w = call("PUT", "/api/system/plan", map[string]any{"revision": 0, "data": plan, "usedGB": -1}); w.Code != 400 {
		t.Fatal("非法校准值应拒绝", w.Code)
	}
	if w = call("PUT", "/api/system/plan", map[string]any{"revision": 5, "data": plan}); w.Code != 409 {
		t.Fatal("旧修订应冲突", w.Code)
	}
	if w = call("PUT", "/api/system/plan", map[string]any{"revision": 0, "data": plan}); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if status.Revision != 1 || status.Usage == nil || !status.Usage.Inexact || status.Usage.Quota != "2000000000000" || !status.Usage.NextReset.After(status.Usage.PeriodStart) {
		t.Fatal("中途开始统计应标不精确并返回周期", w.Body.String())
	}
	if w = call("PUT", "/api/system/plan", map[string]any{"revision": 1, "data": plan, "usedGB": 208.6}); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if used, err := strconv.ParseUint(status.Usage.Used, 10, 64); err != nil || status.Usage.Inexact || used < 208600000000 || used > 208600000000+1e9 {
		t.Fatal("校准后应以校准值为起点", w.Body.String())
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	docs, err := s.ReadDocuments(context.Background(), []string{"server-plan", "host-traffic"})
	if err != nil || docs["server-plan"].Revision != 2 || docs["host-traffic"].Revision < 2 {
		t.Fatal("套餐与周期应原子保存并在关闭时持久化", err, docs)
	}
	reopened := openPlanAccount(s, func() (time.Time, uint64, []monitor.Counter) { return time.Time{}, 0, nil })
	defer reopened.close()
	if reopened.plan == nil || reopened.plan.Node != "LA" || reopened.traffic.Adjust == 0 || reopened.traffic.Inexact {
		t.Fatal("重开后应恢复套餐与校准", reopened.plan, reopened.traffic)
	}
}

func TestServerPlanCorruptDocuments(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.SaveDocument(ctx, "server-plan", 0, json.RawMessage(`{"node":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveDocument(ctx, "host-traffic", 0, json.RawMessage(`{"sent":"x"}`)); err != nil {
		t.Fatal(err)
	}
	a := openPlanAccount(s, func() (time.Time, uint64, []monitor.Counter) { return time.Time{}, 0, nil })
	defer a.close()
	if a.plan != nil || a.issue == "" || !a.traffic.Inexact {
		t.Fatal("损坏记录不应阻止启动，也不应冒充有效套餐", a.plan, a.issue, a.traffic)
	}
}

func TestPublicInfoLookup(t *testing.T) {
	if v, err := parseIPInfo(strings.NewReader(`{"ip":"203.0.113.9","hostname":"vps.example.net","city":"Los Angeles","region":"California","country":"US","org":"AS64500 Example\u0007 Net"}`), "tcp4"); err != nil || v.IP != "203.0.113.9" || v.Hostname != "vps.example.net" || v.Location != "Los Angeles, California, US" || v.Country != "US" || v.Org != "AS64500 Example Net" {
		t.Fatal(v, err)
	}
	if v, err := parseIPInfo(strings.NewReader(`{"ip":"2001:db8::1","hostname":"2001:db8::1","country":"U1"}`), "tcp6"); err != nil || v.Hostname != "" || v.Country != "" {
		t.Fatal("PTR等于地址时不重复显示", v, err)
	}
	for _, body := range []string{`{"ip":"2001:db8::1"}`, `{"ip":"bad"}`, `not json`} {
		if _, err := parseIPInfo(strings.NewReader(body), "tcp4"); err == nil {
			t.Fatal("地址族不符或无效应拒绝", body)
		}
	}
	var calls atomic.Int32
	p := &publicLookup{fetch: func(_ context.Context, network string) (*publicAddress, error) {
		calls.Add(1)
		if network == "tcp6" {
			return nil, errors.New("no ipv6")
		}
		return &publicAddress{IP: "203.0.113.9"}, nil
	}}
	if info := p.get(); info.IPv4 == nil || info.IPv6 != nil || len(info.Issues) != 1 {
		t.Fatal(info)
	}
	p.get()
	if calls.Load() != 2 {
		t.Fatal("成功结果应缓存", calls.Load())
	}
	p.fetch = func(context.Context, string) (*publicAddress, error) { return nil, errors.New("offline") }
	p.expires = time.Time{}
	if info := p.get(); info.IPv4 != nil || len(info.Issues) != 2 || time.Until(p.expires) > publicFailureTTL {
		t.Fatal("失败只短暂缓存", info, p.expires)
	}

	_, _, call, rt := planTestServer(t)
	defer rt.Close()
	rt.public.fetch = func(_ context.Context, network string) (*publicAddress, error) {
		if network == "tcp4" {
			return &publicAddress{IP: "192.0.2.10", Location: "测试"}, nil
		}
		return nil, errors.New("no ipv6")
	}
	w := call("GET", "/api/system/public", nil)
	var info publicInfo
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil || info.IPv4 == nil || info.IPv4.IP != "192.0.2.10" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
}
