package localapi

import (
	"context"
	"encoding/json"
	"independent-panel/internal/i18n"
	"net"
	"os"
	"path/filepath"
	"time"

	"independent-panel/internal/store"
)

// 仅供拥有本机状态目录的操作者离线执行；不创建监听、重置账号或读取证书内容。
func RecoverPanelAccess(ctx context.Context, stateRoot, listen, confirmation string) (string, error) {
	if confirmation != "恢复面板本机访问" {
		return "", i18n.New("恢复将停用面板HTTPS并恢复回环访问；请停面板后明确确认：恢复面板本机访问", "Recovery disables panel HTTPS and restores loopback access; stop the panel and confirm with: 恢复面板本机访问")
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host != "127.0.0.1" {
		return "", i18n.New("恢复监听只允许127.0.0.1非特权端口", "Recovery listen address must be 127.0.0.1 with an unprivileged port")
	}
	access := PanelAccessDraft{Listen: listen}
	if err := validatePanelAccess(access, nil); err != nil {
		return "", err
	}
	if !filepath.IsAbs(stateRoot) {
		return "", i18n.New("恢复必须明确指定现有状态目录的绝对路径", "Recovery requires the absolute path of an existing state directory")
	}
	info, err := os.Lstat(stateRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", i18n.New("恢复状态目录必须是已存在的真实目录", "Recovery state directory must be an existing real directory")
	}
	info, err = os.Lstat(filepath.Join(stateRoot, "panel.sqlite"))
	if err != nil || !info.Mode().IsRegular() {
		return "", i18n.New("恢复只接受现有普通面板数据库，不创建空库或读取链接", "Recovery only accepts an existing regular panel database; it never creates one or follows links")
	}
	lock, err := lockRuntimeDirectory(filepath.Join(stateRoot, "runtime"))
	if err != nil {
		return "", err
	}
	defer lock.Close()
	s, err := store.Open(stateRoot)
	if err != nil {
		return "", err
	}
	defer s.Close()
	initialized, err := s.Initialized(ctx)
	if err != nil || !initialized {
		return "", i18n.New("现有管理员未初始化或不可读取，拒绝执行访问恢复", "The administrator is not set up or unreadable; refusing access recovery")
	}
	docs, err := s.ReadDocuments(ctx, []string{"panel-access", "panel-tls"})
	if err != nil {
		return "", i18n.New("读取访问设置失败，未执行恢复", "Failed to read access settings; recovery not performed")
	}
	for _, doc := range docs {
		if len(doc.Data) > 1<<20 {
			return "", i18n.New("旧访问设置过大，拒绝恢复", "Previous access settings are too large; refusing recovery")
		}
	}
	backup, err := json.Marshal(struct {
		Format    string                    `json:"format"`
		Version   int                       `json:"version"`
		CreatedAt time.Time                 `json:"createdAt"`
		Documents map[string]store.Document `json:"documents"`
	}{"independent-panel-access-recovery", 1, time.Now().UTC(), docs})
	if err != nil {
		return "", i18n.New("无法保留旧访问设置，未执行恢复", "Cannot keep the previous access settings; recovery not performed")
	}
	dir, err := os.MkdirTemp(stateRoot, "access-recovery-")
	if err != nil {
		return "", i18n.New("无法创建旧设置备份目录，未执行恢复", "Cannot create the backup directory for previous settings; recovery not performed")
	}
	backupFile := filepath.Join(dir, "settings.json")
	if err := writePrivate(backupFile, backup); err != nil {
		return backupFile, i18n.New("无法完整写入旧设置备份，未执行恢复；失败文件保留", "Cannot fully write the settings backup; recovery not performed, the failed file is kept")
	}
	for _, name := range []string{dir, stateRoot} {
		parent, err := os.Open(name)
		if err != nil {
			return backupFile, i18n.New("无法同步旧设置备份目录，未执行恢复", "Cannot sync the settings backup directory; recovery not performed")
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil || closeErr != nil {
			return backupFile, i18n.New("无法同步旧设置备份目录，未执行恢复", "Cannot sync the settings backup directory; recovery not performed")
		}
	}
	data, _ := json.Marshal(access)
	if err := s.ReplaceDocuments(ctx, docs, map[string]json.RawMessage{"panel-access": data, "panel-tls": json.RawMessage("null")}); err != nil {
		return backupFile, i18n.New("恢复未确认完成，请保留旧设置备份并重新检查数据库；未启动任何服务", "Recovery completion not confirmed; keep the settings backup and recheck the database (nothing was started)")
	}
	return backupFile, nil
}
