package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"independent-panel/internal/i18n"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/engine"
	"independent-panel/internal/traffic"
)

const trafficInterval = 2 * time.Second

type clientLimit struct {
	Quota     uint64 `json:"quota,string"`
	ExpiresAt *int64 `json:"expiresAt"`
}
type runtimePolicy struct {
	Clients map[string]clientLimit `json:"clients"`
}

func quotaBytes(gb float64) uint64 { return uint64(math.Ceil(gb * 1e9)) }

func policyFor(draft workspaceDraft) runtimePolicy {
	policy := runtimePolicy{Clients: map[string]clientLimit{}}
	for _, c := range draft.Clients {
		policy.Clients[c.ID] = clientLimit{Quota: quotaBytes(c.QuotaGB), ExpiresAt: c.ExpiresAt}
	}
	return policy
}

func blocked(limit clientLimit, used traffic.Count, now time.Time) bool {
	return (limit.ExpiresAt != nil && now.UnixMilli() >= *limit.ExpiresAt) || (limit.Quota > 0 && used.Total() >= limit.Quota)
}

func (rt *Runtime) ledger(ctx context.Context) (traffic.Ledger, int64, error) {
	doc, err := rt.state.ReadDocument(ctx, "traffic")
	if err != nil {
		return traffic.Ledger{}, 0, err
	}
	if doc.Revision == 0 && string(doc.Data) == "null" {
		return traffic.Empty(), 0, nil
	}
	var ledger traffic.Ledger
	if json.Unmarshal(doc.Data, &ledger) != nil {
		return ledger, 0, i18n.New("流量账本损坏，拒绝重置用量", "Traffic ledger is corrupted; refusing to reset usage")
	}
	if ledger.Totals == nil || ledger.Last == nil || !traffic.ValidPeriods(ledger) || (ledger.Active && (ledger.Epoch == "" || ledger.SampledAt == nil)) {
		return ledger, 0, i18n.New("流量账本结构无效", "Invalid traffic ledger structure")
	}
	return ledger, doc.Revision, nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, i18n.New("统计响应过大", "Statistics response too large")
	}
	return b.Buffer.Write(p)
}

func queryTraffic(ctx context.Context, c engine.Candidate) (map[string]traffic.Count, error) {
	data, err := queryTrafficData(ctx, c)
	if err != nil {
		return nil, err
	}
	return traffic.Parse(data)
}

func queryTrafficData(ctx context.Context, c engine.Candidate) ([]byte, error) {
	if !strings.HasPrefix(c.StatsAddress, "unix:///") {
		return nil, i18n.New("统计接口未配置为受限本机套接字", "Statistics API is not configured as a restricted local socket")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, c.Binary, "api", "statsquery", "--server="+c.StatsAddress, "-timeout=2", "-pattern=>>>")
	command.Dir = filepath.Dir(c.Binary)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	var output boundedOutput
	command.Stdout = &output
	if command.Run() != nil {
		return nil, i18n.New("无法采集官方核心流量，未把失败当作零用量", "Cannot sample core traffic; the failure is not counted as zero usage")
	}
	return output.Bytes(), nil
}

func (rt *Runtime) checkpoint(ctx context.Context, c engine.Candidate, started time.Time) error {
	if c.StatsAddress == "" {
		return nil
	}
	data, err := queryTrafficData(ctx, c)
	if err != nil {
		return err
	}
	counts, err := traffic.Parse(data)
	if err != nil {
		return err
	}
	inboundCounts, err := traffic.ParseInbounds(data)
	if err != nil {
		return err
	}
	ledger, revision, err := rt.ledger(ctx)
	if err != nil {
		return err
	}
	// 文件快照与启动时间共同识别实例，失败回退重启同一文件也不会复用旧基线。
	epoch, now := fmt.Sprintf("%s:%d", c.File, started.UnixNano()), time.Now()
	ledger, err = traffic.Merge(ledger, epoch, counts, now)
	if err != nil {
		return err
	}
	inboundLedger := traffic.Empty()
	if ledger.Inbounds != nil {
		inboundLedger = *ledger.Inbounds
	}
	inboundLedger, err = traffic.Merge(inboundLedger, epoch, inboundCounts, now)
	if err != nil {
		return err
	}
	ledger.Inbounds = &inboundLedger
	data, err = json.Marshal(ledger)
	if err != nil {
		return err
	}
	if _, err = rt.state.SaveDocument(ctx, "traffic", revision, data); err != nil {
		return i18n.New("流量检查点保存失败", "Failed to save the traffic checkpoint")
	}
	rt.business.observe(epoch, counts, ledger.Totals, now)
	return nil
}

