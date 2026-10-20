package localapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
)

// PanelInfo供服务器上的管理命令显示当前面板设置；不含密码或私钥内容。
type PanelInfo struct {
	Public          bool   `json:"public"`
	Listen          string `json:"listen"`
	Origin          string `json:"origin"`
	BasePath        string `json:"basePath"`
	URL             string `json:"url"`
	Username        string `json:"username"`
	CertificateFile string `json:"certificateFile"`
	KeyFile         string `json:"keyFile"`
}

// SetPanelPath离线设置面板访问路径（面板须已停止），下次启动生效；其余访问设置保持不变。
func SetPanelPath(ctx context.Context, stateRoot, path string) error {
	if path == "" || !validPanelBasePath(path) {
		return i18n.New("访问路径须为 / 或 /字母数字下划线短横线/（1–64位），不能是 /api/、/sub/、/assets/", "The access path must be / or /letters-digits-underscores-hyphens/ (1–64), and cannot be /api/, /sub/ or /assets/")
	}
	s, lock, err := openOfflineState(stateRoot, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer s.Close()
	doc, err := s.ReadDocument(ctx, "panel-access")
	if err != nil || doc.Revision == 0 || string(doc.Data) == "null" {
		return i18n.New("尚无已保存的面板访问设置，请先完成安装", "No saved panel access settings yet; finish the installation first")
	}
	var access PanelAccessDraft
	if decodeStrict(doc.Data, &access) != nil {
		return i18n.New("面板访问设置损坏，未做修改", "Panel access settings are corrupted; nothing changed")
	}
	access.BasePath = path
	if path == "/" {
		access.BasePath = ""
	}
	data, _ := json.Marshal(access)
	if _, err = s.SaveDocument(ctx, "panel-access", doc.Revision, data); err != nil {
		return i18n.New("保存访问路径失败，原设置未改变", "Failed to save the access path; previous settings unchanged")
	}
	return nil
}

// ReadPanelInfo只读当前面板设置，面板运行时也可调用（不持有运行锁）。
func ReadPanelInfo(ctx context.Context, stateRoot string) (PanelInfo, error) {
	var info PanelInfo
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot {
		return info, i18n.New("必须明确指定状态目录的绝对路径", "The state directory must be an explicit absolute path")
	}
	if fi, err := os.Lstat(filepath.Join(stateRoot, "panel.sqlite")); err != nil || !fi.Mode().IsRegular() {
		return info, i18n.New("只接受现有普通面板数据库，不创建空库或读取链接", "Only an existing regular panel database is accepted; no empty database is created and links are not followed")
	}
	s, err := store.Open(stateRoot)
	if err != nil {
		return info, err
	}
	defer s.Close()
	docs, err := s.ReadDocuments(ctx, []string{"panel-access", "panel-tls"})
	if err != nil {
		return info, i18n.New("读取面板设置失败", "Failed to read panel settings")
	}
	var access PanelAccessDraft
	if raw := docs["panel-access"].Data; len(raw) > 0 && string(raw) != "null" && decodeStrict(raw, &access) != nil {
		return info, i18n.New("面板访问设置损坏", "Panel access settings are corrupted")
	}
	var certificate panelTLSDraft
	if raw := docs["panel-tls"].Data; len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &certificate)
	}
	info = PanelInfo{Public: access.Public, Listen: access.Listen, Origin: access.Origin, BasePath: access.basePath(), CertificateFile: certificate.CertificateFile, KeyFile: certificate.KeyFile}
	if access.Public {
		info.URL = access.Origin + info.BasePath
	} else if access.Listen != "" {
		info.URL = "http://" + access.Listen + info.BasePath
	}
	if info.Username, err = s.AdministratorName(ctx); err != nil {
		return info, err
	}
	return info, nil
}

// ResetPanelAdmin供服务器管理命令离线重置用户名和密码（面板须已停止），全部会话失效。
func ResetPanelAdmin(ctx context.Context, stateRoot, username, password string) error {
	if err := store.ValidateAdministrator(username, password); err != nil {
		return err
	}
	s, lock, err := openOfflineState(stateRoot, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer s.Close()
	return s.ResetAdministrator(ctx, username, password)
}
