import { addClient, chineseDate, clientStatus, createQuick, defaultConnection, defaultSniffing, deleteInbound, editClient,
  editInbound, escapeHtml as esc, inboundName, presets, randomName, seedWorkspace } from './workspace.ts';
import type { Client, ClientInput, Connection, Inbound, Preset, Workspace } from './workspace.ts';
import { api, APIError, confirmSaved, jsonBody, uncertainSave } from './api.ts';
import { initCalendar } from './calendar.ts';
import { initConnections } from './connections.ts';
import { formatTrafficGB, trafficStatus, usedBytes } from './traffic.ts';
import type { TrafficSnapshot } from './traffic.ts';
import { newRealityConnection, nextRealityTarget, realityServerName, realityTargets, readSniffing, sniffingLabels } from './inbound-options.ts';
import type { PanelTLSStatus } from './panel-settings.ts';
import { readProxyEndpoint } from './proxy-endpoint.ts';
import type { RuntimeStatus } from './runtime.ts';
import { workspaceStatusText } from './workspace-status.ts';
import { regionLabel, type PublicInfo } from './server-plan-model.ts';
import './management.css';
import { lang, t } from './i18n.ts';

const icon = (name: string) => `<i data-lucide="${name}" aria-hidden="true"></i>`;
// 一键创建菜单沿用OUI的四个预设名称。
const quickLabels: Record<Preset, string> = { hy2: 'Hysteria2', reality: 'VLESS Reality Vision', 'xhttp-tls': 'VLESS XHTTP TLS', 'xhttp-reality': 'VLESS XHTTP Reality' };
const input = (label: string, name: string, value: string | number, attrs = '', hint = '') => `<label class="form-field">${label}<input name="${name}" value="${esc(String(value))}" ${attrs}>${hint ? `<small class="form-hint">${hint}</small>` : ''}</label>`;
const check = (label: string, name: string, checked: boolean) => `<label class="check-field"><input type="checkbox" name="${name}"${checked ? ' checked' : ''}><span>${label}</span></label>`;
const badge = (text: string, ok: boolean) => `<span class="status-badge ${ok ? 'is-good' : 'is-muted'}"><b></b>${esc(text)}</span>`;