func (rt *Runtime) checkpointCurrent(ctx context.Context) error {
	c, running := rt.engine.Current()
	if !running {
		if rt.engine.Status().UnexpectedExit {
			rt.issue = i18n.T("自有核心异常退出，未自动重启；已存累计保留，未采集尾流未知，请检查后重新预检并应用", "The core exited unexpectedly and was not restarted; saved totals kept, unmeasured traffic unknown, check and run preflight and apply again")
		}
		return nil
	}
	status := rt.engine.Status()
	if status.StartedAt == nil {
		return i18n.New("核心实例已改变", "Core instance changed")
	}
	return rt.checkpoint(ctx, c, *status.StartedAt)
}

func (rt *Runtime) stop(ctx context.Context) error {
	unexpected := rt.engine.Status().UnexpectedExit
	err := rt.checkpointCurrent(ctx)
	stopErr := rt.engine.Stop()
	if err != nil {
		rt.issue = i18n.T("核心已停止，但最后流量采集失败；已存累计保留，未采集尾流未知", "Core stopped but the final traffic sample failed; saved totals kept, unmeasured traffic unknown")
	}
	if stopErr == nil {
		if saveErr := rt.markStopped(context.Background(), err != nil || unexpected); saveErr != nil {
			rt.issue = i18n.T("核心已停止，但停止状态保存失败；累计保留，未采集尾流未知", "Core stopped but saving the stopped state failed; totals kept, unmeasured traffic unknown")
		}
	}
	return stopErr
}

func (rt *Runtime) markStopped(ctx context.Context, incomplete bool) error {
	ledger, revision, err := rt.ledger(ctx)
	if err != nil {
		return err
	}
	if ledger.Inbounds != nil {
		ledger.Inbounds.Active = false
		ledger.Inbounds.Incomplete = ledger.Inbounds.Incomplete || incomplete
	}
	ledger.Active = false
	ledger.Incomplete = ledger.Incomplete || incomplete
	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	_, err = rt.state.SaveDocument(ctx, "traffic", revision, data)
	return err
}

