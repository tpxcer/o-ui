package localapi

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"independent-panel/internal/i18n"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"independent-panel/internal/config"
	"independent-panel/internal/core"
	"independent-panel/internal/diagnostics"
	"independent-panel/internal/engine"
	"independent-panel/internal/monitor"
	"independent-panel/internal/store"
	"independent-panel/internal/traffic"
)

type proxyIdentity struct {
	UUID string
	Auth string
}
type realityIdentity struct {
	PrivateKey string
	ShortID    string
}
type identities struct {
	Clients  map[string]proxyIdentity
	Inbounds map[string]realityIdentity
}
type runtimeRequest struct {
	Version           string `json:"version"`
	WorkspaceRevision int64  `json:"workspaceRevision"`
	SettingsRevision  int64  `json:"settingsRevision"`
	ProxyConfirmation string `json:"proxyConfirmation,omitempty"`
}

type Runtime struct {
	mu          sync.Mutex
	panelMu     sync.Mutex
	backupMu    sync.Mutex
	manager     *core.Manager
	state       *store.Store
	root        string
	lock        *os.File
	engine      engine.Engine
	issue       string
	statsSocket string
	cancel      context.CancelFunc
	done        chan struct{}
	monitor     *monitor.Monitor
	metrics     *engine.MetricsProbe
	logs        *diagnostics.Log
	panelTLS    *PanelTLS
	autoResume  autoResumeState
	plan        *planAccount
	public      *publicLookup
	business    businessTraffic
}

func lockRuntimeDirectory(root string) (*os.File, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, i18n.New("运行配置目录必须是真实目录", "Runtime directory must be a real directory")
	}
	if err = os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".runtime.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err = lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		lock.Close()
		return nil, i18n.New("运行锁必须是普通文件，不能使用链接或特殊文件", "Runtime lock must be a regular file, not a link or special file")
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, i18n.New("本项目运行目录已被占用，不接管已有进程", "This runtime directory is in use; not taking over existing processes")
	}
	return lock, nil
}

func NewRuntime(manager *core.Manager, state *store.Store, root string, panelTLS ...*PanelTLS) (*Runtime, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	lock, err := lockRuntimeDirectory(root)
	if err != nil {
		return nil, err
	}
	rt := &Runtime{manager: manager, state: state, root: root, lock: lock, done: make(chan struct{}), logs: diagnostics.New()}
	rt.logs.Add("panel.started")
	if len(panelTLS) > 0 {
		rt.panelTLS = panelTLS[0]
	}
	ledger, _, err := rt.ledger(context.Background())
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if ledger.Active {
		if err = rt.markStopped(context.Background(), true); err != nil {
			_ = lock.Close()
			return nil, err
		}
		rt.issue = i18n.T("上次未正常停止，已保存用量保留，未采集尾流未知", "Previous run did not stop cleanly; saved usage kept, unmeasured traffic unknown")
	}
	if err = rt.loadAutoResume(context.Background()); err != nil {
		_ = lock.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	rt.monitor = monitor.New(root)
	rt.plan = openPlanAccount(state, rt.monitor.Counters)
	rt.public = newPublicLookup()
	rt.metrics = engine.NewMetricsProbe(&rt.engine)
	go rt.watch(ctx)
	return rt, nil
}

func (rt *Runtime) Close() error {
	rt.metrics.Close()
	rt.plan.close()
	rt.monitor.Close()
	rt.cancel()
	<-rt.done
	rt.mu.Lock()
	defer rt.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_ = rt.stop(ctx)
	err := rt.engine.Close()
	// 统计套接字目录按面板进程创建，核心停止后一并删除，避免每次重启遗留/tmp/ixp-*。
	if dir := filepath.Dir(rt.statsSocket); rt.statsSocket != "" && strings.HasPrefix(dir, "/tmp/ixp-") {
		_ = os.RemoveAll(dir)
		rt.statsSocket = ""
	}
	if rt.lock != nil {
		_ = rt.lock.Close()
		rt.lock = nil
	}
	return err
}

func (rt *Runtime) credentials(ctx context.Context, draft workspaceDraft) (identities, error) {
	doc, err := rt.state.ReadDocument(ctx, "proxy-identities")
	if err != nil {
		return identities{}, err
	}
	ids := identities{Clients: map[string]proxyIdentity{}, Inbounds: map[string]realityIdentity{}}
	if string(doc.Data) != "null" && json.Unmarshal(doc.Data, &ids) != nil {
		return ids, i18n.New("连接凭据损坏，拒绝自动更换身份", "Connection credentials are corrupted; refusing to replace identities automatically")
	}
	if ids.Clients == nil || ids.Inbounds == nil {
		return ids, i18n.New("连接凭据结构损坏", "Connection credential structure is corrupted")
	}
	changed, err := completeIdentities(&ids, draft)
	if err != nil {
		return ids, err
	}
	if changed {
		data, err := json.Marshal(ids)
		if err != nil {
			return ids, err
		}
		if _, err = rt.state.SaveDocument(ctx, "proxy-identities", doc.Revision, data); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

// 备份未应用的草稿时可只补齐包内身份，不先修改当前数据库。
func completeIdentities(ids *identities, draft workspaceDraft) (bool, error) {
	changed := false
	for _, client := range draft.Clients {
		if _, exists := ids.Clients[client.ID]; exists {
			continue
		}
		uuid := make([]byte, 16)
		auth := make([]byte, 32)
		if _, err := rand.Read(uuid); err != nil {
			return false, err
		}
		if _, err := rand.Read(auth); err != nil {
			return false, err
		}
		uuid[6], uuid[8] = (uuid[6]&15)|64, (uuid[8]&63)|128
		ids.Clients[client.ID] = proxyIdentity{UUID: fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]), Auth: base64.RawURLEncoding.EncodeToString(auth)}
		changed = true
	}
	for _, in := range draft.Inbounds {
		if in.Preset != "reality" && in.Preset != "xhttp-reality" {
			continue
		}
		if _, exists := ids.Inbounds[in.ID]; exists {
			continue
		}
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return false, err
		}
		short := make([]byte, 8)
		if _, err = rand.Read(short); err != nil {
			return false, err
		}
		ids.Inbounds[in.ID] = realityIdentity{base64.RawURLEncoding.EncodeToString(key.Bytes()), hex.EncodeToString(short)}
		changed = true
	}
	return changed, nil
}

