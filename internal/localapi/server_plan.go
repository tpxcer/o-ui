package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"independent-panel/internal/monitor"
	"independent-panel/internal/store"
)

const (
	planAccountInterval = 10 * time.Second
	planSaveInterval    = time.Minute
)

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)

// 服务器套餐用于首页显示与月用量统计。mode：off关闭、provider从服务商接口拉取、manual手动填写（旧数据没有mode，按manual）。
// 手动字段在各模式下都保留；providerKey只写不读，留空保存时沿用已保存的密钥。
type serverPlan struct {
	Mode         string   `json:"mode,omitempty"`
	Node         string   `json:"node"`
	Name         string   `json:"name"`
	Resources    string   `json:"resources"`
	QuotaGB      float64  `json:"quotaGB"`
	ResetDay     int      `json:"resetDay"`
	Billing      string   `json:"billing"`
	Interfaces   []string `json:"interfaces"`
	ProviderURL  string   `json:"providerURL,omitempty"`
	ProviderVEID string   `json:"providerVEID,omitempty"`
	ProviderKey  string   `json:"providerKey,omitempty"`
}

var providerVEID = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

// hostTraffic按网卡计数差值累计当前周期；Counters为上次观察的原始值，用于跨面板重启续算。
type hostTraffic struct {
	PeriodStart time.Time            `json:"periodStart"`
	Boot        uint64               `json:"boot"`
	Counters    map[string][2]uint64 `json:"counters"`
	Sent        uint64               `json:"sent,string"`
	Received    uint64               `json:"received,string"`
	Adjust      int64                `json:"adjust,string"`
	Inexact     bool                 `json:"inexact"`
	At          time.Time            `json:"at"`
}

type planUsage struct {
	PeriodStart time.Time `json:"periodStart"`
	NextReset   time.Time `json:"nextReset"`
	Sent        string    `json:"sent"`
	Received    string    `json:"received"`
	Used        string    `json:"used"`
	Quota       string    `json:"quota"`
	Inexact     bool      `json:"inexact"`
	At          time.Time `json:"at"`
}

type planAccount struct {
	mu        sync.Mutex
	state     *store.Store
	counters  func() (time.Time, uint64, []monitor.Counter)
	planRev   int64
	plan      *serverPlan
	trafficRv int64
	traffic   hostTraffic
	dirty     bool
	saved     time.Time
	issue     string
	cancel    context.CancelFunc
	done      chan struct{}
}

func validateServerPlan(data json.RawMessage) (serverPlan, error) {
	var p serverPlan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || decoder.Decode(&struct{}{}) != io.EOF || p.Interfaces == nil {
		return p, i18n.New("套餐设置字段不完整或格式无效", "Plan settings are incomplete or invalid")
	}
	for _, text := range []string{p.Node, p.Name, p.Resources} {
		if len([]rune(text)) > 120 || strings.ContainsFunc(text, func(r rune) bool { return r < 32 || r == 127 }) {
			return p, i18n.New("节点、套餐名称和资源说明各限120字，且不能包含控制字符", "Node, plan name and resources are limited to 120 characters each without control characters")
		}
	}
	if math.IsNaN(p.QuotaGB) || p.QuotaGB < 0 || p.QuotaGB > 1000000 {
		return p, i18n.New("月流量上限须为0至1000000 GB，0表示不限", "Monthly quota must be 0–1000000 GB (0 means unlimited)")
	}
	if p.ResetDay < 1 || p.ResetDay > 31 {
		return p, i18n.New("每月重置日须为1至31；当月没有该日期时按月末重置", "Monthly reset day must be 1–31; months without that day reset on their last day")
	}
	if !slices.Contains([]string{"both", "upload", "max"}, p.Billing) {
		return p, i18n.New("计费方向无效", "Invalid billing direction")
	}
	if len(p.Interfaces) > 8 {
		return p, i18n.New("计量网卡最多8个", "At most 8 metered interfaces")
	}
	if !slices.Contains([]string{"", "off", "provider", "manual"}, p.Mode) {
		return p, i18n.New("套餐方式无效", "Invalid plan mode")
	}
	if len(p.ProviderURL) > 500 || strings.ContainsFunc(p.ProviderURL, func(r rune) bool { return r <= 32 || r == 127 }) || (p.ProviderURL != "" && !strings.HasPrefix(p.ProviderURL, "https://") && !strings.HasPrefix(p.ProviderURL, "http://")) {
		return p, i18n.New("拉取链接须以 http:// 或 https:// 开头，最长500字符，不含空格", "The provider URL must start with http:// or https://, up to 500 characters without spaces")
	}
	if !providerVEID.MatchString(p.ProviderVEID) || len(p.ProviderKey) > 200 || strings.ContainsFunc(p.ProviderKey, func(r rune) bool { return r <= 32 || r == 127 }) {
		return p, i18n.New("VEID 只能含字母、数字、下划线和短横线；API KEY 最长200字符且不含空格", "VEID may contain only letters, digits, underscores and hyphens; API KEY is up to 200 characters without spaces")
	}
	seen := map[string]bool{}
	for _, name := range p.Interfaces {
		if !interfaceName.MatchString(name) || seen[name] {
			return p, i18n.New("计量网卡名称无效或重复", "Invalid or duplicate metered interface name")
		}
		seen[name] = true
	}
	return p, nil
}

