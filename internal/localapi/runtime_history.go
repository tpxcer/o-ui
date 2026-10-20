package localapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/engine"
	"independent-panel/internal/store"
)

const appliedDocument = "runtime-applied"

type appliedSnapshot struct {
	Format    int               `json:"format"`
	Pending   bool              `json:"pending"`
	AppliedAt time.Time         `json:"appliedAt"`
	Candidate engine.Candidate  `json:"candidate"`
	Files     map[string]string `json:"files"`
}

type appliedSummary struct {
	Revision          int64      `json:"revision"`
	Available         bool       `json:"available"`
	Version           string     `json:"version,omitempty"`
	WorkspaceRevision int64      `json:"workspaceRevision"`
	SettingsRevision  int64      `json:"settingsRevision"`
	Inbounds          int        `json:"inbounds"`
	AppliedAt         *time.Time `json:"appliedAt,omitempty"`
	Issue             string     `json:"issue,omitempty"`
	NonLoopbackProxy  bool       `json:"nonLoopbackProxy"`
}

// 只接受当前运行目录内的普通受限文件，不跟随子目录或文件符号链接。
func (rt *Runtime) snapshotFile(path string) ([]byte, error) {
	rel, err := filepath.Rel(rt.root, path)
	if err != nil || !filepath.IsAbs(path) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, i18n.New("运行历史文件不在本项目受限目录内", "Run history file is outside this panel's restricted directory")
	}
	current := rt.root
	parts := strings.Split(rel, string(os.PathSeparator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, i18n.New("运行历史文件缺失或为符号链接", "Run history file is missing or a symlink")
		}
		if index < len(parts)-1 && (!info.IsDir() || info.Mode().Perm() != 0700) {
			return nil, i18n.New("运行历史目录权限无效", "Invalid run history directory permissions")
		}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, i18n.New("运行历史文件无法读取", "Run history file cannot be read")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 8<<20 {
		return nil, i18n.New("运行历史文件类型、权限或大小无效", "Invalid run history file type, permissions or size")
	}
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, i18n.New("运行历史读取失败或过大", "Run history read failed or too large")
	}
	return data, nil
}

func (rt *Runtime) snapshot(c engine.Candidate) (appliedSnapshot, error) {
	result := appliedSnapshot{Format: 1, AppliedAt: time.Now().UTC(), Candidate: c, Files: map[string]string{}}
	result.Candidate.Binary = ""
	data, err := rt.snapshotFile(c.File)
	var full struct {
		Inbounds []config.CompiledInbound `json:"inbounds"`
	}
	if err != nil || json.Unmarshal(data, &full) != nil || len(full.Inbounds) != c.Inbounds {
		return result, i18n.New("无法记录已应用配置快照", "Cannot record the applied configuration snapshot")
	}
	paths := []string{c.File, c.PolicyFile}
	for _, in := range full.Inbounds {
		if in.Stream.TLS != nil {
			for _, cert := range in.Stream.TLS.Certificates {
				paths = append(paths, cert.File, cert.Key)
			}
		}
	}
	for _, path := range paths {
		data, err := rt.snapshotFile(path)
		if err != nil {
			return result, err
		}
		hash := sha256.Sum256(data)
		result.Files[path] = hex.EncodeToString(hash[:])
	}
	return result, nil
}

func (rt *Runtime) syncSnapshot(record appliedSnapshot) error {
	directories := map[string]bool{rt.root: true}
	for path := range record.Files {
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return i18n.New("运行快照无法同步", "Run snapshot cannot be synced")
		}
		err = file.Sync()
		_ = file.Close()
		if err != nil {
			return i18n.New("运行快照无法同步", "Run snapshot cannot be synced")
		}
		directories[filepath.Dir(path)] = true
	}
	for path := range directories {
		dir, err := os.Open(path)
		if err != nil {
			return i18n.New("运行快照目录无法同步", "Run snapshot directory cannot be synced")
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return i18n.New("运行快照目录无法同步", "Run snapshot directory cannot be synced")
		}
	}
	return nil
}