func (rt *Runtime) apply(ctx context.Context, c engine.Candidate) error {
	c.Diagnostics = rt.logs
	record, err := rt.snapshot(c)
	if err != nil {
		return err
	}
	previous, err := rt.state.ReadDocument(ctx, appliedDocument)
	if err != nil {
		return i18n.New("无法读取运行历史，未改变当前核心", "Cannot read run history; current core unchanged")
	}
	pendingRevision := int64(0)
	err = rt.engine.ApplyChecked(ctx, c, func(old engine.Candidate, started time.Time) error { return rt.checkpoint(ctx, old, started) }, func() error {
		_, changed, err := rt.filtered(ctx, c, time.Now())
		if err != nil {
			return err
		}
		if changed {
			return i18n.New("预检期间客户端已到期或用尽额度，请重新应用最新用量", "A client expired or ran out of quota during preflight; apply again with the latest usage")
		}
		// 先标记切换未完成；此后崩溃不能把旧历史当作最后成功配置恢复。
		if err := rt.syncSnapshot(record); err != nil {
			return err
		}
		record.Pending = true
		data, _ := json.Marshal(record)
		doc, err := rt.state.SaveDocument(ctx, appliedDocument, previous.Revision, data)
		if err == nil {
			pendingRevision = doc.Revision
		}
		return err
	}, func() error { return rt.markStopped(ctx, true) })
	if err != nil {
		if pendingRevision != 0 {
			if _, rollbackErr := rt.state.SaveDocument(context.Background(), appliedDocument, pendingRevision, previous.Data); rollbackErr != nil {
				rt.issue = i18n.T("运行历史切换未完成，暂不可恢复历史；请重新预检并应用", "Run history switch not finished; history cannot be resumed now, run preflight and apply again")
			}
		}
		return err
	}
	// 核心已切换：收尾不随关闭信号或请求断开中止，否则历史停在切换未完成、下次启动拒绝恢复；关闭流程持rt.mu等待收尾。
	ctx = context.WithoutCancel(ctx)
	if c.Inbounds > 0 {
		if err = rt.checkpointCurrent(ctx); err != nil {
			_ = rt.engine.Stop()
			_ = rt.markStopped(context.Background(), true)
			rt.issue = i18n.T("启动后统计初始化失败，独立核心已停止；未采集尾流未知", "Statistics failed to initialize after start; core stopped, unmeasured traffic unknown")
			return errors.New(rt.issue)
		}
		if c.StatsAddress == "" {
			if err = rt.markStopped(ctx, true); err != nil {
				_ = rt.engine.Stop()
				return i18n.New("无法记录统计关闭期间的用量缺口，独立核心已停止", "Cannot record the usage gap while statistics were off; core stopped")
			}
		}
	} else if err = rt.markStopped(ctx, false); err != nil {
		return err
	}
	record.Pending = false
	record.AppliedAt = time.Now().UTC()
	data, _ := json.Marshal(record)
	if _, err = rt.state.SaveDocument(ctx, appliedDocument, pendingRevision, data); err != nil {
		_ = rt.engine.Stop()
		_ = rt.markStopped(context.Background(), true)
		rt.issue = i18n.T("运行历史保存失败，独立核心已停止；请重新预检并应用", "Failed to save run history; core stopped, run preflight and apply again")
		return errors.New(rt.issue)
	}
	rt.issue = ""
	if err = rt.setAutoResume(ctx, c.Inbounds > 0); err != nil {
		rt.issue = i18n.T("配置已应用，但自动恢复标记保存失败；面板重启时可能需要手动恢复", "Configuration applied, but saving the auto-resume mark failed; you may need to resume manually after o-ui restarts")
	}
	if c.Inbounds > 0 {
		rt.logs.Add("xray.applied")
	} else {
		rt.logs.Add("xray.empty")
	}
	return nil
}

func newStatsAddress() (string, error) {
	dir, err := os.MkdirTemp("/tmp", "ixp-")
	if err != nil {
		return "", i18n.New("无法创建受限统计套接字目录", "Cannot create the restricted statistics socket directory")
	}
	return filepath.Join(dir, "stats.sock"), nil
}

// filtered只依据已应用策略和配置快照，不夹带未应用的草稿。
func (rt *Runtime) filtered(ctx context.Context, c engine.Candidate, now time.Time) (map[string]json.RawMessage, bool, error) {
	data, err := os.ReadFile(c.PolicyFile)
	var policy runtimePolicy
	if err != nil || json.Unmarshal(data, &policy) != nil || policy.Clients == nil {
		return nil, false, i18n.New("已应用限制快照无法读取", "Applied limit snapshot cannot be read")
	}
	ledger, _, err := rt.ledger(ctx)
	if err != nil {
		return nil, false, err
	}
	data, err = os.ReadFile(c.File)
	var full map[string]json.RawMessage
	if err != nil || json.Unmarshal(data, &full) != nil {
		return nil, false, i18n.New("已应用配置快照无法读取", "Applied configuration snapshot cannot be read")
	}
	var inbounds []config.CompiledInbound
	if json.Unmarshal(full["inbounds"], &inbounds) != nil {
		return nil, false, i18n.New("已应用入站快照无效", "Applied inbound snapshot is invalid")
	}
	changed := false
	next := []config.CompiledInbound{}
	for _, in := range inbounds {
		users := []config.User{}
		for _, user := range in.Settings.Clients {
			id := strings.TrimPrefix(user.Email, "client-")
			limit, exists := policy.Clients[id]
			if !exists {
				return nil, false, i18n.New("客户端缺少已应用限制快照", "Client lacks an applied limit snapshot")
			}
			if blocked(limit, ledger.Totals[id], now) {
				changed = true
				continue
			}
			users = append(users, user)
		}
		if len(users) > 0 {
			in.Settings.Clients = users
			next = append(next, in)
		}
	}
	full["inbounds"], _ = json.Marshal(next)
	return full, changed, nil
}