func buildInbounds(draft workspaceDraft, ids identities, used map[string]traffic.Count, now time.Time, panelTLS ...*PanelTLS) ([]config.CompiledInbound, error) {
	inbounds := []config.Inbound{}
	clients := []config.Client{}
	for _, in := range draft.Inbounds {
		listen, _, err := config.ProxyEndpoint(in.Listen, in.ShareHost)
		if err != nil {
			return nil, err
		}
		item := config.Inbound{ID: in.ID, Preset: in.Preset, Listen: listen, Port: in.Port, Enabled: in.Enabled,
			Sniffing: config.Sniffing{Enabled: in.Sniffing.Enabled, RouteOnly: in.Sniffing.RouteOnly, MetadataOnly: in.Sniffing.MetadataOnly, IPsExcluded: in.Sniffing.IPsExcluded, DomainsExcluded: in.Sniffing.DomainsExcluded}}
		for _, pair := range []struct {
			enabled bool
			name    string
		}{{in.Sniffing.HTTP, "http"}, {in.Sniffing.TLS, "tls"}, {in.Sniffing.QUIC, "quic"}, {in.Sniffing.FakeDNS, "fakedns"}} {
			if pair.enabled {
				item.Sniffing.DestOverride = append(item.Sniffing.DestOverride, pair.name)
			}
		}
		if in.Connection != nil {
			c := in.Connection
			if in.Preset == "hy2" || in.Preset == "xhttp-tls" {
				item.Certificate = &config.Certificate{File: c.CertificateFile, Key: c.KeyFile}
			}
			if in.Preset == "reality" || in.Preset == "xhttp-reality" {
				secret := ids.Inbounds[in.ID]
				item.Reality = &config.Reality{Target: c.Target, ServerNames: []string{c.ServerName}, PrivateKey: secret.PrivateKey, ShortIDs: []string{secret.ShortID}}
				if c.MihomoCompatibility {
					item.Reality.MinClientVer = "1.8.2"
				}
			}
			if strings.HasPrefix(in.Preset, "xhttp-") {
				item.XHTTP = &config.XHTTP{Path: c.Path, Mode: c.Mode}
			}
		}
		inbounds = append(inbounds, item)
	}
	for _, c := range draft.Clients {
		identity := ids.Clients[c.ID]
		item := config.Client{ID: c.ID, InboundID: c.InboundID, Enabled: c.Enabled, QuotaBytes: quotaBytes(c.QuotaGB), UsedBytes: used[c.ID].Total(), IPLimit: uint32(c.IPLimit), UUID: identity.UUID, Auth: identity.Auth, ReverseTag: c.ReverseTag}
		if c.ExpiresAt != nil {
			date := time.UnixMilli(*c.ExpiresAt)
			item.ExpiresAt = &date
		}
		clients = append(clients, item)
	}
	compiled, err := config.CompileInbounds(inbounds, clients, now)
	if err != nil {
		return nil, err
	}
	for _, in := range compiled {
		if in.Stream.TLS == nil {
			continue
		}
		cert := in.Stream.TLS.Certificates[0]
		var certificate, privateKey []byte
		var certErr, keyErr error
		usePanel, serverName := false, ""
		for _, source := range draft.Inbounds {
			if "in-"+source.ID == in.Tag && source.Connection != nil {
				usePanel = source.Connection.UsePanelCertificate
				serverName = source.Connection.ServerName
			}
		}
		if usePanel {
			if len(panelTLS) == 0 || panelTLS[0] == nil {
				return nil, i18n.New("面板HTTPS证书尚未生效；请先配置并重启新面板，或改为手动证书路径", "Panel HTTPS certificate is not active; configure it and restart o-ui, or use manual certificate paths")
			}
			certificate, privateKey = panelTLS[0].cert, panelTLS[0].key
			if serverName == "" {
				for _, name := range panelTLS[0].names {
					if !strings.Contains(name, "*") {
						serverName = name
						break
					}
				}
			}
			if serverName == "" || panelTLS[0].pair.Leaf.VerifyHostname(serverName) != nil {
				return nil, i18n.New("请填写匹配已生效面板证书的具体TLS服务域名", "Enter a specific TLS server name matching the active panel certificate")
			}
		} else {
			certificate, certErr = readPEM(cert.File)
			privateKey, keyErr = readPEM(cert.Key)
		}
		if certErr != nil || keyErr != nil {
			return nil, i18n.New("TLS证书或私钥不存在、不是普通文件或过大", "TLS certificate or key is missing, not a regular file or too large")
		}
		pair, err := tls.X509KeyPair(certificate, privateKey)
		if err != nil {
			return nil, i18n.New("TLS证书与私钥无法解析或不匹配", "TLS certificate and key cannot be parsed or do not match")
		}
		if pair.Leaf == nil || now.Before(pair.Leaf.NotBefore) || !now.Before(pair.Leaf.NotAfter) {
			return nil, i18n.New("TLS证书尚未生效或已经过期", "TLS certificate is not yet valid or has expired")
		}
	}
	return compiled, nil
}

