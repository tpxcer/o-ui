package localapi

import (
	"context"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
	"net/http"
)

// savedDocument是保存结果：保存成功后立即按最新修订预检并应用，失败时保留原运行核心。
type savedDocument struct {
	store.Document
	Applied    bool   `json:"applied"`
	ApplyError string `json:"applyError,omitempty"`
	Firewall   string `json:"firewall,omitempty"`
}

// applySaved按已保存的入站与设置生成候选并应用；调用方须持有rt.mu。
func (rt *Runtime) applySaved(ctx context.Context) error {
	version := rt.manager.Selected()
	if version == "" {
		return i18n.New("尚未安装 Xray 核心，请在首页安装后再保存", "The Xray core is not installed; install it from the dashboard and save again")
	}
	if err := rt.checkpointCurrent(ctx); err != nil {
		return err
	}
	workspace, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		return err
	}
	// 从未保存过入站时给出明确提示；已删光入站的草稿照常应用，以停止旧监听。
	if len(workspace.Data) == 0 || string(workspace.Data) == "null" {
		return i18n.New("还没有入站，请先在“入站列表”创建节点", "No inbounds yet; create one on the Inbounds page first")
	}
	settings, err := rt.state.ReadDocument(ctx, "xray-settings")
	if err != nil {
		return err
	}
	c, err := rt.candidate(ctx, runtimeRequest{Version: version, WorkspaceRevision: workspace.Revision, SettingsRevision: settings.Revision})
	if err != nil {
		return err
	}
	return rt.apply(ctx, c)
}

func (rt *Runtime) saved(ctx context.Context, doc store.Document) savedDocument {
	result := savedDocument{Document: doc}
	if rt == nil || rt.manager == nil {
		return result
	}
	if err := rt.applySaved(ctx); err != nil {
		result.ApplyError = err.Error()
		return result
	}
	result.Applied = true
	return result
}

// registerApplySaved参照OUI的“重启Xray”：按已保存配置一键预检并应用（停止时即启动），失败保留原运行配置。
func registerApplySaved(mux *http.ServeMux, rt *Runtime) {
	mux.HandleFunc("POST /api/runtime/apply-saved", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		var request struct{}
		if !readJSON(w, r, &request, 64) {
			return
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if err := rt.applySaved(r.Context()); err != nil {
			respond(w, 422, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, rt.engine.Status())
	})
}