func (rt *Runtime) enforce(ctx context.Context, c engine.Candidate, now time.Time) error {
	full, changed, err := rt.filtered(ctx, c, now)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	var next []config.CompiledInbound
	_ = json.Unmarshal(full["inbounds"], &next)
	stage, err := os.MkdirTemp(rt.root, "limit-")
	if err != nil {
		return i18n.New("无法创建限制配置快照", "Cannot create the limit configuration snapshot")
	}
	data, _ := json.Marshal(full)
	c.File = filepath.Join(stage, "config.json")
	c.Inbounds = len(next)
	if err = os.WriteFile(c.File, data, 0600); err != nil {
		return i18n.New("无法保存限制配置快照", "Cannot save the limit configuration snapshot")
	}
	if err = rt.apply(ctx, c); err != nil {
		return i18n.New("限制重载失败，已停止独立核心，未恢复过期或用尽客户端", "Limit reload failed; core stopped and expired or exhausted clients were not restored")
	}
	rt.logs.Add("xray.limits")
	return nil
}

func (rt *Runtime) guard(ctx context.Context) error {
	c, running := rt.engine.Current()
	if !running {
		if rt.engine.Status().UnexpectedExit {
			rt.issue = i18n.T("自有核心异常退出，未自动重启；已存累计保留，未采集尾流未知，请检查后重新预检并应用", "The core exited unexpectedly and was not restarted; saved totals kept, unmeasured traffic unknown, check and run preflight and apply again")
			if rt.setAutoResume(ctx, false) != nil {
				rt.issue = i18n.T("自有核心异常退出，且自动恢复标记清除失败；面板重启时可能自动恢复上次应用", "The core exited unexpectedly and clearing the auto-resume mark failed; o-ui may auto-resume the last applied configuration on restart")
			}
		}
		ledger, _, err := rt.ledger(ctx)
		if err != nil {
			return err
		}
		rt.business.observe("", nil, ledger.Totals, time.Now())
		if ledger.Active {
			if err = rt.markStopped(ctx, true); err != nil {
				return err
			}
			rt.issue = i18n.T("核心意外退出，已存累计保留，未采集尾流未知", "Core exited unexpectedly; saved totals kept, unmeasured traffic unknown")
		}
		return nil
	}
	if err := rt.checkpointCurrent(ctx); err != nil {
		return err
	}
	return rt.enforce(ctx, c, time.Now())
}

func (rt *Runtime) watch(ctx context.Context) {
	defer close(rt.done)
	ticker := time.NewTicker(trafficInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.mu.Lock()
			if ctx.Err() == nil {
				if err := rt.guard(ctx); err != nil {
					_ = rt.engine.Stop()
					_ = rt.markStopped(context.Background(), true)
					rt.issue = i18n.T(err.Error()+"；为避免未受控访问，独立核心已停止，已存用量保留", i18n.Translate(err.Error(), "en")+"; to avoid uncontrolled access the core was stopped and saved usage kept")
					if rt.setAutoResume(context.Background(), false) != nil {
						rt.issue = i18n.T(rt.issue+"；自动恢复标记清除失败", i18n.Translate(rt.issue, "en")+"; clearing the auto-resume mark failed")
					}
					rt.logs.Add("xray.guard")
				}
			}
			rt.mu.Unlock()
		}
	}
}

func registerTraffic(mux *http.ServeMux, rt *Runtime) {
	mux.HandleFunc("GET /api/runtime/traffic", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("流量控制未启用", "Traffic control is not enabled")})
			return
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		ledger, _, err := rt.ledger(r.Context())
		if err != nil {
			respond(w, 500, map[string]string{"error": i18n.T("无法读取真实流量累计", "Cannot read traffic totals")})
			return
		}
		c, running := rt.engine.Current()
		var inbounds any
		if in := ledger.Inbounds; in != nil {
			inbounds = map[string]any{"totals": in.Totals, "periods": traffic.CurrentPeriods(*in, time.Now()), "sampledAt": in.SampledAt}
		}
		respond(w, 200, map[string]any{"inbounds": inbounds, "periods": traffic.CurrentPeriods(ledger, time.Now()), "totals": ledger.Totals, "lastOnline": ledger.LastOnline, "sampledAt": ledger.SampledAt, "collecting": running && c.StatsAddress != "", "intervalSeconds": int(trafficInterval.Seconds()), "issue": rt.issue, "incomplete": ledger.Incomplete})
	})
}
