package core

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"independent-panel/internal/i18n"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const maxArchive = 100 << 20
const maxFile = 128 << 20

type selection struct {
	Selected string `json:"selected"`
	Previous string `json:"previous,omitempty"`
}

type receipt struct {
	Version       string            `json:"version"`
	Platform      string            `json:"platform"`
	ArchiveSHA256 string            `json:"archiveSHA256"`
	Files         map[string]string `json:"files"`
}

type Status struct {
	DefaultVersion string   `json:"defaultVersion"`
	Selected       string   `json:"selected"`
	Previous       string   `json:"previous,omitempty"`
	Installed      []string `json:"installed"`
	Platform       string   `json:"platform"`
	Runtime        string   `json:"runtime"`
	Busy           bool     `json:"busy"`
}

type Manager struct {
	root     string
	client   *http.Client
	platform string
	asset    string
	mu       sync.RWMutex
	state    selection
	busy     atomic.Bool
	probe    func(context.Context, string, string) error
	lock     *os.File
}

func NewManager(root string) (*Manager, error) {
	if !filepath.IsAbs(root) {
		return nil, i18n.New("核心数据目录必须为绝对路径", "Core data directory must be an absolute path")
	}
	name, err := assetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, i18n.New("核心数据目录必须为普通目录，不能使用符号链接", "Core data directory must be a regular directory, not a symlink")
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	m := &Manager{root: root, client: newHTTPClient(), platform: runtime.GOOS + "/" + runtime.GOARCH,
		asset: name, state: selection{Selected: DefaultVersion}, probe: probeVersion}
	lock, err := os.OpenFile(filepath.Join(root, ".manager.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, i18n.New("核心目录已被另一进程管理，拒绝并发写入", "Core directory is managed by another process; refusing concurrent writes")
	}
	m.lock = lock
	success := false
	defer func() {
		if !success {
			_ = m.Close()
		}
	}()
	data, err := os.ReadFile(filepath.Join(root, "selection.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if json.Unmarshal(data, &m.state) != nil {
			return nil, i18n.New("核心选择文件损坏，拒绝静默重置", "Core selection file is corrupted; refusing to reset it silently")
		}
		if v, err := NormalizeVersion(m.state.Selected); err != nil || v != m.state.Selected {
			return nil, i18n.New("已保存的核心版本无效", "Saved core version is invalid")
		}
		if m.state.Previous != "" {
			if v, err := NormalizeVersion(m.state.Previous); err != nil || v != m.state.Previous {
				return nil, i18n.New("已保存的上一核心版本无效", "Saved previous core version is invalid")
			}
		}
	}
	success = true
	return m, nil
}

// 仅在HTTP服务已停止接收/处理请求后关闭目录锁。
func (m *Manager) Close() error {
	if m.lock == nil {
		return nil
	}
	err := m.lock.Close()
	m.lock = nil
	return err
}

func fileSHA(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *Manager) installed(version string) error {
	dir := filepath.Join(m.root, "v"+version)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return i18n.New("该版本未安装或安装目录无效", "This version is not installed or its directory is invalid")
	}
	data, err := os.ReadFile(filepath.Join(dir, "receipt.json"))
	if err != nil {
		return i18n.New("缺少安装校验记录", "Installation receipt is missing")
	}
	var record receipt
	if json.Unmarshal(data, &record) != nil || record.Version != version || record.Platform != m.platform || len(record.ArchiveSHA256) != 64 {
		return i18n.New("安装校验记录无效", "Installation receipt is invalid")
	}
	for _, name := range []string{"xray", "geoip.dat", "geosite.dat", "LICENSE"} {
		filename := filepath.Join(dir, name)
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || (name == "xray" && info.Mode()&0100 == 0) {
			return i18n.New("核心文件缺失或不是普通文件", "Core file is missing or not a regular file")
		}
		digest, err := fileSHA(filename)
		if err != nil || digest != record.Files[name] {
			return i18n.New("已安装核心文件校验失败，拒绝使用", "Installed core files failed verification; refusing to use them")
		}
	}
	return nil
}