func planBoundary(year int, month time.Month, day int) time.Time {
	if last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.Local).Day(); day > last {
		day = last
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.Local)
}

// periodStart按服务器本地时区返回不晚于now的最近重置时刻。
func periodStart(now time.Time, day int) time.Time {
	now = now.In(time.Local)
	start := planBoundary(now.Year(), now.Month(), day)
	if start.After(now) {
		start = planBoundary(now.Year(), now.Month()-1, day)
	}
	return start
}

// nextReset返回start之后的第一个重置时刻；修改重置日后周期起点可能不在该日。
func nextReset(start time.Time, day int) time.Time {
	start = start.In(time.Local)
	if next := planBoundary(start.Year(), start.Month(), day); next.After(start) {
		return next
	}
	return planBoundary(start.Year(), start.Month()+1, day)
}

func usedBytes(t hostTraffic, billing string) uint64 {
	used := t.Sent
	switch billing {
	case "both":
		if math.MaxUint64-t.Sent < t.Received {
			return math.MaxUint64
		}
		used = t.Sent + t.Received
	case "max":
		used = max(t.Sent, t.Received)
	}
	if t.Adjust < 0 {
		if uint64(-t.Adjust) > used {
			return 0
		}
		return used - uint64(-t.Adjust)
	}
	if math.MaxUint64-used < uint64(t.Adjust) {
		return math.MaxUint64
	}
	return used + uint64(t.Adjust)
}

func addSaturating(a, b uint64) uint64 {
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	return a + b
}

// observe把最近网卡计数并入当前周期；重启、计数回退或停机跨周期时标记不精确，不补算未知流量。
func (a *planAccount) observe(now time.Time) {
	if a.plan == nil {
		return
	}
	t := &a.traffic
	if start := periodStart(now, a.plan.ResetDay); start.After(t.PeriodStart) {
		gap := t.At.IsZero() || now.Sub(t.At) > 3*planAccountInterval || t.At.After(start)
		t.PeriodStart, t.Sent, t.Received, t.Adjust, t.Inexact = start, 0, 0, 0, gap
		a.dirty, a.saved = true, time.Time{}
	}
	at, boot, all := a.counters()
	if boot == 0 || len(all) == 0 || now.Sub(at) > 3*monitor.Interval {
		return
	}
	current := map[string][2]uint64{}
	for _, c := range all {
		if len(a.plan.Interfaces) == 0 || slices.Contains(a.plan.Interfaces, c.Name) {
			current[c.ID] = [2]uint64{c.Sent, c.Received}
		}
	}
	if len(current) == 0 {
		a.issue = i18n.T("计量网卡不存在或未启用，本周期暂停累计", "Metered interface missing or down; accumulation paused for this period")
		return
	}
	a.issue = ""
	if t.Boot != boot {
		// 开机时刻变化：本次开机以来的计数均为新流量，上次开机末尾未观察部分未知。
		if t.Boot != 0 {
			if int64(boot) >= t.At.Unix() {
				for _, v := range current {
					t.Sent, t.Received = addSaturating(t.Sent, v[0]), addSaturating(t.Received, v[1])
				}
			}
			t.Inexact = true
		}
	} else {
		for id, v := range current {
			old, ok := t.Counters[id]
			switch {
			case !ok:
				t.Inexact = true
			case v[0] < old[0] || v[1] < old[1]:
				t.Sent, t.Received, t.Inexact = addSaturating(t.Sent, v[0]), addSaturating(t.Received, v[1]), true
			default:
				t.Sent, t.Received = addSaturating(t.Sent, v[0]-old[0]), addSaturating(t.Received, v[1]-old[1])
			}
		}
	}
	t.Boot, t.Counters, t.At = boot, current, now.UTC()
	a.dirty = true
}