func (rt *Runtime) history(ctx context.Context) (appliedSnapshot, store.Document, error) {
	doc, err := rt.state.ReadDocument(ctx, appliedDocument)
	var record appliedSnapshot
	if err != nil {
		return record, doc, i18n.New("无法读取运行历史", "Cannot read run history")
	}
	if string(doc.Data) == "null" {
		return record, doc, nil
	}
	if decodeStrict(doc.Data, &record) != nil || record.Format != 1 || record.AppliedAt.IsZero() || record.Candidate.Version == "" || record.Candidate.WorkspaceRevision < 0 || record.Candidate.SettingsRevision < 0 || record.Candidate.Inbounds < 0 || record.Candidate.Inbounds > 500 || len(record.Files) < 2 || len(record.Files) > 1002 {
		return record, doc, i18n.New("运行历史损坏，请重新预检并应用已保存配置", "Run history is corrupted; run preflight and apply the saved configuration again")
	}
	if record.Pending {
		return record, doc, i18n.New("上次配置切换未完成，禁止恢复不确定历史；请重新预检并应用已保存配置", "The last configuration switch did not finish; uncertain history cannot be resumed, run preflight and apply again")
	}
	for path, expected := range record.Files {
		data, err := rt.snapshotFile(path)
		hash := sha256.Sum256(data)
		if err != nil || expected != hex.EncodeToString(hash[:]) {
			return record, doc, i18n.New("运行历史文件缺失或已改变，拒绝恢复", "Run history files are missing or changed; refusing to resume")
		}
	}
	current, err := rt.snapshot(record.Candidate)
	if err != nil || len(current.Files) != len(record.Files) {
		return record, doc, i18n.New("运行历史引用无效，拒绝恢复", "Run history references are invalid; refusing to resume")
	}
	for path, hash := range current.Files {
		if record.Files[path] != hash {
			return record, doc, i18n.New("运行历史引用未校验，拒绝恢复", "Run history references are unverified; refusing to resume")
		}
	}
	return record, doc, nil
}

func (rt *Runtime) historySummary(ctx context.Context) appliedSummary {
	record, doc, err := rt.history(ctx)
	result := appliedSummary{Revision: doc.Revision}
	if err != nil {
		result.Issue = err.Error()
		return result
	}
	if record.Format == 0 {
		return result
	}
	result.Version, result.WorkspaceRevision, result.SettingsRevision = record.Candidate.Version, record.Candidate.WorkspaceRevision, record.Candidate.SettingsRevision
	result.Inbounds, result.AppliedAt, result.Available = record.Candidate.Inbounds, &record.AppliedAt, record.Candidate.Inbounds > 0
	result.NonLoopbackProxy, err = rt.nonLoopbackProxy(record.Candidate)
	if err != nil {
		result.Available, result.Issue = false, err.Error()
	}
	return result
}

func (rt *Runtime) resumeCandidate(ctx context.Context, revision int64) (engine.Candidate, error) {
	return rt.historyCandidate(ctx, revision, nil)
}

func (rt *Runtime) historyCandidate(ctx context.Context, revision int64, restartAt *time.Time) (engine.Candidate, error) {
	record, doc, err := rt.history(ctx)
	c := record.Candidate
	if err != nil {
		return c, err
	}
	if revision != doc.Revision {
		return c, store.ErrConflict
	}
	if record.Format == 0 || c.Inbounds == 0 {
		return c, i18n.New("没有可恢复的已应用入站", "No applied inbounds to resume")
	}
	if restartAt == nil {
		if rt.engine.Status().Running {
			return c, i18n.New("请先停止本项目核心，再恢复上次应用", "Stop this panel's core before resuming the last applied configuration")
		}
	} else {
		status := rt.engine.Status()
		current, running := rt.engine.Current()
		if !running || status.StartedAt == nil || !status.StartedAt.Equal(*restartAt) || current.File != c.File || current.PolicyFile != c.PolicyFile || current.Version != c.Version || current.WorkspaceRevision != c.WorkspaceRevision || current.SettingsRevision != c.SettingsRevision {
			return c, store.ErrConflict
		}
	}
	c.Binary, err = rt.manager.Binary(c.Version)
	if err != nil {
		return c, i18n.New("上次应用的精确官方核心缺失或校验失败，请先安装该版本", "The exact official core last applied is missing or failed verification; install that version first")
	}
	full, _, err := rt.filtered(ctx, c, time.Now())
	if err != nil {
		return c, err
	}
	var inbounds []config.CompiledInbound
	if json.Unmarshal(full["inbounds"], &inbounds) != nil {
		return c, i18n.New("历史入站配置无效", "Historical inbound configuration is invalid")
	}
	for _, in := range inbounds {
		if in.Stream.TLS == nil {
			continue
		}
		for _, cert := range in.Stream.TLS.Certificates {
			pair, err := tls.LoadX509KeyPair(cert.File, cert.Key)
			if err != nil || pair.Leaf == nil || time.Now().Before(pair.Leaf.NotBefore) || !time.Now().Before(pair.Leaf.NotAfter) {
				return c, i18n.New("历史TLS证书无效或已过期，拒绝恢复", "Historical TLS certificate is invalid or expired; refusing to resume")
			}
		}
	}
	c.StatsAddress = ""
	if _, enabled := full["stats"]; enabled {
		if rt.statsSocket == "" {
			rt.statsSocket, err = newStatsAddress()
			if err != nil {
				return c, err
			}
		}
		full["api"], _ = json.Marshal(map[string]any{"tag": "panel-stats", "listen": rt.statsSocket, "services": []string{"StatsService"}})
		c.StatsAddress = "unix://" + rt.statsSocket
	}
	dir, err := os.MkdirTemp(rt.root, "resume-")
	if err != nil {
		return c, i18n.New("无法创建恢复候选目录", "Cannot create the resume candidate directory")
	}
	data, _ := json.Marshal(full)
	c.File, c.Inbounds = filepath.Join(dir, "config.json"), len(inbounds)
	if err = writePrivate(c.File, data); err != nil {
		return c, i18n.New("无法保存恢复候选配置", "Cannot save the resume candidate configuration")
	}
	return c, nil
}

