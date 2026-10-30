package localapi

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"independent-panel/internal/i18n"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"independent-panel/internal/backup"
	"independent-panel/internal/config"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

var backupDocuments = []string{"workspace", "xray-settings", "proxy-identities", "traffic", appliedDocument, subscriptionsDocument}

type backupCertificate struct {
	Certificate []byte `json:"certificate"`
	Key         []byte `json:"key"`
}
type businessBackup struct {
	Format       string                       `json:"format"`
	Version      int                          `json:"version"`
	CreatedAt    time.Time                    `json:"createdAt"`
	Workspace    workspaceDraft               `json:"workspace"`
	Settings     json.RawMessage              `json:"settings"`
	Identities   identities                   `json:"identities"`
	Traffic      traffic.Ledger               `json:"traffic"`
	Certificates map[string]backupCertificate `json:"certificates"`
	// 从OUI数据库换算时为"oui"，并附带预览说明；不进入备份文件。
	source string
	notes  []string
}
type backupRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type backupPreview struct {
	Fingerprint      string         `json:"fingerprint"`
	CreatedAt        time.Time      `json:"createdAt"`
	Inbounds         int            `json:"inbounds"`
	Clients          int            `json:"clients"`
	Certificates     int            `json:"certificates"`
	Source           string         `json:"source,omitempty"`
	Notes            []string       `json:"notes,omitempty"`
	ReplacedInbounds []backupRecord `json:"replacedInbounds"`
	ReplacedClients  []backupRecord `json:"replacedClients"`
	RemovedInbounds  []backupRecord `json:"removedInbounds"`
	RemovedClients   []backupRecord `json:"removedClients"`
}
type restoreRequest struct {
	Archive      string `json:"archive"`
	Password     string `json:"password"`
	Fingerprint  string `json:"fingerprint"`
	Confirmation string `json:"confirmation"`
	ShareHost    string `json:"shareHost,omitempty"` // 仅导入OUI数据库时用：写入导入入站的分享主机
}

func decodeStrict(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(&struct{}{}) != io.EOF {
		return i18n.New("备份结构无效或含不支持的字段", "Invalid backup structure or unsupported fields")
	}
	return nil
}

func backupLedger(data json.RawMessage) (traffic.Ledger, error) {
	if string(data) == "null" {
		return traffic.Empty(), nil
	}
	var ledger traffic.Ledger
	if decodeStrict(data, &ledger) != nil || ledger.Totals == nil || ledger.Last == nil || !traffic.ValidPeriods(ledger) || (ledger.Active && (ledger.Epoch == "" || ledger.SampledAt == nil)) {
		return ledger, i18n.New("流量账本无效，拒绝重置用量", "Traffic ledger is invalid; refusing to reset usage")
	}
	for id := range ledger.Totals {
		if !draftID.MatchString(id) {
			return ledger, i18n.New("流量客户端标识无效", "Invalid traffic client ID")
		}
	}
	if ledger.Inbounds != nil {
		for id := range ledger.Inbounds.Totals {
			if !draftID.MatchString(id) {
				return ledger, i18n.New("流量入站标识无效", "Invalid traffic inbound ID")
			}
		}
	}
	return ledger, nil
}