func readPEM(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, i18n.New("证书文件类型或大小无效", "Invalid certificate file type or size")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, i18n.New("证书读取失败或过大", "Certificate read failed or too large")
	}
	return data, nil
}

func (rt *Runtime) candidate(ctx context.Context, request runtimeRequest) (engine.Candidate, error) {
	c := engine.Candidate{Version: request.Version, WorkspaceRevision: request.WorkspaceRevision, SettingsRevision: request.SettingsRevision}
	if request.Version != rt.manager.Selected() {
		return c, store.ErrConflict
	}
	binary, err := rt.manager.Binary(request.Version)
	if err != nil {
		return c, err
	}
	c.Binary = binary
	workspace, err := rt.state.ReadDocument(ctx, "workspace")
	if err != nil {
		return c, err
	}
	settings, err := rt.state.ReadDocument(ctx, "xray-settings")
	if err != nil {
		return c, err
	}
	if workspace.Revision != request.WorkspaceRevision || settings.Revision != request.SettingsRevision {
		return c, store.ErrConflict
	}
	if err = validateWorkspaceDraft(workspace.Data, nil); err != nil {
		return c, err
	}
	var draft workspaceDraft
	_ = json.Unmarshal(workspace.Data, &draft)
	// 只在运行快照解析，草稿仍保留“复用面板证书”的明确选择。
	for index := range draft.Inbounds {
		in := &draft.Inbounds[index]
		if in.Connection == nil || !in.Connection.UsePanelCertificate {
			continue
		}
		if in.Preset != "hy2" && in.Preset != "xhttp-tls" {
			return c, i18n.New("REALITY不使用面板TLS证书", "REALITY does not use the panel TLS certificate")
		}
		if rt.panelTLS != nil && in.Connection.ServerName == "" {
			for _, name := range rt.panelTLS.names {
				if !strings.Contains(name, "*") {
					in.Connection.ServerName = name
					break
				}
			}
		}
		in.Connection.CertificateFile, in.Connection.KeyFile = filepath.Join(rt.root, "panel-cert.pem"), filepath.Join(rt.root, "panel-key.pem")
	}
	ids, err := rt.credentials(ctx, draft)
	if err != nil {
		return c, i18n.New("无法保存独立连接凭据，未应用核心", "Cannot save connection credentials; core not applied")
	}
	ledger, _, err := rt.ledger(ctx)
	if err != nil {
		return c, err
	}
	inbounds, err := buildInbounds(draft, ids, ledger.Totals, time.Now(), rt.panelTLS)
	if err != nil {
		return c, err
	}
	reverseTags := []string{}
	for _, client := range draft.Clients {
		if client.ReverseTag != "" {
			reverseTags = append(reverseTags, client.ReverseTag)
		}
	}
	full, err := compileSettings(settings.Data, reverseTags...)
	if err != nil {
		return c, err
	}
	full["inbounds"] = inbounds
	if full["stats"] != nil {
		if rt.statsSocket == "" {
			rt.statsSocket, err = newStatsAddress()
			if err != nil {
				return c, err
			}
		}
		full["api"] = map[string]any{"tag": "panel-stats", "listen": rt.statsSocket, "services": []string{"StatsService"}}
		c.StatsAddress = "unix://" + rt.statsSocket
	} else {
		for _, client := range draft.Clients {
			if client.Enabled && client.QuotaGB > 0 {
				return c, i18n.New("非零流量额度需要开启流量统计；未应用核心", "Non-zero traffic quotas require traffic statistics; core not applied")
			}
		}
	}
	stage, err := os.MkdirTemp(rt.root, "candidate-")
	if err != nil {
		return c, i18n.New("无法创建受限配置目录", "Cannot create the restricted config directory")
	}
	c.PolicyFile = filepath.Join(stage, "policy.json")
	policyData, _ := json.Marshal(policyFor(draft))
	if err = os.WriteFile(c.PolicyFile, policyData, 0600); err != nil {
		return c, i18n.New("无法保存限制策略快照", "Cannot save the limit policy snapshot")
	}
	// 保存证书快照，之后原路径被替换也不会破坏旧配置回退。
	for index := range inbounds {
		if inbounds[index].Stream.TLS == nil {
			continue
		}
		cert := &inbounds[index].Stream.TLS.Certificates[0]
		usePanel := false
		for _, source := range draft.Inbounds {
			if "in-"+source.ID == inbounds[index].Tag && source.Connection != nil {
				usePanel = source.Connection.UsePanelCertificate
			}
		}
		for kind, path := range map[string]*string{"cert": &cert.File, "key": &cert.Key} {
			var data []byte
			var err error
			if usePanel && kind == "cert" && rt.panelTLS != nil {
				data = rt.panelTLS.cert
			} else if usePanel && kind == "key" && rt.panelTLS != nil {
				data = rt.panelTLS.key
			} else {
				data, err = readPEM(*path)
			}
			if err != nil {
				return c, i18n.New("证书快照读取失败，未应用核心", "Failed to read the certificate snapshot; core not applied")
			}
			target := filepath.Join(stage, fmt.Sprintf("%d-%s.pem", index, kind))
			if err = os.WriteFile(target, data, 0600); err != nil {
				return c, i18n.New("证书快照保存失败，未应用核心", "Failed to save the certificate snapshot; core not applied")
			}
			*path = target
		}
		if _, err = tls.LoadX509KeyPair(cert.File, cert.Key); err != nil {
			return c, i18n.New("证书在读取期间已改变，未应用核心", "Certificate changed while being read; core not applied")
		}
	}
	data, err := json.Marshal(full)
	if err != nil {
		return c, err
	}
	file, err := os.OpenFile(filepath.Join(stage, "config.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return c, i18n.New("无法创建受限运行配置", "Cannot create the restricted runtime configuration")
	}
	c.File, c.Inbounds = file.Name(), len(inbounds)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return c, i18n.New("写入运行配置失败，未应用核心", "Failed to write the runtime configuration; core not applied")
	}
	return c, nil
}