func (m *Manager) Status() Status {
	m.mu.RLock()
	state := m.state
	m.mu.RUnlock()
	result := Status{DefaultVersion: DefaultVersion, Selected: state.Selected, Previous: state.Previous,
		Installed: []string{}, Platform: m.platform, Runtime: "not-configured", Busy: m.busy.Load()}
	entries, _ := os.ReadDir(m.root)
	for _, entry := range entries {
		v, err := NormalizeVersion(entry.Name())
		if err == nil && entry.Name() == "v"+v && m.installed(v) == nil {
			result.Installed = append(result.Installed, v)
		}
	}
	return result
}

// written表示重命名已完成；其后目录同步失败不能谎报“未更改”。
func atomicJSON(filename string, value any) (written bool, err error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return false, err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".write-")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := os.Rename(f.Name(), filename); err != nil {
		return false, err
	}
	return true, syncDirectory(filepath.Dir(filename))
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (m *Manager) choose(version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.state
	if version != next.Selected {
		if m.installed(next.Selected) == nil {
			next.Previous = next.Selected
		}
		next.Selected = version
	}
	written, err := atomicJSON(filepath.Join(m.root, "selection.json"), next)
	if written {
		m.state = next
	}
	if err != nil {
		if written {
			return i18n.New("版本选择已写入，但持久化同步失败；请重新加载核对，不保证重启后保留", "Version selection was written but syncing to disk failed; reload to check, it may not survive a restart")
		}
		return i18n.New("保存版本选择失败，当前选择未更改", "Failed to save the version selection; current selection unchanged")
	}
	return nil
}

var ErrBusy = i18n.New("另一个核心版本操作正在进行，请稍后重试", "Another core version operation is in progress; try again later")

// Binary返回已校验的精确版本；选择新版本不会替换正在运行的进程。
func (m *Manager) Binary(version string) (string, error) {
	v, err := NormalizeVersion(version)
	if err != nil {
		return "", err
	}
	if err = m.installed(v); err != nil {
		return "", err
	}
	return filepath.Join(m.root, "v"+v, "xray"), nil
}

func (m *Manager) Selected() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Selected
}

// 只安装并选择二进制；不启动代理，也不修改其他面板的核心。
func (m *Manager) Install(ctx context.Context, input string) error {
	return m.InstallWithSelectionLock(ctx, input, nil)
}