func validateBackup(b businessBackup) error {
	if b.Format != "independent-xray-panel" || b.Version != 1 || b.CreatedAt.IsZero() {
		return i18n.New("备份版本或来源格式不支持", "Unsupported backup version or source format")
	}
	workspace, _ := json.Marshal(b.Workspace)
	if err := validateWorkspaceDraft(workspace, nil); err != nil {
		return err
	}
	if len(workspace) > 512<<10 || len(b.Settings) > 128<<10 {
		return i18n.New("备份草稿超过本机测试上限", "Backup draft exceeds the local limit")
	}
	if string(b.Settings) != "null" {
		if err := validateSettingsDraft(b.Settings); err != nil {
			return err
		}
	}
	if b.Identities.Clients == nil || b.Identities.Inbounds == nil || b.Certificates == nil {
		return i18n.New("备份身份或证书集合缺失", "Backup identities or certificates are missing")
	}
	for id, identity := range b.Identities.Clients {
		if !draftID.MatchString(id) || len(identity.UUID) != 36 {
			return i18n.New("备份客户端身份无效", "Invalid client identity in backup")
		}
		uuid, err := hex.DecodeString(identity.UUID[0:8] + identity.UUID[9:13] + identity.UUID[14:18] + identity.UUID[19:23] + identity.UUID[24:36])
		if err != nil || len(uuid) != 16 || identity.UUID[8] != '-' || identity.UUID[13] != '-' || identity.UUID[18] != '-' || identity.UUID[23] != '-' || !config.ValidHY2Auth(identity.Auth) {
			return i18n.New("备份客户端身份无效", "Invalid client identity in backup")
		}
	}
	for id, identity := range b.Identities.Inbounds {
		key, err := base64.RawURLEncoding.DecodeString(identity.PrivateKey)
		if !draftID.MatchString(id) || err != nil || !config.ValidShortID(identity.ShortID) || len(identity.ExtraShortIDs) > 64 || len(identity.ExtraServerNames) > 64 || (identity.MinClientVer != "" && !config.ValidClientVersion(identity.MinClientVer)) {
			return i18n.New("备份REALITY身份无效", "Invalid REALITY identity in backup")
		}
		for _, short := range identity.ExtraShortIDs {
			if !config.ValidShortID(short) {
				return i18n.New("备份REALITY身份无效", "Invalid REALITY identity in backup")
			}
		}
		for _, name := range identity.ExtraServerNames {
			if !config.ValidShareHost(name) || net.ParseIP(name) != nil {
				return i18n.New("备份REALITY身份无效", "Invalid REALITY identity in backup")
			}
		}
		if _, err = ecdh.X25519().NewPrivateKey(key); err != nil {
			return i18n.New("备份REALITY私钥无效", "Invalid REALITY private key in backup")
		}
	}
	for _, c := range b.Workspace.Clients {
		if _, ok := b.Identities.Clients[c.ID]; !ok {
			return i18n.New("备份缺少客户端身份，不自动重建", "Backup lacks client identities; not regenerating automatically")
		}
	}
	wanted := map[string]bool{}
	for _, in := range b.Workspace.Inbounds {
		if in.Preset == "reality" || in.Preset == "xhttp-reality" {
			if _, ok := b.Identities.Inbounds[in.ID]; !ok {
				return i18n.New("备份缺少REALITY身份", "Backup lacks REALITY identities")
			}
		}
		// 从OUI导入时沿用同一台服务器上OUI所用的证书文件路径（如acme.sh续签的文件）。
		if b.source != "oui" && in.Connection != nil && (in.Connection.CertificateFile != "" || in.Connection.KeyFile != "" || in.Connection.UsePanelCertificate) {
			return i18n.New("备份不能指定证书落盘路径", "A backup cannot choose certificate file paths")
		}
		if in.Preset == "hy2" || in.Preset == "xhttp-tls" {
			wanted[in.ID] = true
		}
	}
	for id, c := range b.Certificates {
		if !wanted[id] || len(c.Certificate) > 1<<20 || len(c.Key) > 1<<20 {
			return i18n.New("备份证书关联或大小无效", "Invalid backup certificate link or size")
		}
		if _, err := tls.X509KeyPair(c.Certificate, c.Key); err != nil {
			return i18n.New("备份TLS证书和私钥不匹配", "Backup TLS certificate and key do not match")
		}
	}
	data, _ := json.Marshal(b.Traffic)
	_, err := backupLedger(data)
	return err
}

