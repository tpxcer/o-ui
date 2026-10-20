package localapi

import (
	"context"
	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
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