export function initManagement(root: HTMLElement, refreshIcons: () => void, navigate: (page: string) => void, showDetail: (title: string, text: string) => void, persistent = false, onApply: () => void = () => {}, onSaved: () => void = () => {}): { render: (page: 'inbounds' | 'clients') => void; setRuntime: (status: RuntimeStatus | null, at: number) => void } {
  let state: Workspace = persistent ? { inbounds: [], clients: [] } : seedWorkspace();
  let revision = 0, loaded = !persistent, loading = false, loadError = '', submitting = false, pendingNotice = '', reloadRequired = false;
  let page: 'inbounds' | 'clients' = 'inbounds';
  let search = '', filter = 'all', inboundFilter = '';
  let traffic: TrafficSnapshot | null = null, trafficError = '', trafficLoading = false;
  type IPLimitView = { enforced: boolean; issue: string; clients: Record<string, { online: { ip: string; lastSeen: string }[]; banned: { ip: string; until: string }[]; exempt: { ip: string; until: string }[] }> };
  let ipView: IPLimitView | null = null;
  let subscriptionService: boolean | null = null;
  let runtime: RuntimeStatus | null = null, runtimeAt = 0;
  let region = t('未知地区', 'Unknown region');
  const viewClient = (client: Client): Client => persistent ? { ...client, usedGB: Number(usedBytes(traffic, client.id) ?? 0n) / 1e9 } : client;
  let returnFocus: HTMLElement | null = null;
  const showConnection = persistent ? initConnections(refreshIcons) : null;
  const modal = document.createElement('dialog');
  modal.className = 'edit-dialog'; modal.id = 'edit-dialog'; modal.setAttribute('aria-labelledby', 'edit-title');
  document.body.append(modal);
  const notice = document.createElement('div');
  notice.className = 'save-notice'; notice.setAttribute('role', 'status');
  document.body.append(notice);
  let noticeTimer: ReturnType<typeof setTimeout>;
  function announce(text: string): void {
    if (submitting) { pendingNotice = text; return; }
    notice.textContent = text; clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => { notice.textContent = ''; }, 8000);
  }
  function openModal(title: string, body: string, submitLabel: string, submit: (form: HTMLFormElement) => void): HTMLFormElement {
    returnFocus = document.activeElement as HTMLElement;
    modal.innerHTML = `<form novalidate><header class="dialog-heading"><h2 id="edit-title">${esc(title)}</h2><button type="button" class="icon-button" data-close aria-label="${t('关闭弹窗', 'Close dialog')}">${icon('x')}</button></header>
      <div class="edit-body">${body}<p id="edit-error" class="form-error" role="alert" tabindex="-1" hidden></p></div>
      <footer><button type="button" class="secondary-button" data-close>${t('取消', 'Cancel')}</button>${persistent ? t('<button type="button" class="secondary-button" data-reload>重新读取…</button>', '<button type="button" class="secondary-button" data-reload>Reload…</button>') : ''}<button class="primary-button" type="submit">${esc(submitLabel)}</button></footer></form>`;
    const form = modal.querySelector('form')!;
    modal.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => { if (!submitting && !loading) modal.close(); }));
    modal.querySelector('[data-reload]')?.addEventListener('click', () => { void load(true); });
    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (submitting || loading || !loaded || reloadRequired) return;
      const error = modal.querySelector<HTMLElement>('.form-error')!;
      error.hidden = true;
      form.querySelectorAll('[aria-invalid]').forEach(element => { element.removeAttribute('aria-invalid'); element.removeAttribute('aria-describedby'); });
      const invalid = [...form.elements].find(element => element instanceof HTMLInputElement && !element.validity.valid) as HTMLInputElement | undefined;
      if (invalid) {
        const label = invalid.closest('label')?.childNodes[0]?.textContent?.trim() ?? t('此项', 'This field');
        error.textContent = `${label}${t('输入有误，请按字段要求填写', ': invalid input; check the field requirements')}${invalid.min ? `${t('（最小', ' (min')} ${invalid.min}${invalid.max ? `${t('，最大', ', max')} ${invalid.max}` : ''}${t('）', ')')}` : ''}${t('。', '. ')}`;
        error.hidden = false; invalid.setAttribute('aria-invalid', 'true'); invalid.setAttribute('aria-describedby', 'edit-error'); invalid.focus(); return;
      }
      const previous = structuredClone(state);
      const controls = [...form.querySelectorAll<HTMLInputElement | HTMLButtonElement | HTMLSelectElement | HTMLTextAreaElement>('input,button,select,textarea')];
      const disabled = controls.map(control => control.disabled);
      submitting = true; pendingNotice = ''; updateApplicationStatus(); let writing = false, sent: Workspace = state;
      const before = revision;
      try {
        submit(form);
        controls.forEach(control => { control.disabled = true; });
        form.querySelector<HTMLButtonElement>('[type="submit"]')!.textContent = persistent ? t('正在保存并生效…', 'Saving and applying…') : t('正在处理…', 'Working…');
        if (persistent) {
          writing = true; sent = state;
          const document = await api<{ revision: number; data: Workspace; applied?: boolean; applyError?: string; firewall?: string }>('workspace', jsonBody('PUT', { revision, data: state }));
          revision = document.revision; state = document.data;
          pendingNotice = `${document.applied ? t('已保存并生效', 'Saved and applied') : `${t('已保存，但未生效：', 'Saved, but not applied: ')}${document.applyError ?? ''}`}${document.firewall ? `${t(' · 防火墙：', ' · firewall: ')}${document.firewall}` : ''}`;
          onSaved();
        }
        submitting = false; modal.close(); renderList(); announce(pendingNotice || t('演示数据已更改', 'Demo data changed'));
      }
      catch (reason) {
        if (writing && uncertainSave(reason)) {
          form.querySelector<HTMLButtonElement>('[type="submit"]')!.textContent = t('连接中断，正在确认是否已保存…', 'Connection lost; confirming the save…');
          const confirmed = await confirmSaved('workspace', sent, before + 1);
          if (confirmed) {
            revision = confirmed.revision; state = confirmed.data; submitting = false; onSaved(); modal.close(); renderList();
            announce(t('已保存（连接曾中断，已向服务器确认；生效与防火墙由服务器完成）', 'Saved (the connection dropped, but the server confirmed it; applying and the firewall were finished by the server)'));
            return;
          }
        }
        state = previous; submitting = false; pendingNotice = ''; if (writing && (!(reason instanceof APIError) || reason.status === 409 || reason.status >= 500)) reloadRequired = true;
        controls.forEach((control, index) => { control.disabled = disabled[index]; });
        form.querySelector<HTMLButtonElement>('[type="submit"]')!.disabled = reloadRequired;
        form.querySelector<HTMLButtonElement>('[type="submit"]')!.textContent = submitLabel;
        error.textContent = (reason instanceof Error ? reason.message : t('未保存，请检查输入后重试。', 'Not saved; check the input and retry.')) + (reloadRequired ? t(' 上次保存失败或结果未确认，输入已保留。请先复制需要保留的修改，再重新读取草稿；不会自动合并或覆盖其它页面的数据。', ' The last save failed or was not confirmed; your input is kept. Copy any changes you need, then reload the draft. Data from other pages is never merged or overwritten automatically.') : ''); error.hidden = false; error.focus();
        updateApplicationStatus();
      }
    });
    refreshIcons(); modal.showModal(); updateApplicationStatus();
    return form;
  }
  modal.addEventListener('cancel', event => { if (submitting || loading) event.preventDefault(); });
  modal.addEventListener('close', () => {
    if (reloadRequired) renderList();
    updateApplicationStatus();
    if (returnFocus?.isConnected && returnFocus.getClientRects().length) returnFocus.focus();
    else root.querySelector<HTMLElement>('.page-title')?.focus();
  });
  const clientFields = (client: ClientInput) => `<div class="form-grid">${input(t('客户端名称', 'Client name'), 'name', client.name, 'required maxlength="64" autocomplete="off"')}
    ${input(t('流量上限（GB）', 'Traffic quota (GB)'), 'quotaGB', client.quotaGB, 'type="number" min="0" max="1000000" step="any" required', persistent ? t('0 表示不限；按上传加下载的累计流量计算。', '0 means unlimited; counts total upload plus download.') : t('0 表示不限；用量为演示数值。', '0 means unlimited; usage is demo data.'))}
    ${input(t('IP 上限', 'IP limit'), 'ipLimit', client.ipLimit, 'type="number" min="0" max="10000" step="1" required', persistent ? t('0 表示不限；同时在线的 IP 超过上限时，保留先连上的，新来的 IP 在该入站端口封禁 10 分钟（需要服务器上的防火墙助手）。', '0 means unlimited; when more IPs are online at once than allowed, the first ones are kept and newcomers are banned on that inbound port for 10 minutes (needs the firewall helper on the server).') : t('0 表示不限；演示数据不执行。', '0 means unlimited; not enforced for demo data.'))}</div>
    <div class="form-field expiry-field"><span id="expiry-label">${t('到期时间', 'Expiry')}</span><div id="expiry-picker" role="group" aria-labelledby="expiry-label"></div></div>
    ${check(persistent ? t('允许订阅（保存后在连接信息中生成订阅链接）', 'Allow subscription (generate the link in connection info after saving)') : t('开启订阅（仅保存演示选项，不生成订阅地址）', 'Enable subscription (demo option only; no link is generated)'), 'subscription', client.subscription)}
    ${persistent ? input(t('反向代理标签（可选，仅 VLESS）', 'Reverse tag (optional, VLESS only)'), 'reverseTag', client.reverseTag ?? '', 'maxlength="64" autocomplete="off" placeholder="r-home"', t('填写后，使用此客户端连接的桥接端（如家里运行 Xray 的设备）可建立反向隧道；在 Xray 设置 → 路由规则里把目标选为“反向代理 · 该标签”，匹配的流量就经桥接端访问其所在网络。', 'With a tag, a bridge using this client (such as a home device running Xray) can open a reverse tunnel; in Xray settings → Routing rules, choose "Reverse · this tag" as the target to send matching traffic through the bridge into its network.')) : ''}`;
  const readClient = (form: HTMLFormElement, expiresAt: number | null): ClientInput => {
    const data = new FormData(form);
    return { name: String(data.get('name') ?? ''), quotaGB: Number(data.get('quotaGB')), ipLimit: Number(data.get('ipLimit')), subscription: data.has('subscription'), expiresAt, reverseTag: String(data.get('reverseTag') ?? '').trim() || undefined };
  };
  const refreshField = (label: string, name: string, value: string, action: string, title: string, attrs = '') => `<div class="form-field"><label for="connection-${name}">${label}</label><div class="input-action"><input id="connection-${name}" name="${name}" value="${esc(value)}" maxlength="4096" autocomplete="off" ${attrs}><button type="button" class="icon-button" data-connection="${action}" title="${title}" aria-label="${title}">${icon('refresh-cw')}</button></div></div>`;
  const endpointFields = (inbound?: Inbound) => `<fieldset class="sniffing-field"><legend>${t('代理监听与分享地址', 'Proxy listen and share address')}</legend>${input(t('代理监听 IP', 'Proxy listen IP'), 'listen', inbound ? inbound.listen || '127.0.0.1' : '0.0.0.0', 'required maxlength="45" list="proxy-listens" autocomplete="off"', t('0.0.0.0 监听所有地址，客户端可从外网连接；127.0.0.1 仅本机可用。只影响代理入站，不影响管理面板。', '0.0.0.0 listens on all addresses so clients can connect from outside; 127.0.0.1 is local only. Affects proxy inbounds only, not the panel.'))}<datalist id="proxy-listens"><option value="127.0.0.1"></option><option value="::1"></option><option value="0.0.0.0"></option><option value="::"></option></datalist>${input(t('分享主机（域名或 IP）', 'Share host (domain or IP)'), 'shareHost', inbound?.shareHost || '', 'maxlength="253" autocomplete="off"', t('写入节点链接的服务器地址；新建时优先填入面板证书里的域名，没有证书才填本机公网 IP。域名须直接解析到本机（不能开 CDN 代理）。不含协议、端口或路径。', 'Server address written into node links; new inbounds get the domain from the panel certificate, or the public IP when there is no certificate. The domain must resolve directly to this server (no CDN proxy). No scheme, port or path.'))}<p class="form-hint">${t('端口范围 1024–65535；服务器开了防火墙时请放行该端口。', 'Ports 1024–65535; if the server has a firewall, allow this port.')}</p></fieldset>`;
  const connectionFields = (preset: Preset, connection: Connection = preset === 'hy2' || preset === 'xhttp-tls' ? { ...defaultConnection(), usePanelCertificate: true } : newRealityConnection()) => `<fieldset class="sniffing-field"><legend>${t('连接参数', 'Connection parameters')}</legend><p class="form-hint">${t('UUID／认证值由服务端首次预检生成并保存。REALITY 必须使用公私钥：私钥保留服务端，客户端公钥随连接信息自动提供，无需手填；刷新目标不会更换密钥。', 'The UUID/auth value is generated and saved at the first server preflight. REALITY uses a key pair: the private key stays on the server and the public key is included in the connection info automatically. Changing the target does not change the keys.')}</p>
    ${preset === 'hy2' || preset === 'xhttp-tls' ? check(t('自动复用已生效的面板 HTTPS 证书', 'Reuse the active panel HTTPS certificate'), 'usePanelCertificate', !!connection.usePanelCertificate) + t('<p class="form-hint" data-panel-cert-status role="status">正在读取面板证书状态…</p>', '<p class="form-hint" data-panel-cert-status role="status">Reading panel certificate status…</p>') + `<div data-manual-cert ${connection.usePanelCertificate ? 'hidden' : ''}>` + input(t('证书绝对路径', 'Certificate absolute path'), 'certificateFile', connection.certificateFile, 'maxlength="4096" autocomplete="off"', t('读取本机PEM证书，不上传到远端。', 'Reads a local PEM certificate; nothing is uploaded.')) + input(t('私钥绝对路径', 'Private key absolute path'), 'keyFile', connection.keyFile, 'maxlength="4096" autocomplete="off"') + '</div>' + input(t('TLS 服务域名（SNI）', 'TLS server name (SNI)'), 'serverName', connection.serverName, 'maxlength="253" autocomplete="off"', t('复用时自动填写证书首个域名，可手动修改但必须匹配证书。', 'When reusing, the first certificate domain is filled in; you can change it, but it must match the certificate.')) : refreshField(t('REALITY 目标', 'REALITY target'), 'target', connection.target, 'target', t('换一个预设目标并同步 SNI', 'Pick another preset target and sync SNI'), t('placeholder="域名:443" list="reality-targets"', 'placeholder="domain:443" list="reality-targets"')) + `<datalist id="reality-targets">${realityTargets.map(host => `<option value="${host}:443"></option>`).join('')}</datalist>` + refreshField(t('REALITY 服务域名（SNI）', 'REALITY server name (SNI)'), 'serverName', connection.serverName, 'sni', t('从目标地址填入 SNI', 'Fill SNI from the target')) + t('<p class="form-hint">AMD、Microsoft、Apple、Amazon、特斯拉仅为常用填写候选，可手动修改；未探测外站，不保证在每台服务器可用。</p><p class="form-hint" data-connection-notice role="status"></p>', '<p class="form-hint">AMD, Microsoft, Apple, Amazon and Tesla are only common suggestions and can be changed; external sites are not probed and may not work from every server.</p><p class="form-hint" data-connection-notice role="status"></p>')}
    ${preset === 'reality' || preset === 'xhttp-reality' ? check(t('Mihomo 兼容模式（降低最低客户端版本）', 'Mihomo compatibility (lower minimum client version)'), 'mihomoCompatibility', !!connection.mihomoCompatibility) + t('<p class="form-hint" id="mihomo-compatibility-hint">默认关闭，保留官方客户端限制。开启后最低版本设为 1.8.2；Xray 官方警告此举可能增加被识别、封锁的风险。只保存草稿，应用后才生效。</p>', '<p class="form-hint" id="mihomo-compatibility-hint">Off by default, keeping the official client limit. When on, the minimum version is 1.8.2; Xray warns this may increase the risk of detection and blocking. Saved as a draft; takes effect after applying.</p>') : ''}
    ${preset.startsWith('xhttp-') ? input(t('XHTTP 路径', 'XHTTP path'), 'path', connection.path, 'maxlength="4096" autocomplete="off"') + `<label class="form-field">${t('XHTTP 模式', 'XHTTP mode')}<select name="mode">${['auto', 'packet-up', 'stream-up', 'stream-one'].map(mode => `<option ${connection.mode === mode ? 'selected' : ''}>${mode}</option>`).join('')}</select></label>` : ''}</fieldset>`;
  const sniffingFields = (s = defaultSniffing()) => `<fieldset class="sniffing-field"><legend>${t('嗅探设置', 'Sniffing')}</legend><div class="sniffing-options">${sniffingLabels.map(([key, label]) => check(label, `sniff-${key}`, s[key])).join('')}</div><p class="form-hint">${t('HTTP / TLS / QUIC 从连接内容识别域名；仅路由只用于路由匹配，不替换实际目标。仅元数据不读取内容，普通 HTTP / TLS / QUIC 域名嗅探通常不生效。FakeDNS 需另配对应 DNS，勾选不会自动建立 FakeDNS。', 'HTTP / TLS / QUIC detect domains from connection content; Route only uses them for routing without replacing the destination. Metadata only reads no content, so normal HTTP / TLS / QUIC domain sniffing usually has no effect. FakeDNS needs a matching DNS setup; checking it does not create one.')}</p><label class="form-field" for="sniff-ips">${t('排除的 IP', 'Excluded IPs')}<textarea id="sniff-ips" name="sniff-ips" rows="2" placeholder="192.0.2.1&#10;2001:db8::/32">${esc((s.ipsExcluded ?? []).join('\n'))}</textarea><small class="form-hint">${t('每行一项，支持 IP / CIDR，最多64项；暂不支持外部 Geo 文件。', 'One per line, IP / CIDR, up to 64; external Geo files are not supported.')}</small></label><label class="form-field" for="sniff-domains">${t('排除的域名', 'Excluded domains')}<textarea id="sniff-domains" name="sniff-domains" rows="2" placeholder="domain:example.com&#10;full:example.org">${esc((s.domainsExcluded ?? []).join('\n'))}</textarea><small class="form-hint">${t('每行一项，支持域名、domain:域名、full:域名；跳过嗅探目标改写，不等于阻断访问。关闭嗅探会保留各项选择。', 'One per line: domain, domain:domain or full:domain. Skips destination override but does not block access. Turning sniffing off keeps these choices.')}</small></label></fieldset>`;
  function wireConnection(form: HTMLFormElement): void {
    form.querySelector('[name="mihomoCompatibility"]')?.setAttribute('aria-describedby', 'mihomo-compatibility-hint');
    form.addEventListener('click', event => {
      const button = (event.target as Element).closest<HTMLButtonElement>('[data-connection]');
      if (!button) return;
      const target = form.elements.namedItem('target') as HTMLInputElement | null;
      const name = form.elements.namedItem('serverName') as HTMLInputElement | null;
      if (!target || !name) return;
      let filled = false;
      if (button.dataset.connection === 'target') { const next = nextRealityTarget(target.value); target.value = next.target; name.value = next.serverName; filled = true; }
      else { const next = realityServerName(target.value); if (next) { name.value = next; filled = true; } }
      const notice = form.querySelector('[data-connection-notice]');
      if (notice) notice.textContent = filled ? t('已填入目标对应的 SNI；未联网检测可用性。', 'Filled in the SNI for the target; availability was not checked online.') : t('请先填写域名:端口格式的目标，IP目标需手动指定证书域名；原 SNI 未改动。', 'Enter the target as domain:port first; for an IP target set the certificate domain manually. The SNI was not changed.');
    });
    form.addEventListener('change', event => {
      const item = event.target as HTMLInputElement;
      if (item.name === 'target') {
        const name = form.elements.namedItem('serverName') as HTMLInputElement | null;
        const host = realityServerName(item.value);
        if (name && host) name.value = host;
      }
      if (item.name === 'usePanelCertificate') { const manual = form.querySelector<HTMLElement>('[data-manual-cert]'); if (manual) manual.hidden = item.checked; void updatePanelCertificate(form); }
    });
    void updatePanelCertificate(form);
  }
  // 分享主机优先用面板证书里的具体域名（用户2026-10-21决定），没有证书才用公网IP。
  const certificateDomain = (status: PanelTLSStatus | null) => status?.active ? status.serverNames.find(name => !name.includes('*')) ?? '' : '';
  async function updatePanelCertificate(form: HTMLFormElement): Promise<void> {
    const label = form.querySelector<HTMLElement>('[data-panel-cert-status]'); if (!label) return;
    try {
      const status = await api<PanelTLSStatus>('panel/tls');
      if (!label.isConnected) return;
      label.textContent = status.active ? `${t('已生效证书：', 'Active certificate: ')}${status.serverNames.join(t('、', ', ')) || t('无可用域名', 'no usable names')}${t('。只在服务端复用，不返回私钥。', '. Reused on the server only; the private key is never returned.')}` : t('面板尚无已生效证书。请在“面板设置”配置并重启新面板，或取消复用后手填路径；当前可保存草稿，不能应用。', 'The panel has no active certificate. Configure one in “Panel settings” and restart the panel, or stop reusing it and enter paths manually; you can save the draft but not apply it.');
      if (status.active && !status.serverNames.some(value => !value.includes('*'))) label.textContent += t(' 证书只有通配域名，请手动填写匹配的具体服务域名。', ' The certificate has only wildcard names; enter a matching specific server name.');
      const usePanel = form.elements.namedItem('usePanelCertificate') as HTMLInputElement | null;
      const name = form.elements.namedItem('serverName') as HTMLInputElement | null;
      if (usePanel?.checked && name && !name.value && status.active) name.value = status.serverNames.find(value => !value.includes('*')) ?? '';
    } catch (reason) { if (label.isConnected) label.textContent = (reason as Error).message; }
  }
  const readConnection = (form: HTMLFormElement): Connection => {
    const data = new FormData(form), connection = defaultConnection();
    for (const key of ['certificateFile', 'keyFile', 'target', 'serverName', 'path', 'mode'] as const) { if (data.has(key)) connection[key] = String(data.get(key)).trim(); }
    connection.usePanelCertificate = data.has('usePanelCertificate');
    connection.mihomoCompatibility = data.has('mihomoCompatibility');
    return connection;
  };
  function suggestedPort(preset: Preset): number {
    let port: number = persistent ? Math.max(1024, presets[preset].port) : presets[preset].port;
    while (port < 65535 && state.inbounds.some(item => item.port === port && presets[item.preset].transport === presets[preset].transport)) port++;
    return port;
  }
  function quickMenu(open: boolean): void {
    const menu = root.querySelector<HTMLElement>('.quick-menu');
    if (!menu) return;
    menu.hidden = !open;
    root.querySelector('[data-quick-toggle]')?.setAttribute('aria-expanded', String(open));
    if (open) menu.querySelector<HTMLButtonElement>('button')?.focus();
  }
  document.addEventListener('click', event => { if (!(event.target as Element).closest?.('.quick-create')) quickMenu(false); });
  document.addEventListener('keydown', event => { if (event.key === 'Escape') quickMenu(false); });
  // 参照OUI一键创建：选预设后自动挑空闲端口、生成客户端与名称、公网地址，保存即生效，不弹表单。
  async function quickCreateNow(preset: Preset): Promise<void> {
    if (submitting || loading || !loaded || reloadRequired) return;
    const previous = structuredClone(state), tls = preset === 'hy2' || preset === 'xhttp-tls', proto = preset === 'hy2' ? 'udp' : 'tcp';
    submitting = true; let writing = false, sent: Workspace = state, created = ''; const before = revision; renderList(); notice.textContent = t('正在一键创建…', 'Creating…');
    try {
      const [free, info, cert] = await Promise.all([
        api<{ port: number }>(`system/free-port?transport=${proto}`),
        api<PublicInfo>('system/public').catch(() => null),
        api<PanelTLSStatus>('panel/tls').catch(() => null),
      ]);
      const domain = certificateDomain(cert);
      if (tls && !domain) throw new Error(t('Hysteria2 和 XHTTP TLS 需要面板证书，请先在“面板设置”配置证书，或用“添加入站”手动填写证书路径。', 'Hysteria2 and XHTTP TLS need the panel certificate; configure it in Panel settings first, or use Add inbound to enter certificate paths.'));
      const shareHost = domain || info?.ipv4?.ip || info?.ipv6?.ip || '';
      if (!shareHost) throw new Error(t('面板没有证书域名，也未查询到服务器公网 IP，请用“添加入站”手动填写分享主机。', 'There is no certificate domain and the public IP is unknown; use Add inbound and enter the share host manually.'));
      const client = { name: randomName(), quotaGB: 0, ipLimit: 0, expiresAt: null, subscription: true };
      state = createQuick(state, { ...client, preset, port: free.port, sniffing: defaultSniffing() }, crypto.randomUUID());
      const inbound = state.inbounds.at(-1)!;
      Object.assign(inbound, { name: `${regionLabel(info)}-${presets[preset].label}-${client.name}`, connection: tls ? { ...defaultConnection(), usePanelCertificate: true } : newRealityConnection(), listen: '0.0.0.0', shareHost });
      writing = true; sent = state; created = inbound.name;
      const document = await api<{ revision: number; data: Workspace; applied?: boolean; applyError?: string; firewall?: string }>('workspace', jsonBody('PUT', { revision, data: state }));
      revision = document.revision; state = document.data; submitting = false; search = ''; filter = 'all'; renderList(); onSaved();
      announce(document.applied ? `${inbound.name} ${t('已创建并生效 · 端口', 'created and applied · port')} ${free.port}/${proto}${document.firewall ? `${t(' · 防火墙：', ' · firewall: ')}${document.firewall}` : ''}` : `${inbound.name} ${t('已创建，但未生效：', 'created, but not applied: ')}${document.applyError ?? ''}`);
    } catch (reason) {
      if (writing && uncertainSave(reason)) {
        notice.textContent = t('连接中断，正在确认是否已保存…', 'Connection lost; confirming the save…');
        const confirmed = await confirmSaved('workspace', sent, before + 1);
        if (confirmed) {
          revision = confirmed.revision; state = confirmed.data; submitting = false; search = ''; filter = 'all'; renderList(); onSaved();
          announce(`${created} ${t('已创建（连接曾中断，已向服务器确认；生效与防火墙由服务器完成）', 'created (the connection dropped, but the server confirmed it; applying and the firewall were finished by the server)')}`);
          return;
        }
      }
      state = previous; submitting = false;
      if (writing && (!(reason instanceof APIError) || reason.status === 409 || reason.status >= 500)) reloadRequired = true;
      renderList(); announce((reason as Error).message);
    }
  }
  function quickCreate(): void {
    let preset: Preset = 'reality';
    const demoClient = { name: randomName(), quotaGB: persistent ? 0 : 100, ipLimit: persistent ? 0 : 2, expiresAt: null, subscription: false };
    let date: { value: () => number | null };
    const form = openModal(persistent ? t('添加入站', 'Add inbound') : t('一键创建', 'Quick create'), `<p class="form-intro">${persistent ? t('同时创建入站和一个客户端，保存后立即生效。', 'Creates an inbound and one client; changes apply as soon as you save.') : t('同时添加一条演示入站和一个客户端，不生成连接凭据，不应用到 Xray。', 'Adds a demo inbound and a client together; no credentials are generated and Xray is not applied.')}</p>
      <fieldset class="preset-field"><legend>${t('创建类型', 'Type')}</legend><div class="preset-options">${(Object.keys(presets) as Preset[]).map(key => `<label><input type="radio" name="preset" value="${key}"${key === preset ? ' checked' : ''}><span>${presets[key].label}</span></label>`).join('')}</div></fieldset>
      <div class="name-preview"><small>${t('入站名称预览', 'Inbound name preview')}</small><strong id="inbound-name-preview">${esc(persistent ? `${region}-${presets[preset].label}-${demoClient.name}` : inboundName(preset, demoClient.name))}</strong><small>${persistent ? region === t('未知地区', 'Unknown region') ? t('尚未查询到服务器国家，不用演示国家冒充实际归属地。', 'The server country is not known yet; no demo country is used in its place.') : t('国家来自首页公网信息查询，可在保存后编辑名称。', 'The country comes from the public IP lookup on the dashboard; you can edit the name after saving.') : t('国家固定为演示“美国”，未查询服务器归属地。', 'The country is the demo “United States”; the server location was not looked up.')}</small></div>
      <div class="port-row">${input(t('监听端口', 'Listen port'), 'port', persistent ? Math.max(1024, suggestedPort(preset)) : suggestedPort(preset), `type="number" required min="${persistent ? 1024 : 1}" max="65535" step="1"`, t('仅检查草稿列表冲突，应用时还需检查系统端口。', 'Only conflicts within drafts are checked; system ports are checked when applying.'))}<button type="button" class="secondary-button" id="random-name">${icon('shuffle')}${t('重新生成名称', 'Regenerate name')}</button></div>
      ${persistent ? endpointFields() : ''}<div id="connection-fields">${persistent ? connectionFields(preset) : ''}</div>${clientFields(demoClient)}${sniffingFields()}`, persistent ? t('保存', 'Save') : t('创建演示入站', 'Create demo inbound'), form => {
      state = createQuick(state, { ...readClient(form, date.value()), preset, port: Number(new FormData(form).get('port')), sniffing: readSniffing(new FormData(form)) }, crypto.randomUUID());
      if (persistent) { const inbound = state.inbounds.at(-1)!; inbound.name = `${region}-${presets[preset].label}-${state.clients.at(-1)!.name}`; inbound.connection = readConnection(form); Object.assign(inbound, readProxyEndpoint(new FormData(form))); }
      search = ''; filter = 'all'; inboundFilter = '';
      announce(t('已添加演示入站和客户端 · 未应用 Xray', 'Demo inbound and client added · Xray not applied'));
    });
    date = initCalendar(form.querySelector('#expiry-picker')!, null, refreshIcons);
    wireConnection(form);
    const connections: Partial<Record<Preset, Connection>> = {};
    const nameInput = form.elements.namedItem('name') as HTMLInputElement;
    const preview = () => { form.querySelector('#inbound-name-preview')!.textContent = persistent ? `${region}-${presets[preset].label}-${nameInput.value}` : inboundName(preset, nameInput.value); };
    nameInput.addEventListener('input', preview);
    if (persistent) void Promise.all([api<PublicInfo>('system/public').catch(() => null), api<PanelTLSStatus>('panel/tls').catch(() => null)]).then(([info, cert]) => {
      if (info) { region = regionLabel(info); if (form.isConnected) preview(); }
      const share = form.querySelector<HTMLInputElement>('[name="shareHost"]'), host = certificateDomain(cert) || info?.ipv4?.ip || info?.ipv6?.ip;
      if (share && !share.value && host) share.value = host;
    });
    form.querySelector('#random-name')!.addEventListener('click', () => { nameInput.value = randomName(); preview(); });
    form.querySelectorAll<HTMLInputElement>('[name="preset"]').forEach(radio => radio.addEventListener('change', () => {
      if (persistent) connections[preset] = readConnection(form);
      preset = radio.value as Preset;
      if (persistent) { form.querySelector('#connection-fields')!.innerHTML = connectionFields(preset, connections[preset]); refreshIcons(); void updatePanelCertificate(form); }
      (form.elements.namedItem('port') as HTMLInputElement).value = String(persistent ? Math.max(1024, suggestedPort(preset)) : suggestedPort(preset)); preview();
    }));
  }
  function clientEditor(client?: Client): void {
    if (!state.inbounds.length) { showDetail(t('先创建入站', 'Create an inbound first'), t('当前没有入站草稿。请先在入站列表使用一键创建，再为该入站添加客户端。', 'There are no inbound drafts. Use Quick create on the inbound list first, then add clients to it.')); return; }
    const value = client ?? { name: randomName(), quotaGB: persistent ? 0 : 100, ipLimit: persistent ? 0 : 2, expiresAt: null, subscription: false };
    let date: { value: () => number | null };
    const form = openModal(client ? t('编辑客户端', 'Edit client') : t('添加客户端', 'Add client'), `<p class="form-intro">${persistent ? t('保存后立即生效。', 'Changes apply as soon as you save.') : t('仅修改本页演示数据，流量、到期和 IP 限制尚未由核心执行。', 'Changes only demo data on this page; traffic, expiry and IP limits are not enforced by the core.')}</p>
      ${client ? `<div class="association">${t('关联入站：', 'Linked inbound: ')}${esc(state.inbounds.find(item => item.id === client.inboundId)!.name)}</div>` : `<label class="form-field">${t('关联入站', 'Linked inbound')}<select name="inboundId">${state.inbounds.map(item => `<option value="${esc(item.id)}"${item.id === inboundFilter ? ' selected' : ''}>${esc(item.name)}</option>`).join('')}</select></label>`}
      ${clientFields(value)}${client ? check(t('启用客户端', 'Enable client'), 'enabled', client.enabled) : ''}`, persistent ? t('保存', 'Save') : client ? t('保存演示更改', 'Save demo changes') : t('添加演示客户端', 'Add demo client'), form => {
      const data = new FormData(form), input = readClient(form, date.value());
      state = client ? editClient(state, client.id, { ...input, enabled: data.has('enabled') }) : addClient(state, String(data.get('inboundId')), input, crypto.randomUUID());
      if (!client) { search = ''; filter = 'all'; }
      announce(t('已保存演示客户端 · 未应用 Xray', 'Demo client saved · Xray not applied'));
    });
    date = initCalendar(form.querySelector('#expiry-picker')!, value.expiresAt, refreshIcons);
  }
  function inboundEditor(inbound: Inbound): void {
    const form = openModal(t('编辑入站', 'Edit inbound'), `<p class="form-intro">${presets[inbound.preset].label} · ${presets[inbound.preset].protocol} · ${presets[inbound.preset].transport} · ${presets[inbound.preset].security}${t('，', ', ')}${persistent ? t('保存后立即生效', 'applies on save') : t('仅修改演示数据', 'demo data only')}${t('。', '. ')}</p>
      ${input(t('入站名称', 'Inbound name'), 'name', inbound.name, 'required maxlength="128"')}${input(t('监听端口', 'Listen port'), 'port', inbound.port, `type="number" min="${persistent ? 1024 : 1}" max="65535" step="1" required`)}
      ${persistent ? endpointFields(inbound) + connectionFields(inbound.preset, inbound.connection) : ''}
      ${check(t('启用入站', 'Enable inbound'), 'enabled', inbound.enabled)}
      ${sniffingFields(inbound.sniffing)}`, persistent ? t('保存', 'Save') : t('保存演示更改', 'Save demo changes'), form => {
      const data = new FormData(form);
      const sniffing = readSniffing(data);
      state = editInbound(state, inbound.id, { name: String(data.get('name')), port: Number(data.get('port')), enabled: data.has('enabled'), sniffing });
      if (persistent) Object.assign(state.inbounds.find(item => item.id === inbound.id)!, { connection: readConnection(form), ...readProxyEndpoint(data) });
      announce(t('已保存演示入站 · 未应用 Xray', 'Demo inbound saved · Xray not applied'));
    });
    wireConnection(form);
  }
  // 在线IP与封禁明细；解封后30分钟内放行该IP，不计入上限。
  async function ipDialog(client: Client): Promise<void> {
    returnFocus = document.activeElement as HTMLElement;
    let busy = false;
    const time = (value: string) => new Date(value).toLocaleTimeString(lang === 'en' ? 'en' : 'zh-CN', { hour12: false });
    const render = (message = '') => {
      const view = ipView?.clients[client.id] ?? { online: [], banned: [], exempt: [] };
      const list = (items: string[], empty: string) => items.length ? `<ul class="ip-list">${items.join('')}</ul>` : `<p class="form-hint">${empty}</p>`;
      modal.innerHTML = `<form novalidate><header class="dialog-heading"><h2 id="edit-title">${t('在线 IP', 'Online IPs')} · ${esc(client.name)}</h2><button type="button" class="icon-button" data-close aria-label="${t('关闭弹窗', 'Close dialog')}">${icon('x')}</button></header><div class="edit-body">
        <p class="form-intro">${client.ipLimit ? `${t('IP 上限 ', 'IP limit ')}${client.ipLimit}${t('：同时在线超过上限时保留先连上的，新来的 IP 在该入站端口封禁 10 分钟；手机换网后旧 IP 2 分钟没有新连接会让出位置。', ': when more IPs are online at once, the first ones are kept and newcomers are banned on that inbound port for 10 minutes; after a phone switches networks, an old IP with no new connection for 2 minutes gives up its slot.')}` : t('未设置 IP 上限，只显示在线 IP。', 'No IP limit is set; online IPs are shown only.')}</p>
        ${ipView && !ipView.enforced && !ipView.issue ? `<p class="form-error">${t('服务器没有防火墙助手，IP 上限只能统计、不能执行。请用新版安装脚本运行 o-ui update。', 'The server has no firewall helper, so IP limits are only counted, not enforced. Run o-ui update with the new installer.')}</p>` : ''}
        ${ipView?.issue ? `<p class="form-error">${esc(ipView.issue)}</p>` : ''}
        <h3>${t('在线', 'Online')}</h3>${list(view.online.map(item => `<li><code>${esc(item.ip)}</code><small class="table-secondary">${t('最近新连接 ', 'Last new connection ')}${time(item.lastSeen)}</small></li>`), t('当前没有在线 IP（核心未运行或客户端没有连接）。', 'No IP is online now (the core is stopped or the client is not connected).'))}
        <h3>${t('已封禁', 'Banned')}</h3>${list(view.banned.map(item => `<li><code>${esc(item.ip)}</code><small class="table-secondary">${t('解封时间 ', 'Until ')}${time(item.until)}</small><button type="button" class="secondary-button" data-unban="${esc(item.ip)}" ${busy ? 'disabled' : ''}>${t('解封', 'Unban')}</button></li>`), t('没有被封禁的 IP。', 'No IP is banned.'))}
        ${view.exempt.length ? `<h3>${t('手动放行中', 'Allowed manually')}</h3>${list(view.exempt.map(item => `<li><code>${esc(item.ip)}</code><small class="table-secondary">${t('放行到 ', 'Allowed until ')}${time(item.until)}</small></li>`), '')}` : ''}
        <p class="form-hint" role="status">${esc(message)}</p></div>
        <footer><button type="button" class="secondary-button" data-close>${t('关闭', 'Close')}</button></footer></form>`;
      modal.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => modal.close()));
      modal.querySelector('form')!.addEventListener('submit', event => event.preventDefault());
      modal.querySelectorAll<HTMLButtonElement>('[data-unban]').forEach(button => button.addEventListener('click', async () => {
        if (busy) return;
        busy = true; render(t('正在解封…', 'Unbanning…'));
        try { ipView = await api<IPLimitView>('runtime/iplimit/unban', jsonBody('POST', { clientId: client.id, ip: button.dataset.unban })); busy = false; render(t('已解封，30 分钟内放行该 IP。', 'Unbanned; this IP is allowed for 30 minutes.')); }
        catch (reason) { busy = false; render((reason as Error).message); }
      }));
      refreshIcons();
    };
    render(t('正在读取…', 'Loading…'));
    if (!modal.open) modal.showModal();
    try { ipView = await api<IPLimitView>('runtime/iplimit'); if (modal.open) render(); }
    catch (reason) { if (modal.open) render((reason as Error).message); }
  }
  function confirm(title: string, text: string, label: string, apply: () => void): void {
    openModal(persistent ? title.replaceAll(t('演示', 'demo '), '') : title, `<p class="confirm-copy">${esc(persistent ? text.replaceAll(t('演示', 'demo '), '') : text)}</p><p class="form-hint">${persistent ? t('确认后立即保存并生效。', 'Saved and applied right after you confirm.') : t('仅影响当前内存中的演示数据，刷新恢复。不会操作 VPS。', 'Affects only in-memory demo data; refreshing restores it.')}</p>`, label, () => { apply(); announce(t('演示数据已更改 · 未应用 Xray', 'Demo data changed · Xray not applied')); });
  }
  function operate(action: string, id: string): void {
    const inbound = state.inbounds.find(item => item.id === id), client = state.clients.find(item => item.id === id);
    if (action === 'connection' && client) showConnection?.(client.id, client.name, revision);
    if (action === 'iplimit' && client) void ipDialog(client);
    if (action === 'edit-inbound' && inbound) inboundEditor(inbound);
    if (action === 'edit-client' && client) clientEditor(client);
    if (action === 'clients' && inbound) { inboundFilter = id; search = ''; filter = 'all'; navigate('clients'); }
    if (action === 'toggle-inbound' && inbound) confirm(inbound.enabled ? t('停用演示入站', 'Disable demo inbound') : t('启用演示入站', 'Enable demo inbound'), `${t('入站：', 'Inbound: ')}${inbound.name}${t('。', '. ')}${inbound.enabled ? `${t('停用后，其', 'After disabling, its')} ${state.clients.filter(item => item.inboundId === id).length} ${t('个关联客户端会显示“入站已停用”。', 'linked clients will show “Inbound disabled”.')}` : t('启用后仍按客户端各自的启用、到期和流量条件显示演示状态。', 'After enabling, each client\'s demo status still follows its own enablement, expiry and traffic.')}`, inbound.enabled ? t('确认停用', 'Confirm disable') : t('确认启用', 'Confirm enable'), () => {
      state = editInbound(state, id, { ...inbound, enabled: !inbound.enabled });
    });
    if (action === 'toggle-client' && client) confirm(client.enabled ? t('停用演示客户端', 'Disable demo client') : t('启用演示客户端', 'Enable demo client'), `${t('客户端：', 'Client: ')}${client.name}${t('。只切换手动启用状态，不会重置用量、到期时间或流量上限。', '. Only the manual enable state changes; usage, expiry and quota are not reset.')}`, client.enabled ? t('确认停用', 'Confirm disable') : t('确认启用', 'Confirm enable'), () => {
      state = editClient(state, id, { ...client, enabled: !client.enabled });
    });
    if (action === 'delete-inbound' && inbound) {
      if (state.clients.some(item => item.inboundId === id)) { showDetail(t('暂不能删除入站', 'Cannot delete inbound yet'), t('该入站仍有关联客户端。请逐个处理客户端后再删除入站，不会自动删除关联数据。', 'This inbound still has linked clients. Handle them one by one before deleting it; linked data is never deleted automatically.')); return; }
      confirm(t('删除一个演示入站', 'Delete a demo inbound'), `${t('仅删除“', 'Deletes only “')}${inbound.name}${t('”。', '”.')}`, t('确认删除', 'Confirm delete'), () => { state = deleteInbound(state, id); });
    }
    if (action === 'delete-client' && client) confirm(t('删除一个演示客户端', 'Delete a demo client'), `${t('仅删除客户端“', 'Deletes only client “')}${client.name}${t('”，保留其关联入站及其他客户端。', '”; its linked inbound and other clients are kept.')}`, t('确认删除', 'Confirm delete'), () => {
      state = { ...state, clients: state.clients.filter(item => item.id !== id) };
    });
  }
  // 面板设置里关闭了订阅服务时，所有订阅链接都不返回内容；此时不再按客户端各自的选项显示“订阅开启”。
  const subscriptionLabel = (item: Client) => persistent && subscriptionService === false ? t('订阅已停用（全局）', 'Subscriptions off (global)') : item.subscription ? t('订阅开启', 'Subscription on') : t('订阅关闭', 'Subscription off');
  // 在线IP数/上限与封禁数（真实模式点开看明细与解封）；演示数据只显示上限。
  const ipCell = (item: Client) => {
    if (!persistent) return item.ipLimit ? `${item.ipLimit} ${t('个 IP', 'IPs')}` : t('IP 不限', 'Unlimited IPs');
    const view = ipView?.clients[item.id], online = view?.online.length ?? 0, banned = view?.banned.length ?? 0;
    return `<button class="text-button" data-op="iplimit" data-id="${esc(item.id)}">${online} / ${item.ipLimit || '∞'} ${t('在线 IP', 'online IPs')}</button>${banned ? `<small class="table-secondary danger-text">${t('已封禁 ', 'Banned ')}${banned}</small>` : ''}`;
  };
  const rowActions = (item: Client | Inbound, type: 'inbound' | 'client') => `<div class="row-actions">${type === 'client' && persistent ? `<button class="icon-button" data-op="connection" data-id="${esc(item.id)}" aria-label="${t('连接信息', 'Connection info')} ${esc(item.name)}" title="${t('连接信息', 'Connection info')}" ${loading || reloadRequired ? 'disabled' : ''}>${icon('qr-code')}</button>` : ''}<button class="icon-button" data-op="edit-${type}" data-id="${esc(item.id)}" aria-label="${type === 'inbound' ? t('编辑入站', 'Edit inbound') : t('编辑客户端', 'Edit client')} ${esc(item.name)}" title="${t('编辑', 'Edit')}" ${loading || reloadRequired ? 'disabled' : ''}>${icon('pencil')}</button><button class="icon-button" data-op="toggle-${type}" data-id="${esc(item.id)}" aria-label="${item.enabled ? t('停用', 'Disable') : t('启用', 'Enable')} ${esc(item.name)}" title="${item.enabled ? t('停用', 'Disable') : t('启用', 'Enable')}" ${loading || reloadRequired ? 'disabled' : ''}>${icon('power')}</button><button class="icon-button danger-icon" data-op="delete-${type}" data-id="${esc(item.id)}" aria-label="${type === 'inbound' ? t('删除入站', 'Delete inbound') : t('删除客户端', 'Delete client')} ${esc(item.name)}" title="${t('删除', 'Delete')}" ${loading || reloadRequired ? 'disabled' : ''}>${icon('trash-2')}</button></div>`;
  function listRows(): void {
    const query = search.trim().toLocaleLowerCase();
    const contains = (text: string) => text.toLocaleLowerCase().includes(query);
    const inboundRows = state.inbounds.filter(item => contains(`${item.name} ${presets[item.preset].label} ${item.port}`) && (filter === 'all' || item.enabled === (filter === 'enabled')));
    const now = Date.now();
    const clientRows = state.clients.filter(item => {
      const inbound = state.inbounds.find(record => record.id === item.inboundId);
      return contains(`${item.name} ${inbound?.name ?? ''}`) && (!inboundFilter || item.inboundId === inboundFilter) && (filter === 'all' || (filter === 'available' ? clientStatus(viewClient(item), inbound, now) === t('可用', 'Available') : clientStatus(viewClient(item), inbound, now) !== t('可用', 'Available')));
    });
    const count = page === 'inbounds' ? inboundRows.length : clientRows.length;
    root.querySelector('#result-count')!.textContent = `${t('显示', 'Showing')} ${count} / ${page === 'inbounds' ? state.inbounds.length : state.clients.length} ${t('条', '')}`;
    root.querySelector('#list-content')!.innerHTML = count ? `<table class="data-table"><caption class="sr-only">${page === 'inbounds' ? persistent ? t('入站列表', 'Inbounds') : t('入站演示列表', 'Demo inbounds') : persistent ? t('客户端列表', 'Clients') : t('客户端演示列表', 'Demo clients')}</caption><thead><tr>${(page === 'inbounds' ? [t('入站名称', 'Inbound name'), t('类型 / 端口', 'Type / port'), t('状态', 'Status'), t('客户端', 'Clients'), t('操作', 'Actions')] : [t('客户端 / 入站', 'Client / inbound'), persistent ? t('状态', 'Status') : t('演示状态', 'Demo status'), t('流量（GB）', 'Traffic (GB)'), t('到期时间', 'Expiry'), t('IP / 订阅', 'IP / subscription'), t('操作', 'Actions')]).map(label => `<th scope="col">${label}</th>`).join('')}</tr></thead><tbody>${page === 'inbounds' ? inboundRows.map(item => `<tr>
      <td data-label="${t('入站名称', 'Inbound name')}"><strong class="record-name">${esc(item.name)}</strong><small class="table-secondary">${presets[item.preset].protocol} · ${presets[item.preset].security}</small></td>
      <td data-label="${t('类型 / 端口', 'Type / port')}"><span class="tag">${presets[item.preset].label}</span><small class="table-secondary">${presets[item.preset].transport} / ${item.port}</small></td>
      <td data-label="${t('状态', 'Status')}">${badge(persistent ? item.enabled ? t('已启用', 'Enabled') : t('已停用', 'Disabled') : item.enabled ? t('已启用', 'Enabled') : t('已停用', 'Disabled'), item.enabled)}</td>
      <td data-label="${t('客户端', 'Clients')}"><button class="text-button" data-op="clients" data-id="${esc(item.id)}">${state.clients.filter(client => client.inboundId === item.id).length} ${t('个客户端', 'clients')}</button></td><td data-label="${t('操作', 'Actions')}">${rowActions(item, 'inbound')}</td></tr>`).join('') : clientRows.map(item => {
        const inbound = state.inbounds.find(record => record.id === item.inboundId)!;
        const status = clientStatus(viewClient(item), inbound, now);
        const bytes = persistent ? usedBytes(traffic, item.id) : null;
        const percent = item.quotaGB > 0 ? Math.min(100, viewClient(item).usedGB / item.quotaGB * 100) : 0;
        return `<tr data-client-row="${esc(item.id)}"><td data-label="${t('客户端 / 入站', 'Client / inbound')}"><strong class="record-name">${esc(item.name)}</strong><small class="table-secondary">${esc(inbound.name)}</small></td>
          <td data-label="${persistent ? t('状态', 'Status') : t('演示状态', 'Demo status')}" data-client-status>${badge(persistent && status === t('可用', 'Available') ? bytes === null ? t('可用 · 用量未知', 'Available · usage unknown') : t('可用', 'Available') : status, status === t('可用', 'Available'))}</td><td data-label="${t('流量（GB）', 'Traffic (GB)')}" data-client-traffic><span>${persistent ? formatTrafficGB(bytes) : item.usedGB} / ${item.quotaGB || t('不限', 'Unlimited')}</span>${item.quotaGB && (!persistent || bytes !== null) ? `<div class="mini-meter" aria-hidden="true"><span style="width:${percent}%"></span></div>` : ''}</td>
          <td data-label="${t('到期时间', 'Expiry')}"><span class="date-value">${chineseDate(item.expiresAt)}</span></td><td data-label="${t('IP / 订阅', 'IP / subscription')}" data-client-ip>${ipCell(item)}<small class="table-secondary">${subscriptionLabel(item)}</small></td><td data-label="${t('操作', 'Actions')}">${rowActions(item, 'client')}</td></tr>`;
      }).join('')}</tbody></table>` : `<div class="empty-state">${icon('search')}<h2>${page === 'inbounds' ? t('没有匹配的入站', 'No matching inbounds') : t('没有匹配的客户端', 'No matching clients')}</h2><p>${t('试试其他关键词或清除筛选。', 'Try other keywords or clear the filters.')}</p><button class="secondary-button" data-clear>${t('清除搜索与筛选', 'Clear search and filters')}</button></div>`;
    refreshIcons();
  }
  function renderList(): void {
    const available = state.clients.filter(item => clientStatus(viewClient(item), state.inbounds.find(record => record.id === item.inboundId)) === t('可用', 'Available')).length;
    root.innerHTML = `<header class="page-heading"><div><h1 class="page-title" tabindex="-1">${page === 'inbounds' ? t('入站列表', 'Inbounds') : t('客户端', 'Clients')}</h1><p>${page === 'inbounds' ? t('创建和管理入站，保存后立即生效。', 'Create and manage inbounds; changes apply as soon as you save.') : persistent ? t('管理客户端，查看累计流量与到期时间。', 'Manage clients and view traffic totals and expiry.') : t('管理客户端演示数据与到期时间。', 'Manage demo client data and expiry.')}</p></div>${page === 'inbounds' && persistent ? `<div class="create-actions"><button class="primary-button create-button" data-create ${!loaded || loading || reloadRequired ? 'disabled' : ''}>${icon('plus')}${t('添加入站', 'Add inbound')}</button><div class="quick-create"><button class="secondary-button" data-quick-toggle aria-haspopup="menu" aria-expanded="false" ${!loaded || loading || submitting || reloadRequired ? 'disabled' : ''}>${icon('zap')}${t('一键创建', 'Quick create')}</button><div class="quick-menu" role="menu" hidden>${(Object.keys(quickLabels) as Preset[]).map(key => `<button type="button" role="menuitem" data-quick-preset="${key}">${quickLabels[key]}</button>`).join('')}</div></div></div>` : `<button class="primary-button create-button" data-create ${!loaded || loading || reloadRequired ? 'disabled' : ''}>${icon('plus')}${page === 'inbounds' ? t('一键创建', 'Quick create') : t('添加客户端', 'Add client')}</button>`}</header>
      ${loading ? t('<p class="core-message" role="status">正在读取独立数据库…</p>', '<p class="core-message" role="status">Reading the database…</p>') : loadError ? `<p class="form-error" role="alert">${esc(loadError)}</p>` : ''}
      ${reloadRequired ? t('<p class="form-error" role="alert">保存失败或结果未确认，当前列表可能已过期；请重新读取草稿后再修改，不会自动覆盖最新数据。</p>', '<p class="form-error" role="alert">The save failed or was not confirmed, so this list may be stale. Reload the draft before editing; the latest data is never overwritten automatically.</p>') : ''}${persistent ? `<button class="secondary-button" data-reload ${loading || submitting ? 'disabled' : ''}>${t('刷新', 'Refresh')}</button>` : ''}
      <div class="workspace-summary"><span>${t('入站', 'Inbounds')} <strong>${state.inbounds.length}</strong></span><span>${t('客户端', 'Clients')} <strong>${state.clients.length}</strong></span><span>${persistent ? t('可用', 'Available') : t('演示可用', 'Demo available')} <strong>${available}</strong></span>${persistent ? `<button class="secondary-button" data-apply ${!loaded || loading || submitting || reloadRequired ? 'disabled' : ''}>${t('重新应用', 'Re-apply')}</button>` : ''}</div>
      ${persistent ? '<p class="form-hint" id="workspace-application-status" role="status"></p>' : ''}
      ${persistent ? `<p class="form-hint" id="traffic-status">${esc(trafficStatus(traffic, trafficError))}</p><p class="form-hint">${t('流量约每2秒统计一次；客户端到期或用尽额度时会自动停用。', 'Traffic is counted about every 2 seconds; clients stop working automatically when they expire or run out of quota.')}</p>` : ''}
      <section class="card list-card" aria-label="${page === 'inbounds' ? t('入站管理', 'Inbound management') : t('客户端管理', 'Client management')}"><div class="list-toolbar"><label class="search-box">${icon('search')}<span class="sr-only">${page === 'inbounds' ? t('搜索入站', 'Search inbounds') : t('搜索客户端', 'Search clients')}</span><input type="search" id="workspace-search" value="${esc(search)}" placeholder="${page === 'inbounds' ? t('搜索名称、类型或端口', 'Search name, type or port') : t('搜索客户端或入站名称', 'Search client or inbound name')}" autocomplete="off"></label>
        <label class="filter-control"><span class="sr-only">${t('筛选状态', 'Filter by status')}</span><select id="status-filter">${(page === 'inbounds' ? [['all', t('全部状态', 'All states')], ['enabled', t('已启用', 'Enabled')], ['disabled', t('已停用', 'Disabled')]] : [['all', t('全部状态', 'All states')], ['available', persistent ? t('可用', 'Available') : t('演示可用', 'Demo available')], ['unavailable', persistent ? t('不可用', 'Unavailable') : t('演示不可用', 'Demo unavailable')]]).map(([value, label]) => `<option value="${value}"${value === filter ? ' selected' : ''}>${label}</option>`).join('')}</select></label>
        ${page === 'clients' ? `<label class="filter-control inbound-filter"><span class="sr-only">${t('筛选关联入站', 'Filter by linked inbound')}</span><select id="inbound-filter"><option value="">${t('全部入站', 'All inbounds')}</option>${state.inbounds.map(item => `<option value="${esc(item.id)}"${item.id === inboundFilter ? ' selected' : ''}>${esc(item.name)}</option>`).join('')}</select></label>` : ''}<span id="result-count" class="result-count" aria-live="polite"></span></div><div id="list-content"></div>
      <footer class="list-footer">${persistent ? t('在客户端的连接信息里可查看节点链接、二维码和订阅链接。', 'Open a client\'s connection info for node links, QR codes and subscription links.') : t('所有更改仅保留在本页内存中，刷新即恢复。未生成可用连接或订阅链接。', 'All changes stay in this page\'s memory and are restored on refresh. No usable connections or subscription links are generated.')}</footer></section>`;
    root.querySelector<HTMLInputElement>('#workspace-search')!.addEventListener('input', event => { search = (event.target as HTMLInputElement).value; listRows(); });
    root.querySelector<HTMLSelectElement>('#status-filter')!.addEventListener('change', event => { filter = (event.target as HTMLSelectElement).value; listRows(); });
    root.querySelector<HTMLSelectElement>('#inbound-filter')?.addEventListener('change', event => { inboundFilter = (event.target as HTMLSelectElement).value; listRows(); });
    listRows();
    updateApplicationStatus();
  }
  function updateApplicationStatus(): void {
    const label = root.querySelector<HTMLElement>('#workspace-application-status');
    if (!label) return;
    const text = loading ? t('正在读取已保存入站和客户端草稿…', 'Reading saved inbound and client drafts…') : !loaded ? t('草稿尚未读取，无法核对应用状态。', 'The draft is not loaded; cannot check the apply status.') : submitting ? t('正在保存并应用…', 'Saving and applying…') : reloadRequired ? t('上次保存失败或结果未确认，当前列表可能已过期；请先重新读取草稿，暂不判断此修订已应用。', 'The last save failed or was not confirmed, so this list may be stale; reload the draft first. Whether this revision is applied is not determined.') : `${modal.open ? '' : ''}${workspaceStatusText(persistent, revision, runtime, runtimeAt)}`;
    if (label.textContent !== text) label.textContent = text;
  }
  root.addEventListener('click', event => {
    const button = (event.target as Element).closest<HTMLButtonElement>('button');
    if (!button) return;
    if (button.hasAttribute('data-reload')) { void load(true); return; }
    if (button.hasAttribute('data-apply')) { if (loaded && !loading && !submitting && !reloadRequired) onApply(); return; }
    if (!loaded || loading || submitting || reloadRequired) return;
    if (button.hasAttribute('data-create')) { if (page === 'inbounds') quickCreate(); else clientEditor(); }
    if (button.hasAttribute('data-quick-toggle')) { quickMenu(root.querySelector<HTMLElement>('.quick-menu')!.hidden === true); return; }
    if (button.dataset.quickPreset) { quickMenu(false); void quickCreateNow(button.dataset.quickPreset as Preset); }
    if (button.hasAttribute('data-clear')) { search = ''; filter = 'all'; inboundFilter = ''; renderList(); root.querySelector<HTMLInputElement>('#workspace-search')!.focus(); }
    if (button.dataset.op) operate(button.dataset.op, button.dataset.id!);
  });
  async function load(force = false): Promise<void> {
    if ((!force && loaded) || loading || submitting) return;
    if (force && loaded && !window.confirm(t('重新读取将丢弃当前入站或客户端弹窗中未保存的输入，并使用数据库最新列表。请先复制需要保留的内容。只读取，不保存、不应用核心。确认继续？', 'Reloading discards unsaved input in the inbound or client dialog and uses the latest list from the database. Copy anything you need first. It only reads; nothing is saved or applied. Continue?'))) return;
    const controls = [...modal.querySelectorAll<HTMLInputElement | HTMLButtonElement | HTMLSelectElement | HTMLTextAreaElement>('input,button,select,textarea')];
    const disabled = controls.map(control => control.disabled); controls.forEach(control => { control.disabled = true; });
    loading = true; loadError = ''; renderList();
    try {
      const document = await api<{ revision: number; data: Workspace | null }>('workspace');
      state = document.data ?? { inbounds: [], clients: [] }; revision = document.revision; loaded = true; reloadRequired = false;
      if (inboundFilter && !state.inbounds.some(item => item.id === inboundFilter)) inboundFilter = '';
      if (force && modal.open) modal.close();
    } catch (reason) {
      loadError = (reason as Error).message; if (force) reloadRequired = true;
      if (modal.open) { const error = modal.querySelector<HTMLElement>('.form-error')!; error.textContent = `${loadError} ${t('原输入仍保留，请重试读取；尚未替换列表。', 'Your input is kept; retry loading. The list was not replaced.')}`; error.hidden = false; error.focus(); }
    }
    finally {
      controls.forEach((control, index) => { control.disabled = disabled[index]; });
      if (reloadRequired && modal.open) modal.querySelector<HTMLButtonElement>('[type="submit"]')!.disabled = true;
      loading = false; renderList(); if (force && !modal.open) root.querySelector<HTMLElement>('[data-reload]')?.focus();
    }
  }
  async function refreshTraffic(): Promise<void> {
    if (trafficLoading || document.hidden || root.hidden) return;
    trafficLoading = true;
    try { traffic = await api<TrafficSnapshot>('runtime/traffic'); trafficError = ''; if (page === 'clients') { ipView = await api<IPLimitView>('runtime/iplimit').catch(() => ipView); subscriptionService = await api<{ data: { enabled: boolean } }>('panel/subscription').then(s => s.data.enabled, () => subscriptionService); } }
    catch (reason) { trafficError = (reason as Error).message; }
    finally {
      trafficLoading = false;
      const label = root.querySelector('#traffic-status'); if (label) label.textContent = trafficStatus(traffic, trafficError);
      // 周期更新不替换操作按钮，避免用户的键盘焦点丢失。
      if (!root.hidden && !root.contains(document.activeElement) && !document.querySelector('dialog[open]')) listRows();
      else root.querySelectorAll<HTMLElement>('[data-client-row]').forEach(row => {
        const item = state.clients.find(client => client.id === row.dataset.clientRow); if (!item) return;
        const bytes = usedBytes(traffic, item.id);
        row.querySelector('[data-client-traffic]')!.textContent = `${formatTrafficGB(bytes)} / ${item.quotaGB || t('不限', 'Unlimited')}`;
        const ipTarget = row.querySelector<HTMLElement>('[data-client-ip]');
        if (ipTarget && !ipTarget.contains(document.activeElement)) ipTarget.innerHTML = `${ipCell(item)}<small class="table-secondary">${subscriptionLabel(item)}</small>`;
        const status = clientStatus(viewClient(item), state.inbounds.find(inbound => inbound.id === item.inboundId));
        row.querySelector('[data-client-status]')!.innerHTML = badge(status === t('可用', 'Available') ? bytes === null ? t('可用 · 用量未知', 'Available · usage unknown') : t('可用', 'Available') : status, status === t('可用', 'Available'));
      });
    }
  }
  if (persistent) {
    window.setInterval(() => { void refreshTraffic(); }, 5000);
    window.setInterval(() => { if (!root.hidden && !document.hidden) updateApplicationStatus(); }, 1000);
  }
  return { render(next) { if (page !== next) { search = ''; filter = 'all'; if (next === 'inbounds') inboundFilter = ''; } page = next; renderList(); if (!loaded) void load(); if (persistent) void refreshTraffic(); }, setRuntime(status, at) { runtime = status; runtimeAt = at; updateApplicationStatus(); } };
}