// capture由运行锁保护；只读取已保存草稿所指TLS文件，不读取导入包内路径。
func (rt *Runtime) capture(ctx context.Context) (businessBackup, map[string]store.Document, error) {
	docs, err := rt.state.ReadDocuments(ctx, backupDocuments)
	if err != nil {
		return businessBackup{}, nil, err
	}
	doc := docs["workspace"]
	draft := workspaceDraft{Inbounds: []inboundDraft{}, Clients: []clientDraft{}}
	if string(doc.Data) != "null" && json.Unmarshal(doc.Data, &draft) != nil {
		return businessBackup{}, nil, i18n.New("已保存入站损坏", "Saved inbounds are corrupted")
	}
	b := businessBackup{Format: "independent-xray-panel", Version: 1, CreatedAt: time.Now().UTC(), Workspace: draft, Settings: docs["xray-settings"].Data, Certificates: map[string]backupCertificate{}}
	b.Identities = identities{Clients: map[string]proxyIdentity{}, Inbounds: map[string]realityIdentity{}}
	if string(docs["proxy-identities"].Data) != "null" && json.Unmarshal(docs["proxy-identities"].Data, &b.Identities) != nil {
		return b, nil, i18n.New("无法读取连接身份", "Cannot read connection identities")
	}
	if b.Identities.Clients == nil || b.Identities.Inbounds == nil {
		return b, nil, i18n.New("连接身份结构无效", "Invalid connection identity structure")
	}
	if _, err = completeIdentities(&b.Identities, draft); err != nil {
		return b, nil, err
	}
	if b.Traffic, err = backupLedger(docs["traffic"].Data); err != nil {
		return b, nil, err
	}
	size := 0
	for _, doc := range docs {
		size += len(doc.Data)
	}
	if size > backup.MaxPlaintext {
		return b, nil, i18n.New("业务数据超过8 MB备份上限", "Data exceeds the 8 MB backup limit")
	}
	for index := range b.Workspace.Inbounds {
		in := &b.Workspace.Inbounds[index]
		if in.Connection == nil {
			continue
		}
		c := in.Connection
		if (in.Preset == "hy2" || in.Preset == "xhttp-tls") && (c.UsePanelCertificate || c.CertificateFile != "" || c.KeyFile != "") {
			var cert, key []byte
			var certErr, keyErr error
			if c.UsePanelCertificate {
				if rt.panelTLS == nil {
					return b, nil, i18n.New("草稿选择的面板证书尚未生效，无法完整备份", "The panel certificate chosen by the draft is not active; cannot make a complete backup")
				}
				cert, key = rt.panelTLS.cert, rt.panelTLS.key
			} else {
				cert, certErr = readPEM(c.CertificateFile)
				key, keyErr = readPEM(c.KeyFile)
			}
			if certErr != nil || keyErr != nil {
				return b, nil, i18n.New("草稿TLS证书或私钥无法读取，请修正路径后备份；没有省略证书", "Draft TLS certificate or key cannot be read; fix the paths and back up again (certificates are never omitted)")
			}
			size += base64.StdEncoding.EncodedLen(len(cert)) + base64.StdEncoding.EncodedLen(len(key))
			if size > backup.MaxPlaintext {
				return b, nil, i18n.New("含证书的业务数据超过8 MB备份上限", "Data including certificates exceeds the 8 MB backup limit")
			}
			b.Certificates[in.ID] = backupCertificate{cert, key}
		}
		c.CertificateFile, c.KeyFile = "", ""
		c.UsePanelCertificate = false
	}
	return b, docs, validateBackup(b)
}

func sealBackup(b businessBackup, password string) ([]byte, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	defer clear(data)
	return backup.Seal(data, password)
}

func decodeArchive(archive string) ([]byte, error) {
	if len(archive) > base64.StdEncoding.EncodedLen(backup.MaxFile) {
		return nil, i18n.New("备份文件过大", "Backup file too large")
	}
	file, err := base64.StdEncoding.Strict().DecodeString(archive)
	if err != nil {
		return nil, i18n.New("备份文件编码无效", "Invalid backup file encoding")
	}
	return file, nil
}

func openBackup(request restoreRequest) (businessBackup, []byte, error) {
	var b businessBackup
	file, err := decodeArchive(request.Archive)
	if err != nil {
		return b, nil, err
	}
	data, err := backup.Open(file, request.Password)
	if err != nil {
		return b, nil, err
	}
	defer clear(data)
	if err = decodeStrict(data, &b); err != nil {
		return b, nil, err
	}
	return b, file, validateBackup(b)
}