const autoResumeDocument = "runtime-auto-resume"

// 仅记录上次核心是否应在运行；面板正常退出或被强杀时保留，手动停止、核心异常退出或安全停止时清除。
type autoResumeState struct {
	resume   bool
	revision int64
}

func (rt *Runtime) loadAutoResume(ctx context.Context) error {
	doc, err := rt.state.ReadDocument(ctx, autoResumeDocument)
	if err != nil {
		return i18n.New("无法读取自动恢复标记", "Cannot read the auto-resume mark")
	}
	var data struct {
		Resume bool `json:"resume"`
	}
	// 标记损坏时按不自动恢复处理，不阻止面板启动。
	rt.autoResume = autoResumeState{revision: doc.Revision, resume: decodeStrict(doc.Data, &data) == nil && data.Resume}
	return nil
}

func (rt *Runtime) setAutoResume(ctx context.Context, resume bool) error {
	if rt.autoResume.resume == resume {
		return nil
	}
	data, _ := json.Marshal(map[string]bool{"resume": resume})
	doc, err := rt.state.SaveDocument(ctx, autoResumeDocument, rt.autoResume.revision, data)
	if err != nil {
		return err
	}
	rt.autoResume = autoResumeState{resume: resume, revision: doc.Revision}
	return nil
}

// ResumeOnStart只在上次面板退出时核心仍在运行时，按与手动恢复相同的校验恢复已应用历史；
// 不读取较新草稿或另选版本，该历史的非回环监听已在应用时确认过。失败保持停止，标记保留供下次启动重试。
func (rt *Runtime) ResumeOnStart(ctx context.Context) (bool, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.autoResume.resume {
		return false, nil
	}
	_, doc, err := rt.history(ctx)
	var c engine.Candidate
	if err == nil {
		c, err = rt.historyCandidate(ctx, doc.Revision, nil)
	}
	if err == nil {
		err = rt.apply(ctx, c)
	}
	if err != nil {
		rt.logs.Add("xray.autoresume.failed")
		rt.issue = i18n.Wrap("面板启动时自动恢复上次应用失败，核心保持停止：", "Auto-resume at startup failed; the core stays stopped: ", err).Error()
		return false, err
	}
	rt.logs.Add("xray.autoresume.ok")
	return rt.engine.Status().Running, nil
}

func registerRuntimeHistory(mux *http.ServeMux, rt *Runtime) {
	mux.HandleFunc("GET /api/runtime/history", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		respond(w, 200, rt.historySummary(r.Context()))
	})
	for _, action := range []string{"resume-preflight", "resume", "restart-preflight", "restart"} {
		mux.HandleFunc("POST /api/runtime/"+action, func(w http.ResponseWriter, r *http.Request) {
			if rt == nil {
				respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
				return
			}
			var request struct {
				Revision          int64      `json:"revision"`
				Confirmation      string     `json:"confirmation"`
				StartedAt         *time.Time `json:"startedAt"`
				ProxyConfirmation string     `json:"proxyConfirmation,omitempty"`
			}
			if !readJSON(w, r, &request, 512) {
				return
			}
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if action == "resume" && request.Confirmation != "确认恢复上次应用" {
				respond(w, 409, map[string]string{"error": i18n.T("尚未明确确认恢复上次应用", "Resuming the last applied configuration has not been confirmed")})
				return
			}
			restart := strings.HasPrefix(action, "restart")
			if restart && (request.StartedAt == nil || request.StartedAt.IsZero() || (action == "restart" && request.Confirmation != "确认重启当前配置")) {
				respond(w, 409, map[string]string{"error": i18n.T("请重新读取当前运行实例并明确确认重启", "Reload the running instance and confirm the restart explicitly")})
				return
			}
			var restartAt *time.Time
			if restart {
				restartAt = request.StartedAt
				if err := rt.checkpointCurrent(r.Context()); err != nil {
					respond(w, 422, map[string]string{"error": err.Error()})
					return
				}
			}
			c, err := rt.historyCandidate(r.Context(), request.Revision, restartAt)
			c.Diagnostics = rt.logs
			if err == nil {
				if action == "resume" || action == "restart" {
					if err = rt.confirmProxy(c, request.ProxyConfirmation); err == nil {
						err = rt.apply(r.Context(), c)
					}
				} else {
					err = engine.Preflight(r.Context(), c)
				}
			}
			if err != nil {
				code := 422
				if errors.Is(err, store.ErrConflict) {
					code = 409
				}
				respond(w, code, map[string]string{"error": err.Error()})
				return
			}
			respond(w, 200, rt.engine.Status())
		})
	}
}