func registerRuntime(mux *http.ServeMux, rt *Runtime) {
	registerRuntimeHistory(mux, rt)
	registerConnections(mux, rt)
	registerTraffic(mux, rt)
	mux.HandleFunc("GET /api/runtime/status", func(w http.ResponseWriter, r *http.Request) {
		if rt == nil {
			respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
			return
		}
		respond(w, 200, rt.engine.Status())
	})
	for _, action := range []string{"preflight", "apply", "stop"} {
		mux.HandleFunc("POST /api/runtime/"+action, func(w http.ResponseWriter, r *http.Request) {
			if rt == nil {
				respond(w, 503, map[string]string{"error": i18n.T("核心运行控制未启用", "Core runtime control is not enabled")})
				return
			}
			var request runtimeRequest
			if !readJSON(w, r, &request, 1024) {
				return
			}
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if action == "stop" {
				if err := rt.stop(r.Context()); err != nil {
					respond(w, 500, map[string]string{"error": err.Error()})
					return
				}
				if err := rt.setAutoResume(r.Context(), false); err != nil {
					respond(w, 500, map[string]string{"error": i18n.T("核心已停止，但自动恢复标记清除失败；面板重启时可能自动恢复，请重试停止", "Core stopped, but clearing the auto-resume mark failed; it may auto-resume when o-ui restarts, try stopping again")})
					return
				}
			} else {
				if err := rt.checkpointCurrent(r.Context()); err != nil {
					respond(w, 422, map[string]string{"error": err.Error()})
					return
				}
				c, err := rt.candidate(r.Context(), request)
				c.Diagnostics = rt.logs
				if err == nil {
					if action == "apply" {
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
			}
			respond(w, 200, rt.engine.Status())
		})
	}
}