// openAnyBackup：OUI（3x-ui系）的x-ui.db换算成业务备份，其余按本面板备份打开。
// OUI的分享主机参与预览指纹，预览后改了分享主机需要重新预览。
func (rt *Runtime) openAnyBackup(request restoreRequest) (businessBackup, []byte, error) {
	file, err := decodeArchive(request.Archive)
	if err != nil || !isOUIBackup(file) {
		return openBackup(request)
	}
	b, err := importOUI(file, request.ShareHost, rt.root, time.Now())
	return b, append(file, "\x00"+request.ShareHost...), err
}

func previewBackup(b businessBackup, file []byte, docs map[string]store.Document) (backupPreview, error) {
	result := backupPreview{Source: b.source, Notes: b.notes, CreatedAt: b.CreatedAt, Inbounds: len(b.Workspace.Inbounds), Clients: len(b.Workspace.Clients), Certificates: len(b.Certificates), ReplacedInbounds: []backupRecord{}, ReplacedClients: []backupRecord{}, RemovedInbounds: []backupRecord{}, RemovedClients: []backupRecord{}}
	data, _ := json.Marshal(docs)
	hash := sha256.New()
	_, _ = hash.Write(file)
	_, _ = hash.Write(data)
	result.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	old := workspaceDraft{}
	if string(docs["workspace"].Data) != "null" && json.Unmarshal(docs["workspace"].Data, &old) != nil {
		return result, i18n.New("已有草稿损坏，拒绝覆盖", "Existing draft is corrupted; refusing to overwrite")
	}
	inbounds, clients := map[string]bool{}, map[string]bool{}
	for _, in := range b.Workspace.Inbounds {
		inbounds[in.ID] = true
	}
	for _, c := range b.Workspace.Clients {
		clients[c.ID] = true
	}
	for _, in := range old.Inbounds {
		item := backupRecord{in.ID, in.Name}
		if inbounds[in.ID] {
			result.ReplacedInbounds = append(result.ReplacedInbounds, item)
		} else {
			result.RemovedInbounds = append(result.RemovedInbounds, item)
		}
	}
	for _, c := range old.Clients {
		item := backupRecord{c.ID, c.Name}
		if clients[c.ID] {
			result.ReplacedClients = append(result.ReplacedClients, item)
		} else {
			result.RemovedClients = append(result.RemovedClients, item)
		}
	}
	return result, nil
}