func (a *planAccount) persist(ctx context.Context, force bool) {
	if !a.dirty || (!force && time.Since(a.saved) < planSaveInterval) {
		return
	}
	data, err := json.Marshal(a.traffic)
	if err != nil {
		return
	}
	doc, err := a.state.SaveDocument(ctx, "host-traffic", a.trafficRv, data)
	if err != nil {
		a.issue = i18n.T("月用量保存失败，将在下次采样时重试", "Failed to save monthly usage; will retry at the next sample")
		return
	}
	a.trafficRv, a.dirty, a.saved = doc.Revision, false, time.Now()
}

func openPlanAccount(state *store.Store, counters func() (time.Time, uint64, []monitor.Counter)) *planAccount {
	a := &planAccount{state: state, counters: counters, done: make(chan struct{})}
	ctx := context.Background()
	docs, err := state.ReadDocuments(ctx, []string{"server-plan", "host-traffic"})
	if err != nil {
		a.issue = i18n.T("无法读取套餐设置", "Cannot read plan settings")
	} else {
		a.planRev, a.trafficRv = docs["server-plan"].Revision, docs["host-traffic"].Revision
		if raw := docs["server-plan"].Data; string(raw) != "null" {
			if p, err := validateServerPlan(raw); err == nil {
				a.plan = &p
			} else {
				a.issue = i18n.T("套餐设置损坏，请重新保存", "Plan settings are corrupted; please save them again")
			}
		}
		if raw := docs["host-traffic"].Data; string(raw) != "null" && json.Unmarshal(raw, &a.traffic) != nil {
			a.traffic, a.issue = hostTraffic{Inexact: true}, i18n.T("月用量记录损坏，已从现在重新累计", "Monthly usage record was corrupted; accumulation restarted from now")
		}
	}
	loop, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(planAccountInterval)
		defer ticker.Stop()
		for {
			select {
			case <-loop.Done():
				return
			case <-ticker.C:
				a.mu.Lock()
				a.observe(time.Now())
				a.persist(loop, false)
				a.mu.Unlock()
			}
		}
	}()
	return a
}

