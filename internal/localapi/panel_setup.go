package localapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"independent-panel/internal/i18n"
	"independent-panel/internal/store"
)

const httpsConfirmation = "确认启用HTTPS非本机访问"

// openOfflineState只供面板停止后由状态目录拥有者执行的离线CLI：绝对真实目录，持有与面板相同的运行锁。
func openOfflineState(stateRoot string, existing bool) (*store.Store, *os.File, error) {
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot {
		return nil, nil, i18n.New("必须明确指定状态目录的绝对路径", "The state directory must be an explicit absolute path")
	}
	if info, err := os.Lstat(filepath.Dir(stateRoot)); err != nil || !info.IsDir() {
		return nil, nil, i18n.New("状态目录的上级目录必须已存在", "The parent of the state directory must exist")
	}
	info, err := os.Lstat(stateRoot)
	if (err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0)) || (err != nil && (existing || !os.IsNotExist(err))) {
		return nil, nil, i18n.New("状态目录必须是已存在的真实目录", "The state directory must be an existing real directory")
	}
	if existing {
		if info, err := os.Lstat(filepath.Join(stateRoot, "panel.sqlite")); err != nil || !info.Mode().IsRegular() {
			return nil, nil, i18n.New("只接受现有普通面板数据库，不创建空库或读取链接", "Only an existing regular panel database is accepted; no empty database is created and links are not followed")
		}
	}
	lock, err := lockRuntimeDirectory(filepath.Join(stateRoot, "runtime"))
	if err != nil {
		return nil, nil, err
	}
	s, err := store.Open(stateRoot)
	if err != nil {
		lock.Close()
		return nil, nil, err
	}
	return s, lock, nil
}

// InitPanelAdmin供安装器在面板首次启动前离线设置管理员；不监听网络，已有管理员时拒绝。
// 面板首次启动时管理员已存在，公网HTTPS模式下也就没有可被抢先的网页初始化入口。
func InitPanelAdmin(ctx context.Context, stateRoot, username, password string) error {
	if err := store.ValidateAdministrator(username, password); err != nil {
		return err
	}
	s, lock, err := openOfflineState(stateRoot, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer s.Close()
	session, err := s.Initialize(ctx, username, password)
	if err != nil {
		return err
	}
	// 初始化附带的会话令牌从未离开本进程；删除后首次登录仍须经面板访问方式完成。
	return s.Logout(ctx, session.Token)
}

// EnablePanelHTTPS供安装器离线写入面板证书路径与固定HTTPS访问来源，校验与网页保存相同，下次启动生效。
func EnablePanelHTTPS(ctx context.Context, stateRoot, certFile, keyFile, listen, origin, confirmation string) error {
	if confirmation != httpsConfirmation {
		return i18n.New("启用后管理面板与订阅可从非本机经HTTPS访问；请明确确认：确认启用HTTPS非本机访问", "Enabling exposes the panel and subscriptions over HTTPS to other hosts; confirm explicitly with: 确认启用HTTPS非本机访问")
	}
	tlsDraft := panelTLSDraft{CertificateFile: certFile, KeyFile: keyFile}
	certificate, err := loadPanelPair(tlsDraft)
	if err != nil {
		return err
	}
	access := PanelAccessDraft{Public: true, Listen: listen, Origin: origin}
	if err := validatePanelAccess(access, certificate); err != nil {
		return err
	}
	s, lock, err := openOfflineState(stateRoot, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer s.Close()
	initialized, err := s.Initialized(ctx)
	if err != nil || !initialized {
		return i18n.New("请先离线初始化管理员，再启用HTTPS非本机访问", "Initialize the administrator offline before enabling non-local HTTPS access")
	}
	docs, err := s.ReadDocuments(ctx, []string{"panel-access", "panel-tls"})
	if err != nil {
		return i18n.New("读取面板访问设置失败，未做修改", "Failed to read panel access settings; nothing changed")
	}
	accessData, _ := json.Marshal(access)
	tlsData, _ := json.Marshal(tlsDraft)
	if err := s.ReplaceDocuments(ctx, docs, map[string]json.RawMessage{"panel-access": accessData, "panel-tls": tlsData}); err != nil {
		return i18n.New("写入面板HTTPS设置失败，原设置未改变", "Failed to write panel HTTPS settings; previous settings unchanged")
	}
	return nil
}