func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func (rt *Runtime) restore(ctx context.Context, b businessBackup, current businessBackup, docs map[string]store.Document, password string) (string, []byte, error) {
	rollback, err := sealBackup(current, password)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(rt.root, "restore-")
	if err != nil {
		return "", nil, i18n.New("无法创建恢复目录，未改变数据", "Cannot create the restore directory; data unchanged")
	}
	name := filepath.Base(dir) + "-rollback" + backupExt(password)
	if err = writePrivate(filepath.Join(dir, name), rollback); err != nil {
		return "", nil, i18n.New("无法保存回退副本，未改变数据", "Cannot save the rollback copy; data unchanged")
	}
	for index := range b.Workspace.Inbounds {
		in := &b.Workspace.Inbounds[index]
		c, exists := b.Certificates[in.ID]
		if !exists {
			continue
		}
		certPath, keyPath := filepath.Join(dir, fmt.Sprintf("%d-cert.pem", index)), filepath.Join(dir, fmt.Sprintf("%d-key.pem", index))
		if writePrivate(certPath, c.Certificate) != nil || writePrivate(keyPath, c.Key) != nil {
			return "", nil, i18n.New("恢复证书保存失败，未改变数据库", "Failed to save restored certificates; database unchanged")
		}
		if in.Connection == nil {
			in.Connection = &connectionDraft{}
		}
		in.Connection.CertificateFile, in.Connection.KeyFile = certPath, keyPath
	}
	// 不通过旧备份降低同ID累计；未知备份之后的用量不能猜测补齐。
	for id, count := range current.Traffic.Totals {
		next := b.Traffic.Totals[id]
		next.Uplink, next.Downlink = max(next.Uplink, count.Uplink), max(next.Downlink, count.Downlink)
		b.Traffic.Totals[id] = next
	}
	if current.Traffic.Inbounds != nil || b.Traffic.Inbounds != nil {
		in := traffic.Empty()
		if b.Traffic.Inbounds != nil {
			in = *b.Traffic.Inbounds
		}
		if current.Traffic.Inbounds != nil {
			for id, count := range current.Traffic.Inbounds.Totals {
				next := in.Totals[id]
				next.Uplink, next.Downlink = max(next.Uplink, count.Uplink), max(next.Downlink, count.Downlink)
				in.Totals[id] = next
			}
		}
		in.Epoch, in.Last, in.Active, in.Incomplete, in.Periods = "", map[string]traffic.Count{}, false, true, nil
		b.Traffic.Inbounds = &in
	}
	b.Traffic.Epoch, b.Traffic.Last, b.Traffic.Active, b.Traffic.Incomplete = "", map[string]traffic.Count{}, false, true
	// 恢复后的周期从首个新样本起算，不能把合并的历史累计计入本周期。
	b.Traffic.Periods = nil
	data := map[string]json.RawMessage{}
	data["workspace"], _ = json.Marshal(b.Workspace)
	data["xray-settings"] = b.Settings
	if b.source == "oui" {
		// OUI的路由/DNS等不导入，保留o-ui当前的Xray设置。
		data["xray-settings"] = docs["xray-settings"].Data
	}
	data["proxy-identities"], _ = json.Marshal(b.Identities)
	data["traffic"], _ = json.Marshal(b.Traffic)
	// 历史运行快照不在业务包内；同一事务使旧历史失效，避免恢复后重启旧用户。
	data[appliedDocument] = json.RawMessage("null")
	// 不导入订阅密钥；换代使恢复前的所有链接失效，不移除原摘要记录。
	data[subscriptionsDocument], err = invalidateSubscriptions(docs[subscriptionsDocument])
	if err != nil {
		return "", nil, err
	}
	// 文件已fsync；目录条目也落盘后才提交SQLite引用。
	d, err := os.Open(dir)
	if err != nil {
		return "", nil, err
	}
	err = d.Sync()
	_ = d.Close()
	if err != nil {
		return "", nil, i18n.New("恢复目录无法同步，未改变数据库", "Restore directory could not be synced; database unchanged")
	}
	parent, err := os.Open(rt.root)
	if err != nil {
		return "", nil, err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return "", nil, i18n.New("恢复父目录无法同步，未改变数据库", "Restore parent directory could not be synced; database unchanged")
	}
	if err = rt.state.ReplaceDocuments(ctx, docs, data); err != nil {
		return "", nil, err
	}
	rt.issue = i18n.T("已恢复业务备份，核心保持停止；备份后的未知用量未补算，周期统计将重新起算，请预检后再应用", "Backup restored and the core stays stopped; usage after the backup is unknown and period statistics will restart, run preflight before applying")
	return name, rollback, nil
}

// backupExt：填口令的备份为加密的.ixpb，口令为空时为不加密的.json。
func backupExt(password string) string {
	if password == "" {
		return ".json"
	}
	return ".ixpb"
}

// exportBackup生成业务备份（填口令时加密）；调用方须持有backupMu与rt.mu（网页导出与Telegram定时备份共用）。
func (rt *Runtime) exportBackup(ctx context.Context, password string) (string, []byte, error) {
	if err := rt.checkpointCurrent(ctx); err != nil {
		return "", nil, err
	}
	workspace, err := rt.state.ReadDocument(ctx, "workspace")
	if err == nil && string(workspace.Data) != "null" {
		var draft workspaceDraft
		if err = validateWorkspaceDraft(workspace.Data, nil); err == nil {
			_ = json.Unmarshal(workspace.Data, &draft)
			_, err = rt.credentials(ctx, draft)
		}
	}
	if err != nil {
		return "", nil, i18n.New("无法读取或保存连接身份，未生成备份", "Cannot read or save connection identities; no backup created")
	}
	b, _, err := rt.capture(ctx)
	if err != nil {
		return "", nil, err
	}
	file, err := sealBackup(b, password)
	if err != nil {
		return "", nil, err
	}
	return "xray-panel-" + b.CreatedAt.Format("20060102-150405") + backupExt(password), file, nil
}