func (a *planAccount) close() {
	a.cancel()
	<-a.done
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observe(time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a.persist(ctx, true)
}

func (a *planAccount) status() map[string]any {
	var view *serverPlan
	if a.plan != nil {
		copied := *a.plan
		copied.ProviderKey = ""
		view = &copied
	}
	result := map[string]any{"revision": a.planRev, "data": view, "hasProviderKey": a.plan != nil && a.plan.ProviderKey != "", "usage": nil, "issue": a.issue}
	if a.plan != nil && !a.traffic.PeriodStart.IsZero() {
		quota := "0"
		if a.plan.QuotaGB > 0 {
			quota = strconv.FormatUint(uint64(math.Round(a.plan.QuotaGB*1e9)), 10)
		}
		result["usage"] = planUsage{
			PeriodStart: a.traffic.PeriodStart, NextReset: nextReset(a.traffic.PeriodStart, a.plan.ResetDay),
			Sent: strconv.FormatUint(a.traffic.Sent, 10), Received: strconv.FormatUint(a.traffic.Received, 10),
			Used: strconv.FormatUint(usedBytes(a.traffic, a.plan.Billing), 10), Quota: quota, Inexact: a.traffic.Inexact, At: a.traffic.At,
		}
	}
	return result
}

// save原子写入套餐与当前周期；中途开始或改变重置日、计费方向、网卡且未校准时标记不精确。
func (a *planAccount) save(ctx context.Context, revision int64, p serverPlan, usedGB *float64) error {
	if revision != a.planRev {
		return store.ErrConflict
	}
	now := time.Now()
	a.observe(now)
	next := a.traffic
	if a.plan == nil || next.PeriodStart.IsZero() {
		next = hostTraffic{PeriodStart: periodStart(now, p.ResetDay), Inexact: true}
	} else {
		if p.ResetDay != a.plan.ResetDay {
			start := periodStart(now, p.ResetDay)
			if start.After(next.PeriodStart) {
				next.Sent, next.Received = 0, 0
			}
			next.PeriodStart, next.Adjust, next.Inexact = start, 0, true
		}
		if p.Billing != a.plan.Billing {
			next.Adjust, next.Inexact = 0, true
		}
		if !slices.Equal(p.Interfaces, a.plan.Interfaces) {
			next.Boot, next.Counters, next.Adjust, next.Inexact = 0, nil, 0, true
		}
	}
	if usedGB != nil {
		next.Adjust = 0
		base := min(usedBytes(next, p.Billing), math.MaxInt64)
		next.Adjust, next.Inexact = int64(math.Round(*usedGB*1e9))-int64(base), false
	}
	planData, _ := json.Marshal(p)
	trafficData, _ := json.Marshal(next)
	expected := map[string]store.Document{"server-plan": {Revision: a.planRev}, "host-traffic": {Revision: a.trafficRv}}
	if err := a.state.ReplaceDocuments(ctx, expected, map[string]json.RawMessage{"server-plan": planData, "host-traffic": trafficData}); err != nil {
		return err
	}
	a.plan, a.traffic, a.planRev, a.trafficRv, a.dirty, a.saved = &p, next, a.planRev+1, a.trafficRv+1, false, time.Now()
	a.observe(now)
	return nil
}

func registerServerPlan(mux *http.ServeMux, rt *Runtime) {
	mux.HandleFunc("GET /api/system/plan", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil || rt.plan == nil {
			respond(w, 503, map[string]string{"error": i18n.T("套餐统计尚未启动", "Plan accounting is not running")})
			return
		}
		rt.plan.mu.Lock()
		rt.plan.observe(time.Now())
		result, current := rt.plan.status(), rt.plan.plan
		rt.plan.mu.Unlock()
		respond(w, 200, withProvider(r.Context(), result, current))
	})
	mux.HandleFunc("PUT /api/system/plan", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil || rt.plan == nil {
			respond(w, 503, map[string]string{"error": i18n.T("套餐统计尚未启动", "Plan accounting is not running")})
			return
		}
		var request struct {
			Revision int64           `json:"revision"`
			Data     json.RawMessage `json:"data"`
			UsedGB   *float64        `json:"usedGB"`
		}
		if !readJSON(w, r, &request, 8192) {
			return
		}
		p, err := validateServerPlan(request.Data)
		if err == nil && request.UsedGB != nil && (math.IsNaN(*request.UsedGB) || *request.UsedGB < 0 || *request.UsedGB > 1000000) {
			err = i18n.New("本周期已用校准值须为0至1000000 GB", "Usage calibration must be 0–1000000 GB")
		}
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		rt.plan.mu.Lock()
		if p.ProviderKey == "" && rt.plan.plan != nil {
			p.ProviderKey = rt.plan.plan.ProviderKey
		}
		if err := rt.plan.save(r.Context(), request.Revision, p, request.UsedGB); err != nil {
			rt.plan.mu.Unlock()
			code, message := 500, i18n.T("保存套餐设置失败，未改变已保存数据", "Failed to save plan settings; saved data unchanged")
			if errors.Is(err, store.ErrConflict) {
				code, message = 409, store.ErrConflict.Error()
			}
			respond(w, code, map[string]string{"error": message})
			return
		}
		result, current := rt.plan.status(), rt.plan.plan
		rt.plan.mu.Unlock()
		respond(w, 200, withProvider(r.Context(), result, current))
	})
}

// withProvider在锁外拉取服务商信息（有缓存），失败时只附带错误，不影响套餐设置本身。
func withProvider(ctx context.Context, result map[string]any, p *serverPlan) map[string]any {
	if p == nil || p.Mode != "provider" {
		return result
	}
	if info, err := fetchProvider(ctx, p.ProviderURL, p.ProviderVEID, p.ProviderKey); err != nil {
		result["providerError"] = err.Error()
	} else {
		result["provider"] = info
	}
	return result
}