// 下载和校验不占用运行锁，仅提交选择时与配置应用串行。
func (m *Manager) InstallWithSelectionLock(ctx context.Context, input string, selectionLock sync.Locker) error {
	v, err := NormalizeVersion(input)
	if err != nil {
		return err
	}
	if !m.busy.CompareAndSwap(false, true) {
		return ErrBusy
	}
	defer m.busy.Store(false)
	choose := func() error {
		if selectionLock != nil {
			selectionLock.Lock()
			defer selectionLock.Unlock()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return m.choose(v)
	}
	if m.installed(v) == nil {
		if err := m.probe(ctx, filepath.Join(m.root, "v"+v, "xray"), v); err != nil {
			return err
		}
		return choose()
	}
	if _, err := os.Lstat(filepath.Join(m.root, "v"+v)); !errors.Is(err, os.ErrNotExist) {
		return i18n.New("该版本目录已存在但校验未通过；保留原文件，不覆盖或删除", "This version directory exists but failed verification; files are kept, not overwritten or deleted")
	}
	r, err := m.release(ctx, v)
	if err != nil {
		return err
	}
	var selected *asset
	for i := range r.Assets {
		if r.Assets[i].Name == m.asset {
			if selected != nil {
				return i18n.New("官方资产名称重复", "Duplicate official asset name")
			}
			selected = &r.Assets[i]
		}
	}
	if selected == nil {
		return i18n.New("该版本没有匹配本机架构的官方发布包", "This version has no official package for this architecture")
	}
	expectedURL := repository + "/releases/download/v" + v + "/" + m.asset
	digest := strings.TrimPrefix(selected.Digest, "sha256:")
	decoded, digestErr := hex.DecodeString(digest)
	if selected.URL != expectedURL || !strings.HasPrefix(selected.Digest, "sha256:") || digestErr != nil || len(decoded) != 32 || selected.Size <= 0 || selected.Size > maxArchive {
		return i18n.New("官方包来源、大小或SHA256摘要缺失/无效；拒绝不校验安装", "Official package source, size or SHA256 digest is missing/invalid; refusing unverified install")
	}
	resp, err := m.get(ctx, selected.URL, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	archive, err := os.CreateTemp(m.root, ".download-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(archive, h), io.LimitReader(resp.Body, maxArchive+1))
	closeErr := archive.Close()
	if copyErr != nil || closeErr != nil || n != selected.Size || n > maxArchive || hex.EncodeToString(h.Sum(nil)) != strings.ToLower(digest) {
		return i18n.New("下载不完整或SHA256校验失败，当前选择未更改", "Download incomplete or SHA256 mismatch; current selection unchanged")
	}
	stage, err := os.MkdirTemp(m.root, ".stage-")
	if err != nil {
		return err
	}
	// 失败暂存目录保留以便排查；不递归删除或覆盖已有版本。
	record, err := extractArchive(archive.Name(), stage)
	if err != nil {
		return err
	}
	record.Version, record.Platform, record.ArchiveSHA256 = v, m.platform, strings.ToLower(digest)
	if err := m.probe(ctx, filepath.Join(stage, "xray"), v); err != nil {
		return err
	}
	if _, err := atomicJSON(filepath.Join(stage, "receipt.json"), record); err != nil {
		return err
	}
	if err := os.Rename(stage, filepath.Join(m.root, "v"+v)); err != nil {
		return err
	}
	if err := syncDirectory(m.root); err != nil {
		return i18n.New("核心目录已写入，但持久化同步失败；当前选择未更改，请重新加载核对", "Core directory was written but syncing to disk failed; current selection unchanged, reload to check")
	}
	return choose()
}

func extractArchive(filename, destination string) (receipt, error) {
	r, err := zip.OpenReader(filename)
	if err != nil {
		return receipt{}, i18n.New("官方包不是有效ZIP文件", "Official package is not a valid ZIP file")
	}
	defer r.Close()
	allowed := map[string]bool{"xray": true, "geoip.dat": true, "geosite.dat": true, "LICENSE": true}
	record := receipt{Files: map[string]string{}}
	for _, entry := range r.File {
		if strings.Contains(entry.Name, "\\") || path.IsAbs(entry.Name) || path.Clean(entry.Name) != entry.Name || strings.HasPrefix(entry.Name, "../") || entry.Mode()&os.ModeSymlink != 0 {
			return record, i18n.New("发布包包含不安全路径或符号链接", "Package contains unsafe paths or symlinks")
		}
		if !allowed[entry.Name] {
			continue
		}
		if _, exists := record.Files[entry.Name]; exists || entry.UncompressedSize64 > maxFile || !entry.Mode().IsRegular() {
			return record, i18n.New("发布包文件重复、过大或类型无效", "Package files are duplicated, too large or of an invalid type")
		}
		in, err := entry.Open()
		if err != nil {
			return record, err
		}
		mode := os.FileMode(0600)
		if entry.Name == "xray" {
			mode = 0700
		}
		out, err := os.OpenFile(filepath.Join(destination, entry.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			in.Close()
			return record, err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, maxFile+1))
		in.Close()
		if copyErr == nil {
			copyErr = out.Sync()
		}
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || n > maxFile {
			return record, i18n.New("发布包解压失败", "Failed to extract the package")
		}
		record.Files[entry.Name] = hex.EncodeToString(h.Sum(nil))
	}
	if len(record.Files) != len(allowed) {
		return record, i18n.New("官方包缺少核心、地理数据或许可证，拒绝安装", "Official package lacks the core, geo data or license; refusing to install")
	}
	return record, nil
}

func probeVersion(ctx context.Context, binary, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "version")
	cmd.Dir = filepath.Dir(binary)
	output, err := cmd.Output()
	if err != nil {
		return i18n.New("官方核心无法在本机运行，当前选择未更改", "Official core cannot run on this machine; current selection unchanged")
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 || fields[0] != "Xray" || fields[1] != version {
		return i18n.Errorf("核心自报版本与v%s不一致，当前选择未更改", "Core reports a version different from v%s; current selection unchanged", version)
	}
	return nil
}