func registerBackup(mux *http.ServeMux, rt *Runtime) {
	for _, action := range []string{"export", "preview", "restore"} {
		mux.HandleFunc("POST /api/backup/"+action, func(w http.ResponseWriter, r *http.Request) {
			if rt == nil {
				respond(w, 503, map[string]string{"error": i18n.T("备份运行服务未启用", "Backup service is not enabled")})
				return
			}
			var request restoreRequest
			if !readJSON(w, r, &request, 12<<20) {
				return
			}
			// 固定KDF耗内存；与核心操作串行，禁止并发解密堆积内存。
			if !rt.backupMu.TryLock() {
				respond(w, 409, map[string]string{"error": i18n.T("核心或备份操作正在进行，请稍后重试", "A core or backup operation is in progress; try again later")})
				return
			}
			defer rt.backupMu.Unlock()
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if r.Context().Err() != nil {
				return
			}
			if action == "export" {
				filename, file, err := rt.exportBackup(r.Context(), request.Password)
				if err != nil {
					respond(w, 422, map[string]string{"error": err.Error()})
					return
				}
				respond(w, 200, map[string]string{"archive": base64.StdEncoding.EncodeToString(file), "filename": filename})
				return
			}
			if rt.engine.Status().Running {
				respond(w, 409, map[string]string{"error": i18n.T("请先停止本项目Xray核心，再预览或恢复备份", "Stop this panel's Xray core before previewing or restoring a backup")})
				return
			}
			b, file, err := rt.openAnyBackup(request)
			if err != nil {
				respond(w, 422, map[string]string{"error": err.Error()})
				return
			}
			docs, err := rt.state.ReadDocuments(r.Context(), backupDocuments)
			if err != nil {
				respond(w, 500, map[string]string{"error": i18n.T("读取当前数据失败，未执行恢复", "Failed to read current data; restore not performed")})
				return
			}
			preview, err := previewBackup(b, file, docs)
			if err != nil {
				respond(w, 422, map[string]string{"error": err.Error()})
				return
			}
			if action == "preview" {
				respond(w, 200, preview)
				return
			}
			if request.Confirmation != "确认覆盖并恢复" || subtle.ConstantTimeCompare([]byte(preview.Fingerprint), []byte(request.Fingerprint)) != 1 {
				respond(w, 409, map[string]string{"error": i18n.T("未确认恢复，或数据/备份已改变；请重新预览准确覆盖范围", "Restore not confirmed, or data/backup changed; preview again to see exactly what will be overwritten")})
				return
			}
			current, currentDocs, err := rt.capture(r.Context())
			if err == nil {
				// 回退副本取自与预览完全相同的文档快照。
				nowPreview, previewErr := previewBackup(b, file, currentDocs)
				if previewErr != nil || nowPreview.Fingerprint != request.Fingerprint {
					err = store.ErrConflict
				}
			}
			var name string
			var rollback []byte
			if err == nil {
				name, rollback, err = rt.restore(r.Context(), b, current, currentDocs, request.Password)
			}
			if err != nil {
				code := 422
				if errors.Is(err, store.ErrConflict) {
					code = 409
				}
				respond(w, code, map[string]string{"error": i18n.Wrap("恢复未提交，原业务数据保留：", "Restore not committed; existing data kept: ", err).Error()})
				return
			}
			respond(w, 200, map[string]string{"rollbackFile": name, "rollbackArchive": base64.StdEncoding.EncodeToString(rollback), "message": rt.issue})
		})
	}
}
