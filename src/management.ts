import { addClient, chineseDate, clientStatus, createQuick, defaultConnection, defaultSniffing, deleteRecords, editClient,
  editInbound, escapeHtml as esc, inboundName, presets, quickInboundName, randomName, seedWorkspace } from './workspace.ts';
import type { Client, ClientInput, Connection, Inbound, Preset, TrafficReset, Workspace } from './workspace.ts';
import { api, APIError, confirmSaved, jsonBody, uncertainSave } from './api.ts';
import { initCalendar } from './calendar.ts';
import Sortable from 'sortablejs';
import { mergeVisibleOrder, orderedRecords, type ListKind, type ListOrder } from './list-order.ts';
import { initConnections } from './connections.ts';
import { expiryLeft, formatBytes, inboundTrafficPeriod, onlineWindowMs, relativeTime, trafficPeriodHint, trafficPeriodLabel, trafficSpeedVisible, trafficStatus, usedBytes } from './traffic.ts';
import type { TrafficSnapshot } from './traffic.ts';
import { newRealityConnection, nextRealityTarget, realityServerName, realityTargets, readSniffing, sniffingLabels } from './inbound-options.ts';
import type { PanelTLSStatus } from './panel-settings.ts';
import { readProxyEndpoint } from './proxy-endpoint.ts';
import type { RuntimeStatus } from './runtime.ts';
import { workspaceStatusText } from './workspace-status.ts';
import { firewallStatusText, oldFirewallCleanupAllowed, type FirewallReport } from './firewall-status.ts';
import { confirmAction } from './confirm.ts';
import { regionLabel, type PublicInfo } from './server-plan-model.ts';
import './management.css';
import { lang, locale, t } from './i18n.ts';

const icon = (name: string) => `<i data-lucide="${name}" aria-hidden="true"></i>`;
// 一键创建菜单沿用OUI的四个预设名称。
const quickLabels: Record<Preset, string> = { hy2: 'Hysteria2', reality: 'VLESS Reality Vision', 'xhttp-tls': 'VLESS XHTTP TLS', 'xhttp-reality': 'VLESS XHTTP Reality' };
const input = (label: string, name: string, value: string | number, attrs = '', hint = '') => `<label class="form-field">${label}<input name="${name}" value="${esc(String(value))}" ${attrs}>${hint ? `<small class="form-hint">${hint}</small>` : ''}</label>`;
const check = (label: string, name: string, checked: boolean) => `<label class="check-field"><input type="checkbox" name="${name}"${checked ? ' checked' : ''}><span>${label}</span></label>`;
const badge = (text: string, ok: boolean) => `<span class="status-badge ${ok ? 'is-good' : 'is-muted'}"><b></b>${esc(text)}</span>`;
const trafficResetFields = (value: TrafficReset = 'never') => `<label class="form-field"><span id="traffic-reset-label">${t('流量统计重置周期', 'Traffic statistics reset period')}</span><select name="trafficReset" aria-labelledby="traffic-reset-label">${([['never', t('不重置', 'Never')], ['daily', t('每日', 'Daily')], ['weekly', t('每周', 'Weekly')], ['monthly', t('每月', 'Monthly')]]).map(([key, label]) => `<option value="${key}"${value === key ? ' selected' : ''}>${label}</option>`).join('')}</select><small class="form-hint">${t('仅作用于本节点及其客户端的展示统计。按服务器时间每日零点、每周一零点、每月1日零点重置，不影响累计限额。', 'Applies to display statistics for this node and its clients only. Resets at server midnight, Monday midnight or the first day of each month; lifetime quotas are unchanged.')}</small></label>`;
const readTrafficReset = (data: FormData): TrafficReset => {
  const value = String(data.get('trafficReset') ?? 'never');
  if (value !== 'never' && value !== 'daily' && value !== 'weekly' && value !== 'monthly') throw new Error(t('节点统计周期无效', 'Invalid node statistics period'));
  return value;
};

export function initManagement(root: HTMLElement, refreshIcons: () => void, navigate: (page: string) => void, showDetail: (title: string, text: string) => void, persistent = false, onSaved: () => void = () => {}): { render: (page: 'inbounds' | 'clients') => void; setRuntime: (status: RuntimeStatus | null, at: number) => void } {
  let state: Workspace = persistent ? { inbounds: [], clients: [] } : seedWorkspace();
  let revision = 0, loaded = !persistent, loading = false, loadError = '', submitting = false, pendingNotice = '', reloadRequired = false;
  let page: 'inbounds' | 'clients' = 'inbounds';
  const orders: Record<ListKind, ListOrder> = { inbounds: { revision: 0, data: [] }, clients: { revision: 0, data: [] } };
  const orderReady = { inbounds: !persistent, clients: !persistent };
  let orderSaving = false, sortable: Sortable | null = null;
  let search = '', filter = 'all', inboundFilter = '';
  let traffic: TrafficSnapshot | null = null, trafficError = '', trafficLoading = false;
  type IPBan = { ip: string; until: string; manual?: boolean; permanent?: boolean; port?: number; protocol?: string };
  type IPLimitView = { enforced: boolean; issue: string; revision: number; workspaceRevision: number; serverTime: string; clients: Record<string, { online: { ip: string; lastSeen: string }[]; banned: IPBan[]; exempt: IPBan[] }> };
  let ipView: IPLimitView | null = null;
  let ipDialogRefresh: (() => void) | null = null, ipDialogClock: (() => void) | null = null;
  let ipDialogGeneration = 0;
  let subscriptionService: boolean | null = null;
  // Telegram上线/离线通知按客户端单独开启；开关存在独立设置里，切换不保存入站、不重启 Xray。
  let notifyView: { clients: Set<string>; botEnabled: boolean } | null = null;
  // 多选删除：选中项只保留在当前页可见的行里；客户端实时网速由相邻两次累计差值算出。
  const selected = new Set<string>();
  let speeds: Record<string, { up: number; down: number }> = {};
  let runtime: RuntimeStatus | null = null, runtimeAt = 0;
  let firewall: FirewallReport | null = null, firewallError = '', firewallBusy = '';
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
      <footer><button type="button" class="secondary-button" data-close>${t('取消', 'Cancel')}</button>${persistent ? `<button type="button" class="secondary-button" data-reload ${reloadRequired ? '' : 'hidden'}>${t('重新读取…', 'Reload…')}</button>` : ''}<button class="primary-button" type="submit">${esc(submitLabel)}</button></footer></form>`;
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
          onSaved(); void refreshFirewall();
        }
        submitting = false; modal.close(); renderList(); announce(pendingNotice || t('演示数据已更改', 'Demo data changed'));
      }
      catch (reason) {
        if (writing && uncertainSave(reason)) {
          form.querySelector<HTMLButtonElement>('[type="submit"]')!.textContent = t('连接中断，正在确认是否已保存…', 'Connection lost; confirming the save…');
          const confirmed = await confirmSaved('workspace', sent, before + 1);
          if (confirmed) {
            revision = confirmed.revision; state = confirmed.data; submitting = false; onSaved(); modal.close(); renderList();
            void refreshFirewall();
            announce(t('已确认保存；连接曾中断，请核对核心与防火墙状态。', 'Save confirmed after a connection interruption; check core and firewall status.'));
            return;
          }
        }
        state = previous; submitting = false; pendingNotice = ''; if (writing && (!(reason instanceof APIError) || reason.status === 409 || reason.status >= 500)) reloadRequired = true;
        controls.forEach((control, index) => { control.disabled = disabled[index]; });
        form.querySelector<HTMLButtonElement>('[type="submit"]')!.disabled = reloadRequired;
        form.querySelector<HTMLButtonElement>('[type="submit"]')!.textContent = submitLabel;
        if (reloadRequired) modal.querySelector<HTMLElement>('[data-reload]')?.removeAttribute('hidden');
        error.textContent = (reason instanceof Error ? reason.message : t('未保存，请检查输入后重试。', 'Not saved; check the input and retry.')) + (reloadRequired ? t(' 上次保存失败或结果未确认，输入已保留。请先复制需要保留的修改，再重新读取草稿；不会自动合并或覆盖其它页面的数据。', ' The last save failed or was not confirmed; your input is kept. Copy any changes you need, then reload the draft. Data from other pages is never merged or overwritten automatically.') : ''); error.hidden = false; error.focus();
        updateApplicationStatus();
      }
    });
    refreshIcons(); modal.showModal(); updateApplicationStatus();
    return form;
  }
  modal.addEventListener('cancel', event => { if (submitting || loading) event.preventDefault(); });
  modal.addEventListener('close', () => {
    ipDialogGeneration++; ipDialogRefresh = null; ipDialogClock = null;
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
  const endpointFields = (inbound?: Inbound) => `<fieldset class="sniffing-field"><legend>${t('代理监听与分享地址', 'Proxy listen and share address')}</legend>${input(t('代理监听 IP', 'Proxy listen IP'), 'listen', inbound ? inbound.listen || '127.0.0.1' : '0.0.0.0', 'required maxlength="45" list="proxy-listens" autocomplete="off"', t('0.0.0.0 监听所有地址，客户端可从外网连接；127.0.0.1 仅本机可用。只影响代理入站，不影响管理面板。', '0.0.0.0 listens on all addresses so clients can connect from outside; 127.0.0.1 is local only. Affects proxy inbounds only, not the panel.'))}<datalist id="proxy-listens"><option value="127.0.0.1"></option><option value="::1"></option><option value="0.0.0.0"></option><option value="::"></option></datalist>${input(t('分享主机（域名或 IP）', 'Share host (domain or IP)'), 'shareHost', inbound?.shareHost || '', 'maxlength="253" autocomplete="off"', t('写入节点链接的服务器地址；新建时优先填入面板证书里的域名，没有证书才填本机公网 IP。域名须直接解析到本机（不能开 CDN 代理）。不含协议、端口或路径。', 'Server address written into node links; new inbounds get the domain from the panel certificate, or the public IP when there is no certificate. The domain must resolve directly to this server (no CDN proxy). No scheme, port or path.'))}<p class="form-hint">${t('端口范围 1024–65535；保存时自动放行受支持的主机防火墙，失败会显示原因；云安全组需另行放行。', 'Ports 1024–65535; saving automatically allows supported host firewalls and reports failures. Cloud security groups must be configured separately.')}</p></fieldset>`;
  const connectionFields = (preset: Preset, connection: Connection = preset === 'hy2' || preset === 'xhttp-tls' ? { ...defaultConnection(), usePanelCertificate: true } : newRealityConnection(), portHopping = '') => `<fieldset class="sniffing-field"><legend>${t('连接参数', 'Connection parameters')}</legend><p class="form-hint">${t('UUID／认证值由服务端首次预检生成并保存。REALITY 必须使用公私钥：私钥保留服务端，客户端公钥随连接信息自动提供，无需手填；刷新目标不会更换密钥。', 'The UUID/auth value is generated and saved at the first server preflight. REALITY uses a key pair: the private key stays on the server and the public key is included in the connection info automatically. Changing the target does not change the keys.')}</p>
    ${preset === 'hy2' || preset === 'xhttp-tls' ? check(t('自动复用已生效的面板 HTTPS 证书', 'Reuse the active panel HTTPS certificate'), 'usePanelCertificate', !!connection.usePanelCertificate) + t('<p class="form-hint" data-panel-cert-status role="status">正在读取面板证书状态…</p>', '<p class="form-hint" data-panel-cert-status role="status">Reading panel certificate status…</p>') + `<div data-manual-cert ${connection.usePanelCertificate ? 'hidden' : ''}>` + input(t('证书绝对路径', 'Certificate absolute path'), 'certificateFile', connection.certificateFile, 'maxlength="4096" autocomplete="off"', t('读取本机PEM证书，不上传到远端。', 'Reads a local PEM certificate; nothing is uploaded.')) + input(t('私钥绝对路径', 'Private key absolute path'), 'keyFile', connection.keyFile, 'maxlength="4096" autocomplete="off"') + '</div>' + input(t('TLS 服务域名（SNI）', 'TLS server name (SNI)'), 'serverName', connection.serverName, 'maxlength="253" autocomplete="off"', t('复用时自动填写证书首个域名，可手动修改但必须匹配证书。', 'When reusing, the first certificate domain is filled in; you can change it, but it must match the certificate.')) : refreshField(t('REALITY 目标', 'REALITY target'), 'target', connection.target, 'target', t('换一个预设目标并同步 SNI', 'Pick another preset target and sync SNI'), t('placeholder="域名:443" list="reality-targets"', 'placeholder="domain:443" list="reality-targets"')) + `<datalist id="reality-targets">${realityTargets.map(host => `<option value="${host}:443"></option>`).join('')}</datalist>` + refreshField(t('REALITY 服务域名（SNI）', 'REALITY server name (SNI)'), 'serverName', connection.serverName, 'sni', t('从目标地址填入 SNI', 'Fill SNI from the target')) + t('<p class="form-hint">AMD、Microsoft、Apple、Amazon、特斯拉仅为常用填写候选，可手动修改；未探测外站，不保证在每台服务器可用。</p><p class="form-hint" data-connection-notice role="status"></p>', '<p class="form-hint">AMD, Microsoft, Apple, Amazon and Tesla are only common suggestions and can be changed; external sites are not probed and may not work from every server.</p><p class="form-hint" data-connection-notice role="status"></p>')}
    ${preset === 'reality' ? check(t('XTLS Vision 流控', 'XTLS Vision flow'), 'vision', !connection.noVision) + t('<p class="form-hint">默认开启。只有旧客户端没配置流控（如从 OUI 导入的无流控节点）时才关闭；两端流控不一致会连不上。</p>', '<p class="form-hint">On by default. Turn it off only when existing clients have no flow configured (such as flow-less nodes imported from OUI); a flow mismatch between the two ends fails to connect.</p>') : ''}
    ${preset === 'reality' || preset === 'xhttp-reality' ? check(t('Mihomo 兼容模式（降低最低客户端版本）', 'Mihomo compatibility (lower minimum client version)'), 'mihomoCompatibility', !!connection.mihomoCompatibility) + t('<p class="form-hint" id="mihomo-compatibility-hint">默认关闭，保留官方客户端限制。开启后最低版本设为 1.8.2（从 OUI 导入的入站沿用原来更低的版本）；Xray 官方警告此举可能增加被识别、封锁的风险。只保存草稿，应用后才生效。</p>', '<p class="form-hint" id="mihomo-compatibility-hint">Off by default, keeping the official client limit. When on, the minimum version is 1.8.2 (inbounds imported from OUI keep their original lower version); Xray warns this may increase the risk of detection and blocking. Saved as a draft; takes effect after applying.</p>') : ''}
    ${preset.startsWith('xhttp-') ? input(t('XHTTP 路径', 'XHTTP path'), 'path', connection.path, 'maxlength="4096" autocomplete="off"') + `<label class="form-field">${t('XHTTP 模式', 'XHTTP mode')}<select name="mode">${['auto', 'packet-up', 'stream-up', 'stream-one'].map(mode => `<option ${connection.mode === mode ? 'selected' : ''}>${mode}</option>`).join('')}</select></label>` : ''}${preset === 'hy2' ? input(t('端口跳跃范围（可选）', 'Port hopping range (optional)'), 'portHopping', portHopping, 'maxlength="11" pattern="[0-9]{4,5}-[0-9]{4,5}" placeholder="48000-48199" autocomplete="off"', t('填写后服务器把这段 UDP 端口转发到监听端口，客户端在其中切换端口，减轻单个端口被限速或封锁的影响；订阅与分享链接自动带上。不能包含任何入站端口，也不能与其他入站的范围重叠。云服务器的安全组需另外放行这段 UDP 端口。', 'The server forwards this UDP port range to the listen port and clients switch ports within it, reducing the impact of throttling or blocking on a single port; subscriptions and share links include it automatically. It must not include any inbound port or overlap another inbound\'s range. Cloud security groups must allow this UDP range separately.')) : ''}</fieldset>`;
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
  const readHopping = (data: FormData) => String(data.get('portHopping') ?? '').trim() || undefined;
  const readConnection = (form: HTMLFormElement): Connection => {
    const data = new FormData(form), connection = defaultConnection();
    for (const key of ['certificateFile', 'keyFile', 'target', 'serverName', 'path', 'mode'] as const) { if (data.has(key)) connection[key] = String(data.get(key)).trim(); }
    connection.usePanelCertificate = data.has('usePanelCertificate');
    connection.mihomoCompatibility = data.has('mihomoCompatibility');
    if (form.elements.namedItem('vision')) connection.noVision = !data.has('vision');
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
        api<{ port: number; portHopping?: string }>(`system/free-port?transport=${proto}${preset === 'hy2' ? '&hop=1' : ''}`),
        api<PublicInfo>('system/public').catch(() => null),
        api<PanelTLSStatus>('panel/tls').catch(() => null),
      ]);
      const domain = certificateDomain(cert);
      if (tls && !domain) throw new Error(t('Hysteria2 和 XHTTP TLS 需要面板证书，请先在“面板设置”配置证书，或用“添加入站”手动填写证书路径。', 'Hysteria2 and XHTTP TLS need the panel certificate; configure it in Panel settings first, or use Add inbound to enter certificate paths.'));
      const shareHost = domain || info?.ipv4?.ip || info?.ipv6?.ip || '';
      if (!shareHost) throw new Error(t('面板没有证书域名，也未查询到服务器公网 IP，请用“添加入站”手动填写分享主机。', 'There is no certificate domain and the public IP is unknown; use Add inbound and enter the share host manually.'));
      const client = { name: randomName(), quotaGB: 0, ipLimit: 0, expiresAt: null, subscription: true };
      const name = quickInboundName(presets[preset].label, new Date(), state.inbounds.map(item => item.name));
      state = createQuick(state, { ...client, preset, port: free.port, sniffing: defaultSniffing() }, crypto.randomUUID());
      const inbound = state.inbounds.at(-1)!;
      Object.assign(inbound, { name, connection: tls ? { ...defaultConnection(), usePanelCertificate: true } : newRealityConnection(), listen: '0.0.0.0', shareHost, portHopping: free.portHopping });
      writing = true; sent = state; created = inbound.name;
      const document = await api<{ revision: number; data: Workspace; applied?: boolean; applyError?: string; firewall?: string }>('workspace', jsonBody('PUT', { revision, data: state }));
      revision = document.revision; state = document.data; submitting = false; search = ''; filter = 'all'; renderList(); onSaved(); void refreshFirewall();
      announce((document.applied ? `${inbound.name} ${t('已创建并生效 · 端口', 'created and applied · port')} ${free.port}/${proto}${free.portHopping ? `${t(' · 端口跳跃 ', ' · port hopping ')}${free.portHopping}` : ''}` : `${inbound.name} ${t('已创建，但未生效：', 'created, but not applied: ')}${document.applyError ?? ''}`) + (document.firewall ? `${t(' · 防火墙：', ' · firewall: ')}${document.firewall}` : ''));
    } catch (reason) {
      if (writing && uncertainSave(reason)) {
        notice.textContent = t('连接中断，正在确认是否已保存…', 'Connection lost; confirming the save…');
        const confirmed = await confirmSaved('workspace', sent, before + 1);
        if (confirmed) {
          revision = confirmed.revision; state = confirmed.data; submitting = false; search = ''; filter = 'all'; renderList(); onSaved();
          void refreshFirewall();
          announce(`${created} ${t('已确认创建；连接曾中断，请核对核心与防火墙状态。', 'creation confirmed after a connection interruption; check core and firewall status.')}`);
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
      ${persistent ? endpointFields() : ''}<div id="connection-fields">${persistent ? connectionFields(preset) : ''}</div>${trafficResetFields()}${clientFields(demoClient)}${sniffingFields()}`, persistent ? t('保存', 'Save') : t('创建演示入站', 'Create demo inbound'), form => {
      state = createQuick(state, { ...readClient(form, date.value()), preset, port: Number(new FormData(form).get('port')), sniffing: readSniffing(new FormData(form)) }, crypto.randomUUID());
      state.inbounds.at(-1)!.trafficReset = readTrafficReset(new FormData(form));
      if (persistent) { const inbound = state.inbounds.at(-1)!; inbound.name = `${region}-${presets[preset].label}-${state.clients.at(-1)!.name}`; inbound.connection = readConnection(form); Object.assign(inbound, readProxyEndpoint(new FormData(form)), { portHopping: readHopping(new FormData(form)) }); }
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
      ${trafficResetFields(inbound.trafficReset)}
      ${persistent ? endpointFields(inbound) + connectionFields(inbound.preset, inbound.connection, inbound.portHopping) : ''}
      ${check(t('启用入站', 'Enable inbound'), 'enabled', inbound.enabled)}
      ${sniffingFields(inbound.sniffing)}`, persistent ? t('保存', 'Save') : t('保存演示更改', 'Save demo changes'), form => {
      const data = new FormData(form);
      const sniffing = readSniffing(data);
      state = editInbound(state, inbound.id, { name: String(data.get('name')), port: Number(data.get('port')), enabled: data.has('enabled'), sniffing });
      state.inbounds.find(item => item.id === inbound.id)!.trafficReset = readTrafficReset(data);
      if (persistent) Object.assign(state.inbounds.find(item => item.id === inbound.id)!, { connection: readConnection(form), ...readProxyEndpoint(data), portHopping: readHopping(data) });
      announce(t('已保存演示入站 · 未应用 Xray', 'Demo inbound saved · Xray not applied'));
    });
    wireConnection(form);
  }
  // 同一入站端口的封禁在共享客户端间可见，操作前明确范围与时长。
  async function ipDialog(client: Client): Promise<void> {
    returnFocus = document.activeElement as HTMLElement;
    const generation = ++ipDialogGeneration;
    let busy = false, ready = false, selected = '', minutes = '10', message = '', failed = false, selectionView: IPLimitView | null = null;
    let clockOffset = 0, limitInput = String(client.ipLimit);
    const current = () => generation === ipDialogGeneration && modal.open;
    const time = (value: string) => new Date(value).toLocaleString(lang === 'en' ? 'en' : 'zh-CN', { hour12: false });
    const now = () => Date.now() + clockOffset;
    const remaining = (until: string) => {
      const seconds = Math.max(0, Math.ceil((Date.parse(until) - now()) / 1000));
      if (!seconds) return t('已到期，等待状态同步', 'Expired; waiting for status update');
      const days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600), mins = Math.floor(seconds % 3600 / 60), secs = seconds % 60;
      return t('剩余 ', 'Remaining ') + (days ? `${days}${t('天 ', 'd ')}` : '') + (hours ? `${hours}${t('小时 ', 'h ')}` : '') + `${mins}${t('分 ', 'm ')}${secs}${t('秒', 's')}`;
    };
    const durations = [[10, t('10 分钟', '10 minutes')], [30, t('30 分钟', '30 minutes')], [60, t('1 小时', '1 hour')], [1440, t('24 小时', '24 hours')], [10080, t('7 天', '7 days')], [0, t('永久，手动解封', 'Permanent, until manually unbanned')]] as const;
    const inbound = state.inbounds.find(item => item.id === client.inboundId);
    const endpoint = inbound ? `${inbound.port}/${inbound.preset === 'hy2' ? 'UDP' : 'TCP'}` : '';
    const scope = t(`作用范围：该 IP → 入站 ${endpoint}。同一入站的其他客户端从这个 IP 连接也会受影响。`, `Scope: this IP → inbound ${endpoint}. Other clients sharing this inbound are also affected when connecting from this IP.`);
    const clock = () => {
      if (!current()) return;
      modal.querySelectorAll<HTMLElement>('[data-ip-until]').forEach(item => { item.textContent = remaining(item.dataset.ipUntil!); });
    };
    const render = () => {
      const view = ipView?.clients[client.id] ?? { online: [], banned: [], exempt: [] };
      const banned = [...new Map(view.banned.map(item => [item.ip, item])).values()];
      const bannedIPs = new Set(banned.map(item => item.ip));
      const online = view.online.filter(item => !bannedIPs.has(item.ip));
      const list = (items: string[], empty: string) => items.length ? `<ul class="ip-list">${items.join('')}</ul>` : `<p class="form-hint ip-empty">${empty}</p>`;
      const row = (ip: string, tone: string, status: string, detail: string) => `<li class="ip-card ip-${tone}${selected === ip ? ' is-selected' : ''}"><div class="ip-card-heading"><button type="button" class="ip-address" data-ip="${esc(ip)}" aria-expanded="${selected === ip}" aria-controls="ip-action-panel" ${busy || !ready ? 'disabled' : ''}><code>${esc(ip)}</code><span class="ip-manage-label">${t('管理', 'Manage')}</span></button><span class="ip-badge">${status}</span></div><div class="ip-detail">${detail}</div></li>`;
      const deadline = (item: IPBan, label: string) => item.permanent ? `<strong>${t('永久封禁 · 需手动解封', 'Permanent ban · manual unban required')}</strong>` : `<span data-ip-until="${esc(item.until)}">${remaining(item.until)}</span><span>${label}${time(item.until)}</span>`;
      const selectedBan = banned.find(item => item.ip === selected);
      const changedWorkspace = reloadRequired || ready && ipView?.workspaceRevision !== revision;
      const disabled = busy || !ready || !ipView?.enforced || changedWorkspace;
      modal.innerHTML = `<form novalidate data-ip-dialog><header class="dialog-heading"><h2 id="edit-title">${t('在线 IP', 'Online IPs')} · ${esc(client.name)}</h2><button type="button" class="icon-button" data-close aria-label="${t('关闭弹窗', 'Close dialog')}">${icon('x')}</button></header><div class="edit-body ip-dialog-body">
        <p class="form-intro">${client.ipLimit ? t(`IP 上限 ${client.ipLimit}：超限自动封禁 10 分钟；手动封禁按你选择的时长执行。`, `IP limit ${client.ipLimit}: excess IPs are automatically banned for 10 minutes; manual bans use your chosen duration.`) : t('未设置 IP 上限，可点击下方 IP 手动封禁。', 'No IP limit is set. Click an IP below to ban it manually.')}</p>
        <div class="ip-limit-settings"><label class="form-field" for="ip-limit-input">${t('IP 上限', 'IP limit')}<input id="ip-limit-input" data-ip-limit name="ipLimit" type="number" min="0" max="10000" step="1" required value="${esc(limitInput)}" ${busy || changedWorkspace ? 'disabled' : ''}></label><button type="button" class="primary-button" data-ip-save ${busy || !ready || changedWorkspace ? 'disabled' : ''}>${t('保存上限', 'Save limit')}</button></div>
        <p class="form-hint">${t('0 表示不限，最多 10000；仅修改当前客户端。保存后立即生效，代理连接可能短暂中断。', '0 means unlimited, up to 10000; changes this client only. Saving applies immediately and may briefly interrupt proxy connections.')}</p>
        <p class="form-hint">${t('点击 IP 管理；到期后通常在 10 秒内自动解封。', 'Click an IP to manage it. Expired bans are normally removed within 10 seconds.')}</p>
        ${changedWorkspace ? `<p class="form-error">${t('客户端配置已变化，请关闭弹窗并重新读取列表后操作。', 'Client settings changed; close this dialog and reload the list before acting.')}</p>` : ''}
        ${ipView?.issue ? `<p class="form-error">${esc(ipView.issue)}</p>` : ipView && !ipView.enforced ? `<p class="form-error">${t('防火墙助手不可用，请更新安装后重试。', 'The firewall helper is unavailable; update the installation and retry.')}</p>` : ''}
        <h3>${t('在线', 'Online')} <small>${online.length}</small></h3>${list(online.map(item => row(item.ip, 'online', t('在线', 'Online'), `<span>${t('最近新连接 ', 'Last new connection ')}${time(item.lastSeen)}</span>`)), t('当前没有未封禁的在线 IP。', 'No unblocked IP is online.'))}
        <h3>${t('已封禁', 'Banned')} <small>${banned.length}</small></h3>${list(banned.map(item => row(item.ip, item.permanent || Date.parse(item.until) > now() ? 'banned' : 'pending', item.permanent || Date.parse(item.until) > now() ? t('已封禁', 'Banned') : t('待解封', 'Unban pending'), `<span>${item.manual ? t('手动封禁', 'Manual ban') : t('超限自动封禁', 'Automatic limit ban')} · ${item.port ?? inbound?.port}/${(item.protocol ?? 'tcp').toUpperCase()}</span>${deadline(item, t('解封时间 ', 'Unban at '))}`)), t('没有被封禁的 IP。', 'No IP is banned.'))}
        ${selected ? `<section id="ip-action-panel" class="ip-action-panel" aria-label="${t('IP 操作', 'IP actions')}"><div class="ip-action-heading"><strong><code>${esc(selected)}</code></strong><button type="button" class="text-button" data-ip-cancel ${busy ? 'disabled' : ''}>${t('取消选择', 'Cancel selection')}</button></div><p class="form-hint">${esc(scope)}</p><label class="form-field">${t('封禁时长（从确认时开始）', 'Ban duration (from confirmation)')}<select data-ip-duration ${disabled ? 'disabled' : ''}>${durations.map(([value, label]) => `<option value="${value}" ${String(value) === minutes ? 'selected' : ''}>${label}</option>`).join('')}</select></label><div class="ip-action-buttons"><button type="button" class="danger-button" data-ip-ban ${disabled ? 'disabled' : ''}>${selectedBan ? t('更新封禁时长', 'Update ban duration') : t('确认封禁', 'Confirm ban')}</button>${selectedBan ? `<button type="button" class="primary-button" data-ip-unban ${disabled ? 'disabled' : ''}>${t('确认解封', 'Confirm unban')}</button>` : ''}</div>${selectedBan ? `<p class="form-hint">${t('手动解封会取消封禁，恢复正常状态；仍超过 IP 上限时会再次自动封禁。', 'Manual unban removes the ban and restores normal status; exceeding the IP limit may trigger another automatic ban.')}</p>` : ''}</section>` : ''}
        <p class="${failed ? 'form-error' : 'form-hint'}" role="status">${esc(message)}</p></div><footer><button type="button" class="secondary-button" data-ip-refresh ${busy ? 'disabled' : ''}>${t('刷新状态', 'Refresh status')}</button><button type="button" class="secondary-button" data-close>${t('关闭', 'Close')}</button></footer></form>`;
      modal.querySelector('form')!.addEventListener('submit', event => { event.preventDefault(); void saveLimit(); });
      modal.querySelector<HTMLInputElement>('[data-ip-limit]')!.addEventListener('input', event => { limitInput = (event.target as HTMLInputElement).value; });
      modal.querySelector('[data-ip-save]')!.addEventListener('click', () => { void saveLimit(); });
      modal.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => { if (!submitting) modal.close(); }));
      modal.querySelectorAll<HTMLButtonElement>('[data-ip]').forEach(button => button.addEventListener('click', () => {
        if (busy || !ready) return;
        selected = button.dataset.ip!; selectionView = ipView; failed = false; message = ''; render();
        modal.querySelector<HTMLSelectElement>('[data-ip-duration]')?.focus();
      }));
      modal.querySelector('[data-ip-cancel]')?.addEventListener('click', () => { selected = ''; render(); });
      modal.querySelector<HTMLSelectElement>('[data-ip-duration]')?.addEventListener('change', event => { minutes = (event.target as HTMLSelectElement).value; });
      modal.querySelector('[data-ip-refresh]')!.addEventListener('click', () => { selected = ''; void refresh(); });
      modal.querySelector('[data-ip-ban]')?.addEventListener('click', () => { void change('ban'); });
      modal.querySelector('[data-ip-unban]')?.addEventListener('click', () => { void change('unban'); });
      refreshIcons();
    };
    async function saveLimit(): Promise<void> {
      if (busy || !ready || reloadRequired || loading || submitting || ipView?.workspaceRevision !== revision) return;
      const value = Number(limitInput);
      if (!limitInput.trim() || !Number.isInteger(value) || value < 0 || value > 10000) {
        failed = true; message = t('IP 上限须为 0～10000 的整数，0 表示不限。', 'IP limit must be an integer from 0 to 10000; 0 means unlimited.'); render(); modal.querySelector<HTMLInputElement>('[data-ip-limit]')?.focus(); return;
      }
      const sent = structuredClone(state), target = sent.clients.find(item => item.id === client.id);
      if (!target) return;
      target.ipLimit = value;
      const before = revision;
      busy = true; submitting = true; failed = false; message = t('正在保存并生效…', 'Saving and applying…'); render(); updateApplicationStatus();
      let saved = false;
      try {
        let result;
        try {
          result = await api<{ revision: number; data: Workspace; applied?: boolean; applyError?: string; firewall?: string }>('workspace', jsonBody('PUT', { revision: before, data: sent }));
        } catch (reason) {
          if (!uncertainSave(reason)) throw reason;
          const confirmed = await confirmSaved('workspace', sent, before + 1);
          if (!confirmed) throw reason;
          result = { ...confirmed, applyError: t('连接曾中断，请核对核心与防火墙状态。', 'Connection was interrupted; check core and firewall status.') };
        }
        revision = result.revision; state = result.data; client = state.clients.find(item => item.id === client.id)!;
        limitInput = String(client.ipLimit); selected = ''; selectionView = null; saved = true;
        message = result.applied ? t('IP 上限已保存并生效。', 'IP limit saved and applied.') : t('IP 上限已保存，请核对生效状态：', 'IP limit saved; check application status: ') + (result.applyError ?? '');
        if (result.firewall) message += ` ${result.firewall}`;
        onSaved(); void refreshFirewall();
      } catch (reason) {
        if (!(reason instanceof APIError) || reason.status === 409 || reason.status >= 500) reloadRequired = true;
        failed = true; message = (reason as Error).message + (reloadRequired ? t(' 输入已保留。请关闭弹窗，重新读取列表后再操作。', ' Input is kept. Close this dialog and reload the list before trying again.') : '');
      } finally {
        submitting = false; busy = false; pendingNotice = ''; renderList(); updateApplicationStatus();
      }
      if (saved) {
        try { ipView = await api<IPLimitView>('runtime/iplimit'); }
        catch { ready = false; message += t(' IP 状态读取失败，请刷新状态。', ' IP status could not be read; refresh the status.'); }
      }
      if (current()) render();
    }
    async function refresh(): Promise<void> {
      if (busy) return;
      busy = true; ready = false; failed = false; message = t('正在读取…', 'Loading…'); render();
      try {
        const next = await api<IPLimitView>('runtime/iplimit');
        if (!current()) return;
        ipView = next; clockOffset = Date.parse(next.serverTime) - Date.now(); if (!Number.isFinite(clockOffset)) clockOffset = 0;
        ready = true; message = '';
      } catch (reason) { if (!current()) return; failed = true; message = (reason as Error).message; }
      finally { if (current()) { busy = false; render(); } }
    }
    async function change(action: 'ban' | 'unban'): Promise<void> {
      if (busy || !ready || !selected || !selectionView || !ipView?.enforced || reloadRequired || ipView.workspaceRevision !== revision) return;
      const request = { clientId: client.id, ip: selected, minutes: Number(minutes), confirm: true, revision: selectionView.revision, workspaceRevision: revision };
      busy = true; failed = false; message = t('正在执行…', 'Applying…'); render();
      try {
        const result = await api<IPLimitView>(`runtime/iplimit/${action}`, jsonBody('POST', request));
        if (!current()) return;
        ipView = result; selected = ''; message = action === 'ban' ? t('封禁已生效。', 'Ban applied.') : t('已取消封禁，恢复正常状态。', 'Ban removed; normal status restored.');
      } catch (reason) { if (!current()) return; ready = false; failed = true; message = (reason as Error).message + t(' 请刷新状态后再操作。', ' Refresh the status before trying again.'); }
      finally { if (current()) { busy = false; render(); } }
    }
    render();
    if (!modal.open) modal.showModal();
    ipDialogRefresh = () => {
      if (!current() || busy || selected || !ready || document.activeElement?.hasAttribute('data-ip-limit')) return;
      const active = document.activeElement as HTMLElement | null, focused = !!active && modal.contains(active);
      const focusedIP = active?.dataset.ip, closing = active?.hasAttribute('data-close');
      const scroll = modal.querySelector('.edit-body')!.scrollTop;
      render(); modal.querySelector('.edit-body')!.scrollTop = scroll;
      if (focused) {
        const target = focusedIP ? [...modal.querySelectorAll<HTMLButtonElement>('[data-ip]')].find(item => item.dataset.ip === focusedIP) : null;
        (target ?? modal.querySelector<HTMLButtonElement>(closing ? '[data-close]' : '[data-ip-refresh]'))?.focus({ preventScroll: true });
      }
    };
    ipDialogClock = clock;
    await refresh();
  }
  function confirm(title: string, text: string, label: string, apply: () => void): void {
    openModal(persistent ? title.replaceAll(t('演示', 'demo '), '') : title, `<p class="confirm-copy">${esc(persistent ? text.replaceAll(t('演示', 'demo '), '') : text)}</p><p class="form-hint">${persistent ? t('确认后立即保存并生效。', 'Saved and applied right after you confirm.') : t('仅影响当前内存中的演示数据，刷新恢复。不会操作 VPS。', 'Affects only in-memory demo data; refreshing restores it.')}</p>`, label, () => { apply(); announce(t('演示数据已更改 · 未应用 Xray', 'Demo data changed · Xray not applied')); });
  }
  function confirmDeleteClients(ids: string[], title: string): void {
    const names = state.clients.filter(item => ids.includes(item.id)).map(item => item.name);
    if (!names.length) return;
    const next = deleteRecords(state, [], ids), retained = new Set(next.inbounds.map(item => item.id));
    const removed = state.inbounds.filter(item => !retained.has(item.id)).map(item => item.name);
    const consequence = removed.length
      ? `${t(' 将同步删除不再有客户端的 ', ' Also deletes ')}${removed.length}${t(' 个入站：', ' inbounds with no remaining clients: ')}${removed.join(t('、', ', '))}${t('。', '.')}`
      : t(' 所属入站仍有其他客户端，入站保留。', ' The linked inbounds still have other clients and are kept.');
    confirm(title, `${t('将删除 ', 'Deletes ')}${names.length}${t(' 个客户端：', ' clients: ')}${names.join(t('、', ', '))}${t('。其连接与订阅随之失效。', '. Their connections and subscriptions stop working.')}${consequence}`, t('确认删除', 'Confirm delete'), () => {
      state = deleteRecords(state, [], ids);
      for (const id of ids) selected.delete(id);
    });
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
      const linked = state.clients.filter(item => item.inboundId === id).length;
      confirm(t('删除一个演示入站', 'Delete a demo inbound'), `${t('删除“', 'Deletes “')}${inbound.name}${t('”', '”')}${linked ? `${t('及其 ', ' and its ')}${linked}${t(' 个客户端，客户端的连接与订阅随之失效。', ' clients; their connections and subscriptions stop working.')}` : t('。', '.')}`, t('确认删除', 'Confirm delete'), () => { state = deleteRecords(state, [id]); });
    }
    if (action === 'delete-client' && client) confirmDeleteClients([id], t('删除一个演示客户端', 'Delete a demo client'));
  }
  // 面板设置里关闭了订阅服务时，所有订阅链接都不返回内容；此时不再按客户端各自的选项显示“订阅开启”。
  const subscriptionLabel = (item: Client) => persistent && subscriptionService === false ? t('订阅已停用（全局）', 'Subscriptions off (global)') : item.subscription ? t('订阅开启', 'Subscription on') : t('订阅关闭', 'Subscription off');
  // 在线IP数/上限与封禁数（真实模式点开看明细与解封）；演示数据只显示上限。
  const notifyCell = (item: Client) => {
    const hint = notifyView && !notifyView.botEnabled ? t('Telegram 机器人未启用：开启后也暂不发送', 'The Telegram bot is off: nothing is sent until it is enabled') : t('通过 Telegram 通知上线（本次累计 1 MB 流量）和离线（10 分钟无流量）', 'Telegram alerts when the client comes online (1 MB this session) or goes offline (10 minutes without traffic)');
    return `<label class="switch" title="${hint}"><input type="checkbox" data-toggle-notify="${esc(item.id)}" ${notifyView?.clients.has(item.id) ? 'checked' : ''} ${notifyView ? '' : 'disabled'} aria-label="${t('TG 上线通知', 'TG online alerts')} ${esc(item.name)}"><span aria-hidden="true"></span></label>`;
  };
  async function toggleNotify(input: HTMLInputElement): Promise<void> {
    const id = input.dataset.toggleNotify!, enabled = input.checked, name = state.clients.find(item => item.id === id)?.name ?? '';
    input.disabled = true;
    try {
      const view = await api<{ clients: string[]; botEnabled: boolean }>('panel/telegram/notify', jsonBody('PUT', { clientId: id, enabled }));
      notifyView = { clients: new Set(view.clients), botEnabled: view.botEnabled };
      announce(`${enabled ? t('已开启 ', 'TG alerts on for ') : t('已关闭 ', 'TG alerts off for ')}${name}${t(' 的 TG 上线通知', '')}${enabled && !view.botEnabled ? t('（Telegram 机器人未启用，暂不发送）', ' (the Telegram bot is off, so nothing is sent yet)') : ''}`);
    } catch (reason) { input.checked = !enabled; announce((reason as Error).message); }
    finally { input.disabled = false; }
  }
  const ipCell = (item: Client) => {
    if (!persistent) return item.ipLimit ? `${item.ipLimit} ${t('个 IP', 'IPs')}` : t('IP 不限', 'Unlimited IPs');
    const view = ipView?.clients[item.id], online = view?.online.length ?? 0, banned = view?.banned.length ?? 0;
    return `<button class="text-button" data-op="iplimit" data-id="${esc(item.id)}">${online} / ${item.ipLimit || '∞'} ${t('在线 IP', 'online IPs')}</button>${banned ? `<small class="table-secondary danger-text">${t('已封禁 ', 'Banned ')}${banned}</small>` : ''}`;
  };
  const rowActions = (item: Client | Inbound, type: 'inbound' | 'client') => `<div class="row-actions">${type === 'client' && persistent ? `<button class="icon-button" data-op="connection" data-id="${esc(item.id)}" aria-label="${t('连接信息', 'Connection info')} ${esc(item.name)}" title="${t('连接信息', 'Connection info')}" ${loading || reloadRequired ? 'disabled' : ''}>${icon('qr-code')}</button>` : ''}<button class="icon-button" data-op="edit-${type}" data-id="${esc(item.id)}" aria-label="${type === 'inbound' ? t('编辑入站', 'Edit inbound') : t('编辑客户端', 'Edit client')} ${esc(item.name)}" title="${t('编辑', 'Edit')}" ${loading || reloadRequired ? 'disabled' : ''}>${icon('pencil')}</button>${type === 'inbound' ? `<button class="icon-button" data-op="toggle-${type}" data-id="${esc(item.id)}" aria-label="${item.enabled ? t('停用', 'Disable') : t('启用', 'Enable')} ${esc(item.name)}" title="${item.enabled ? t('停用', 'Disable') : t('启用', 'Enable')}" ${loading || reloadRequired ? 'disabled' : ''}>${icon('power')}</button>` : ''}<button class="icon-button danger-icon" data-op="delete-${type}" data-id="${esc(item.id)}" aria-label="${type === 'inbound' ? t('删除入站', 'Delete inbound') : t('删除客户端', 'Delete client')} ${esc(item.name)}" title="${t('删除', 'Delete')}" ${loading || reloadRequired ? 'disabled' : ''}>${icon('trash-2')}</button></div>`;
  // OUI的在线判定：启用且最近2分钟内有流量；核心未采集时一律离线。
  const isOnline = (item: Client) => {
    const at = traffic?.lastOnline?.[item.id];
    return clientStatus(viewClient(item), state.inbounds.find(inbound => inbound.id === item.inboundId)) === t('可用', 'Available') && persistent && !!at && !!traffic?.collecting && !trafficError && !!traffic.sampledAt && Date.now() - Date.parse(traffic.sampledAt) <= 10_000 && Date.now() - Date.parse(at) <= onlineWindowMs;
  };
  const trafficCell = (item: Client) => {
    const trafficPeriod = inboundTrafficPeriod(state.inbounds.find(inbound => inbound.id === item.inboundId));
    const bytes = persistent ? usedBytes(traffic, item.id, trafficPeriod) : BigInt(Math.round(item.usedGB * 1e9));
    const percent = item.quotaGB > 0 ? Math.min(100, viewClient(item).usedGB / item.quotaGB * 100) : 0;
    const rate = persistent && traffic?.collecting && !trafficError && traffic.sampledAt && Date.now() - Date.parse(traffic.sampledAt) <= 10_000 ? speeds[item.id] : undefined;
    const speed = trafficSpeedVisible(isOnline(item), rate) ? `<small class="table-secondary speed-pair"><span class="speed-up" title="${t('上传', 'Upload')}">↑ ${formatBytes(rate!.up)}/s</span><span class="speed-down" title="${t('下载', 'Download')}">↓ ${formatBytes(rate!.down)}/s</span></small>` : '';
    const quota = `${formatBytes(persistent ? usedBytes(traffic, item.id) : bytes)} / ${item.quotaGB ? formatBytes(item.quotaGB * 1e9) : t('不限', 'Unlimited')}`;
    return `<span title="${esc(trafficPeriodHint(traffic, trafficPeriod))}">${trafficPeriod === 'total' ? quota : `${formatBytes(bytes)} · ${trafficPeriodLabel(trafficPeriod)}`}</span>${trafficPeriod !== 'total' ? `<small class="table-secondary">${t('累计', 'Lifetime')} ${quota}</small>` : ''}${item.quotaGB && bytes !== null ? `<div class="mini-meter" aria-hidden="true"><span style="width:${percent}%"></span></div>` : ''}${speed}`;
  };
  const lastOnlineCell = (item: Client) => {
    const at = traffic?.lastOnline?.[item.id];
    if (isOnline(item)) return `<span class="status-badge is-online"><b></b>${t('在线', 'Online')}</span>`;
    return at ? `<span class="date-value" title="${esc(new Date(at).toLocaleString(locale))}">${t(`${relativeTime(Date.parse(at))}上线过`, `Last online ${relativeTime(Date.parse(at))}`)}</span>` : `<span class="date-value muted-text">${persistent ? t('从未上线', 'Never online') : '—'}</span>`;
  };
  const inboundTraffic = (id: string) => {
    const trafficPeriod = inboundTrafficPeriod(state.inbounds.find(inbound => inbound.id === id));
    let total = 0n;
    for (const client of state.clients.filter(item => item.inboundId === id)) {
      const bytes = persistent ? usedBytes(traffic, client.id, trafficPeriod) : BigInt(Math.round(client.usedGB * 1e9));
      if (bytes === null) return t('未采集', 'Not collected');
      total += bytes;
    }
    return `<span title="${esc(trafficPeriodHint(traffic, trafficPeriod))}">${formatBytes(total)}</span><small class="table-secondary">${trafficPeriodLabel(trafficPeriod)}</small>`;
  };
  const selectCell = (item: Client | Inbound) => `<td class="select-cell"><div class="record-controls"><button type="button" class="icon-button row-drag-handle" data-reorder="${esc(item.id)}" title="${t('拖动排序；聚焦后按上下方向键调整', 'Drag to reorder; use arrow keys when focused')}" aria-label="${t('调整顺序', 'Reorder')} ${esc(item.name)}" ${!orderReady[page] || orderSaving || loading || submitting || reloadRequired ? 'disabled' : ''}>${icon('grip-vertical')}</button><input type="checkbox" data-select="${esc(item.id)}" ${selected.has(item.id) ? 'checked' : ''} aria-label="${t('选择', 'Select')} ${esc(item.name)}"></div></td>`;
  function updateSelection(): void {
    const boxes = [...root.querySelectorAll<HTMLInputElement>('[data-select]')], all = root.querySelector<HTMLInputElement>('[data-select-all]');
    if (all) { all.checked = boxes.length > 0 && boxes.every(box => box.checked); all.indeterminate = !all.checked && boxes.some(box => box.checked); }
    const button = root.querySelector<HTMLButtonElement>('[data-delete-selected]');
    if (button) { button.hidden = selected.size === 0; button.querySelector('span')!.textContent = `${t('删除所选', 'Delete selected')} (${selected.size})`; }
  }
  function deleteSelected(): void {
    const ids = [...selected];
    if (page === 'inbounds') {
      const names = state.inbounds.filter(item => selected.has(item.id)).map(item => item.name), linked = state.clients.filter(item => selected.has(item.inboundId)).length;
      confirm(t('删除所选入站', 'Delete selected inbounds'), `${t('将删除 ', 'Deletes ')}${names.length}${t(' 个入站', ' inbounds')}${linked ? `${t('及其 ', ' and their ')}${linked}${t(' 个客户端', ' clients')}` : ''}${t('：', ': ')}${names.join(t('、', ', '))}${t('。', '.')}`, t('确认删除', 'Confirm delete'), () => { state = deleteRecords(state, ids); selected.clear(); });
    } else {
      confirmDeleteClients(ids, t('删除所选客户端', 'Delete selected clients'));
    }
  }
  function listRows(): void {
    sortable?.destroy(); sortable = null;
    const query = search.trim().toLocaleLowerCase();
    const contains = (text: string) => text.toLocaleLowerCase().includes(query);
    const inboundRows = orderedRecords(state.inbounds, orders.inbounds.data).filter(item => contains(`${item.name} ${presets[item.preset].label} ${item.port}`) && (filter === 'all' || item.enabled === (filter === 'enabled')));
    const now = Date.now();
    const clientRows = orderedRecords(state.clients, orders.clients.data).filter(item => {
      const inbound = state.inbounds.find(record => record.id === item.inboundId);
      return contains(`${item.name} ${inbound?.name ?? ''}`) && (!inboundFilter || item.inboundId === inboundFilter) && (filter === 'all' || (filter === 'available' ? clientStatus(viewClient(item), inbound, now) === t('可用', 'Available') : clientStatus(viewClient(item), inbound, now) !== t('可用', 'Available')));
    });
    const visible = new Set((page === 'inbounds' ? inboundRows : clientRows).map(item => item.id));
    for (const id of [...selected]) if (!visible.has(id)) selected.delete(id);
    const count = visible.size;
    root.querySelector('#result-count')!.textContent = `${t('显示', 'Showing')} ${count} / ${page === 'inbounds' ? state.inbounds.length : state.clients.length} ${t('条', '')}`;
    const headers = page === 'inbounds' ? [t('入站名称', 'Inbound name'), t('类型 / 端口', 'Type / port'), t('状态', 'Status'), t('客户端', 'Clients'), t('总流量', 'Total traffic'), t('操作', 'Actions')]
      : [t('客户端 / 入站', 'Client / inbound'), t('流量', 'Traffic'), t('到期', 'Expiry'), t('最近上线', 'Last online'), t('IP / 订阅', 'IP / subscription'), ...(persistent ? [t('TG 上线通知', 'TG online alerts')] : []), t('操作', 'Actions')];
    root.querySelector('#list-content')!.innerHTML = count ? `<table class="data-table ${page}-table${page === 'clients' && persistent ? ' with-notify' : ''}"><caption class="sr-only">${page === 'inbounds' ? persistent ? t('入站列表', 'Inbounds') : t('入站演示列表', 'Demo inbounds') : persistent ? t('客户端列表', 'Clients') : t('客户端演示列表', 'Demo clients')}</caption><thead><tr><th scope="col" class="select-cell"><input type="checkbox" data-select-all aria-label="${t('全选', 'Select all')}"></th>${headers.map(label => `<th scope="col">${label}</th>`).join('')}</tr></thead><tbody>${page === 'inbounds' ? inboundRows.map(item => `<tr>${selectCell(item)}
      <td data-label="${t('入站名称', 'Inbound name')}"><strong class="record-name">${esc(item.name)}</strong><small class="table-secondary">${presets[item.preset].protocol} · ${presets[item.preset].security}</small></td>
      <td data-label="${t('类型 / 端口', 'Type / port')}"><span class="tag preset-${item.preset}">${presets[item.preset].label}</span><small class="table-secondary">${presets[item.preset].transport} / ${item.port}${item.portHopping ? ` · ${t('跳跃', 'hop')} ${item.portHopping}` : ''}</small></td>
      <td data-label="${t('状态', 'Status')}">${badge(item.enabled ? t('已启用', 'Enabled') : t('已停用', 'Disabled'), item.enabled)}${persistent ? `<div data-inbound-firewall="${esc(item.id)}">${firewallCell(item)}</div>` : ''}</td>
      <td data-label="${t('客户端', 'Clients')}"><button class="text-button" data-op="clients" data-id="${esc(item.id)}">${state.clients.filter(client => client.inboundId === item.id).length} ${t('个客户端', 'clients')}</button></td>
      <td data-label="${t('总流量', 'Total traffic')}" data-inbound-traffic="${esc(item.id)}">${inboundTraffic(item.id)}</td><td data-label="${t('操作', 'Actions')}">${rowActions(item, 'inbound')}</td></tr>`).join('') : clientRows.map(item => {
        const inbound = state.inbounds.find(record => record.id === item.inboundId)!;
        return `<tr data-client-row="${esc(item.id)}">${selectCell(item)}<td data-label="${t('客户端 / 入站', 'Client / inbound')}"><div class="client-identity"><label class="switch" title="${item.enabled ? t('停用', 'Disable') : t('启用', 'Enable')}"><input type="checkbox" data-toggle-client="${esc(item.id)}" ${item.enabled ? 'checked' : ''} ${loading || submitting || reloadRequired ? 'disabled' : ''} aria-label="${t('启用客户端', 'Enable client')} ${esc(item.name)}"><span aria-hidden="true"></span></label><div class="client-identity-text"><strong class="record-name">${esc(item.name)}</strong><small class="table-secondary">${esc(inbound.name)}</small></div></div></td>
          <td data-label="${t('流量', 'Traffic')}" data-client-traffic>${trafficCell(item)}</td>
          <td data-label="${t('到期', 'Expiry')}">${item.expiresAt === null ? `<span class="date-value">${t('永不过期', 'Never expires')}</span>` : `<span class="date-value">${expiryLeft(item.expiresAt, now)}</span><small class="table-secondary">${chineseDate(item.expiresAt)}</small>`}</td>
          <td data-label="${t('最近上线', 'Last online')}" data-client-last>${lastOnlineCell(item)}</td>
          <td data-label="${t('IP / 订阅', 'IP / subscription')}" data-client-ip>${ipCell(item)}<small class="table-secondary">${subscriptionLabel(item)}</small></td>${persistent ? `<td data-label="${t('TG 上线通知', 'TG online alerts')}" data-client-notify>${notifyCell(item)}</td>` : ''}<td data-label="${t('操作', 'Actions')}">${rowActions(item, 'client')}</td></tr>`;
      }).join('')}</tbody></table>` : (page === 'inbounds' ? state.inbounds : state.clients).length === 0 ? `<div class="empty-state">${icon(page === 'inbounds' ? 'log-in' : 'users')}<h2>${page === 'inbounds' ? t('还没有入站', 'No inbounds yet') : t('还没有客户端', 'No clients yet')}</h2><p>${page === 'inbounds' ? t('用右上角的“添加入站”或“一键创建”开始。', 'Start with Add inbound or Quick create at the top right.') : t('先创建入站，再为它添加客户端。', 'Create an inbound first, then add clients to it.')}</p></div>`
      : `<div class="empty-state">${icon('search')}<h2>${page === 'inbounds' ? t('没有匹配的入站', 'No matching inbounds') : t('没有匹配的客户端', 'No matching clients')}</h2><p>${t('试试其他关键词或清除筛选。', 'Try other keywords or clear the filters.')}</p><button class="secondary-button" data-clear>${t('清除搜索与筛选', 'Clear search and filters')}</button></div>`;
    updateSelection();
    refreshIcons();
    const body = root.querySelector<HTMLTableSectionElement>('.data-table tbody');
    body?.querySelectorAll<HTMLTableRowElement>('tr').forEach(row => { row.dataset.orderId = row.querySelector<HTMLElement>('[data-reorder]')!.dataset.reorder; });
    if (body) sortable = Sortable.create(body, {
      handle: '.row-drag-handle', draggable: 'tr', dataIdAttr: 'data-order-id',
      disabled: !orderReady[page] || orderSaving || loading || submitting || reloadRequired,
      animation: matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 140,
      ghostClass: 'row-sort-ghost', chosenClass: 'row-sort-chosen',
      onEnd(event) {
        if (event.oldIndex === event.newIndex) return;
        const ids = [...body.rows].map(row => row.dataset.orderId!);
        const kind = page, id = event.item.dataset.orderId!;
        queueMicrotask(() => { void saveOrder(kind, ids, id); });
      },
    });
  }

  async function readOrder(kind: ListKind): Promise<void> {
    orderReady[kind] = false;
    orders[kind] = await api<ListOrder>(`panel/list-order/${kind}`);
    orderReady[kind] = true;
  }
  async function saveOrder(kind: ListKind, visible: string[], focusId: string): Promise<void> {
    if (orderSaving || loading || submitting || reloadRequired || !orderReady[kind]) return;
    const previous = orders[kind];
    const data = mergeVisibleOrder(state[kind], previous.data, visible);
    if (JSON.stringify(data) === JSON.stringify(orderedRecords<{ id: string }>(state[kind], previous.data).map(item => item.id))) return;
    orderSaving = true; orders[kind] = { ...previous, data }; listRows();
    try {
      if (persistent) orders[kind] = await api<ListOrder>(`panel/list-order/${kind}`, jsonBody('PUT', { revision: previous.revision, data }));
      announce(t('列表顺序已保存', 'List order saved'));
    } catch (reason) {
      orders[kind] = previous;
      // 写入结果不明或修订冲突时只重新读取，绝不覆盖其他页面的顺序。
      try { await readOrder(kind); } catch { orderReady[kind] = false; }
      announce(`${(reason as Error).message} ${orderReady[kind] ? t('已重新读取列表顺序。', 'List order reloaded.') : t('顺序未确认，请刷新列表后重试。', 'Order unconfirmed; reload the list before retrying.')}`);
    } finally {
      orderSaving = false; listRows();
      if (page === kind) [...root.querySelectorAll<HTMLButtonElement>('[data-reorder]')].find(button => button.dataset.reorder === focusId)?.focus();
    }
  }
  root.addEventListener('keydown', event => {
    const button = (event.target as Element).closest<HTMLButtonElement>('[data-reorder]');
    if (!button || button.disabled || !['ArrowUp', 'ArrowDown'].includes(event.key)) return;
    event.preventDefault();
    const ids = [...root.querySelectorAll<HTMLElement>('tbody tr')].map(row => row.dataset.orderId!);
    const index = ids.indexOf(button.dataset.reorder!), next = index + (event.key === 'ArrowUp' ? -1 : 1);
    if (next < 0 || next >= ids.length) return;
    [ids[index], ids[next]] = [ids[next]!, ids[index]!];
    void saveOrder(page, ids, button.dataset.reorder!);
  });
  function renderList(): void {
    const available = state.clients.filter(item => clientStatus(viewClient(item), state.inbounds.find(record => record.id === item.inboundId)) === t('可用', 'Available')).length;
    root.innerHTML = `<header class="page-heading"><div><h1 class="page-title" tabindex="-1">${page === 'inbounds' ? t('入站列表', 'Inbounds') : t('客户端', 'Clients')}</h1><p>${page === 'inbounds' ? t('创建和管理入站，保存后立即生效。', 'Create and manage inbounds; changes apply as soon as you save.') : persistent ? t('管理客户端，查看累计流量与到期时间。', 'Manage clients and view traffic totals and expiry.') : t('管理客户端演示数据与到期时间。', 'Manage demo client data and expiry.')}</p></div>${page === 'inbounds' && persistent ? `<div class="create-actions"><button class="primary-button create-button" data-create ${!loaded || loading || reloadRequired ? 'disabled' : ''}>${icon('plus')}${t('添加入站', 'Add inbound')}</button><div class="quick-create"><button class="secondary-button" data-quick-toggle aria-haspopup="menu" aria-expanded="false" ${!loaded || loading || submitting || reloadRequired ? 'disabled' : ''}>${icon('zap')}${t('一键创建', 'Quick create')}</button><div class="quick-menu" role="menu" hidden>${(Object.keys(quickLabels) as Preset[]).map(key => `<button type="button" role="menuitem" data-quick-preset="${key}">${quickLabels[key]}</button>`).join('')}</div></div></div>` : `<button class="primary-button create-button" data-create ${!loaded || loading || reloadRequired ? 'disabled' : ''}>${icon('plus')}${page === 'inbounds' ? t('一键创建', 'Quick create') : t('添加客户端', 'Add client')}</button>`}</header>
      ${loading ? t('<p class="core-message" role="status">正在读取独立数据库…</p>', '<p class="core-message" role="status">Reading the database…</p>') : loadError ? `<p class="form-error" role="alert">${esc(loadError)}</p>` : ''}
      <p class="form-error" role="alert" id="firewall-read-error" ${firewallError ? '' : 'hidden'}>${esc(firewallError)}</p>
      <div id="firewall-maintenance" class="firewall-maintenance" hidden></div>
      ${reloadRequired ? t('<p class="form-error" role="alert">保存失败或结果未确认，当前列表可能已过期；请重新读取草稿后再修改，不会自动覆盖最新数据。</p>', '<p class="form-error" role="alert">The save failed or was not confirmed, so this list may be stale. Reload the draft before editing; the latest data is never overwritten automatically.</p>') : ''}
      <div class="workspace-bar"><div class="workspace-summary"><span>${t('入站', 'Inbounds')} <strong>${state.inbounds.length}</strong></span><span>${t('客户端', 'Clients')} <strong>${state.clients.length}</strong></span><span>${persistent ? t('可用', 'Available') : t('演示可用', 'Demo available')} <strong>${available}</strong></span></div>${persistent ? `<div class="workspace-actions"><button class="icon-button bordered" data-reload title="${t('从服务器重新读取列表（别处修改过时使用）', 'Reload the list from the server (after changes elsewhere)')}" aria-label="${t('刷新', 'Refresh')}" ${loading || submitting ? 'disabled' : ''}>${icon('refresh-cw')}</button></div>` : ''}</div>
      ${persistent ? `<div class="workspace-status"><p class="form-hint" id="workspace-application-status" role="status"></p><p class="form-hint" id="traffic-status">${esc(trafficStatus(traffic, trafficError))}</p><p class="form-hint">${t('流量约每2秒统计一次；客户端到期或用尽额度时会自动停用。', 'Traffic is counted about every 2 seconds; clients stop working automatically when they expire or run out of quota.')}</p></div>` : ''}
      <section class="card list-card" aria-label="${page === 'inbounds' ? t('入站管理', 'Inbound management') : t('客户端管理', 'Client management')}"><div class="list-toolbar"><label class="search-box">${icon('search')}<span class="sr-only">${page === 'inbounds' ? t('搜索入站', 'Search inbounds') : t('搜索客户端', 'Search clients')}</span><input type="search" id="workspace-search" value="${esc(search)}" placeholder="${page === 'inbounds' ? t('搜索名称、类型或端口', 'Search name, type or port') : t('搜索客户端或入站名称', 'Search client or inbound name')}" autocomplete="off"></label>
        <label class="filter-control"><span class="sr-only">${t('筛选状态', 'Filter by status')}</span><select id="status-filter">${(page === 'inbounds' ? [['all', t('全部状态', 'All states')], ['enabled', t('已启用', 'Enabled')], ['disabled', t('已停用', 'Disabled')]] : [['all', t('全部状态', 'All states')], ['available', persistent ? t('可用', 'Available') : t('演示可用', 'Demo available')], ['unavailable', persistent ? t('不可用', 'Unavailable') : t('演示不可用', 'Demo unavailable')]]).map(([value, label]) => `<option value="${value}"${value === filter ? ' selected' : ''}>${label}</option>`).join('')}</select></label>
        ${page === 'clients' ? `<label class="filter-control inbound-filter"><span class="sr-only">${t('筛选关联入站', 'Filter by linked inbound')}</span><select id="inbound-filter"><option value="">${t('全部入站', 'All inbounds')}</option>${state.inbounds.map(item => `<option value="${esc(item.id)}"${item.id === inboundFilter ? ' selected' : ''}>${esc(item.name)}</option>`).join('')}</select></label>` : ''}<button type="button" class="danger-button is-solid" data-delete-selected hidden>${icon('trash-2')}<span></span></button><span id="result-count" class="result-count" aria-live="polite"></span></div><div id="list-content"></div>
      <footer class="list-footer">${persistent ? t('在客户端的连接信息里可查看节点链接、二维码和订阅链接。', 'Open a client\'s connection info for node links, QR codes and subscription links.') : t('所有更改仅保留在本页内存中，刷新即恢复。未生成可用连接或订阅链接。', 'All changes stay in this page\'s memory and are restored on refresh. No usable connections or subscription links are generated.')}</footer></section>`;
    root.querySelector<HTMLInputElement>('#workspace-search')!.addEventListener('input', event => { search = (event.target as HTMLInputElement).value; listRows(); });
    root.querySelector<HTMLSelectElement>('#status-filter')!.addEventListener('change', event => { filter = (event.target as HTMLSelectElement).value; listRows(); });
    root.querySelector<HTMLSelectElement>('#inbound-filter')?.addEventListener('change', event => { inboundFilter = (event.target as HTMLSelectElement).value; listRows(); });
    listRows();
    renderFirewall();
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
    if (button.dataset.firewallRetry) { void retryFirewall(button.dataset.firewallRetry); return; }
    if (button.dataset.firewallCleanup) { void cleanupFirewall(button.dataset.firewallCleanup); return; }
    if (!loaded || loading || submitting || orderSaving || reloadRequired) return;
    if (button.hasAttribute('data-create')) { if (page === 'inbounds') quickCreate(); else clientEditor(); }
    if (button.hasAttribute('data-quick-toggle')) { quickMenu(root.querySelector<HTMLElement>('.quick-menu')!.hidden === true); return; }
    if (button.dataset.quickPreset) { quickMenu(false); void quickCreateNow(button.dataset.quickPreset as Preset); }
    if (button.hasAttribute('data-delete-selected')) { if (selected.size) deleteSelected(); return; }
    if (button.hasAttribute('data-clear')) { search = ''; filter = 'all'; inboundFilter = ''; renderList(); root.querySelector<HTMLInputElement>('#workspace-search')!.focus(); }
    if (button.dataset.op) operate(button.dataset.op, button.dataset.id!);
  });
  root.addEventListener('change', event => {
    const target = event.target as HTMLInputElement;
    if (target.dataset.select !== undefined) { if (target.checked) selected.add(target.dataset.select); else selected.delete(target.dataset.select); updateSelection(); }
    if (target.hasAttribute('data-select-all')) { root.querySelectorAll<HTMLInputElement>('[data-select]').forEach(box => { box.checked = target.checked; if (target.checked) selected.add(box.dataset.select!); else selected.delete(box.dataset.select!); }); updateSelection(); }
    // 开关先复原，确认弹窗保存成功后列表按新状态重绘。
    if (target.dataset.toggleClient !== undefined) { target.checked = !target.checked; if (loaded && !loading && !submitting && !reloadRequired) operate('toggle-client', target.dataset.toggleClient); }
    if (target.dataset.toggleNotify !== undefined) void toggleNotify(target);
  });
  async function load(force = false): Promise<void> {
    if ((!force && loaded) || loading || submitting || orderSaving) return;
    if (force && loaded && !await confirmAction(t('重新读取将丢弃当前入站或客户端弹窗中未保存的输入，并使用数据库最新列表。请先复制需要保留的内容。只读取，不保存、不应用核心。', 'Reloading discards unsaved input in the inbound or client dialog and uses the latest list from the database. Copy anything you need first. It only reads; nothing is saved or applied.'), { title: t('重新读取列表', 'Reload list'), confirmLabel: t('重新读取', 'Reload') })) return;
    const controls = [...modal.querySelectorAll<HTMLInputElement | HTMLButtonElement | HTMLSelectElement | HTMLTextAreaElement>('input,button,select,textarea')];
    const disabled = controls.map(control => control.disabled); controls.forEach(control => { control.disabled = true; });
    loading = true; loadError = ''; renderList();
    try {
      const document = await api<{ revision: number; data: Workspace | null }>('workspace');
      state = document.data ?? { inbounds: [], clients: [] }; revision = document.revision; loaded = true; reloadRequired = false;
      for (const kind of ['inbounds', 'clients'] as const) {
        try { await readOrder(kind); } catch { announce(t('列表顺序读取失败，拖动暂不可用；请刷新重试。', 'List order could not be loaded; reload to enable reordering.')); }
      }
      await refreshFirewall();
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
  function firewallCell(inbound: Inbound): string {
    const key = `${inbound.port}/${inbound.preset === 'hy2' ? 'udp' : 'tcp'}`;
    const status = firewall?.workspaceRevision === revision ? firewall.endpoints[key] : undefined;
    const hint = status ? `${status.message} · ${status.checkedAt}` : t('尚无放行记录；不代表公网可达。', 'No firewall record; public reachability is unverified.');
    return `<small class="table-secondary${status?.state === 'failed' ? ' form-error' : ''}" title="${esc(hint)}">${esc(firewallStatusText(status))}</small>${status?.state !== 'allowed' ? `<button class="icon-button" data-firewall-retry="${esc(inbound.id)}" title="${t('重试放行防火墙', 'Retry firewall update')}" aria-label="${t('重试放行防火墙', 'Retry firewall update')}" ${firewallBusy || loading || submitting ? 'disabled' : ''}>${icon('refresh-cw')}</button>` : ''}`;
  }
  function renderFirewall(): void {
    root.querySelectorAll<HTMLElement>('[data-inbound-firewall]').forEach(cell => {
      const inbound = state.inbounds.find(item => item.id === cell.dataset.inboundFirewall);
      if (inbound) cell.innerHTML = firewallCell(inbound);
    });
    const error = root.querySelector<HTMLElement>('#firewall-read-error');
    if (error) {
      error.textContent = firewallError; error.hidden = !firewallError;
    }
    const maintenance = root.querySelector<HTMLElement>('#firewall-maintenance');
    if (maintenance) {
      const endpoints = new Set(state.inbounds.map(item => `${item.port}/${item.preset === 'hy2' ? 'udp' : 'tcp'}`));
      const old = Object.entries(firewall?.endpoints ?? {}).filter(([key, value]) => !endpoints.has(key) && value.state !== 'allowed');
      // 周期更新只切换控件状态，保留用户展开的详情和键盘焦点。
      const signature = JSON.stringify(old);
      if (maintenance.dataset.signature !== signature) {
        const expanded = maintenance.querySelector('details')?.open ?? false;
        maintenance.dataset.signature = signature;
        maintenance.innerHTML = old.length ? `<details><summary>${t('旧端口防火墙待处理', 'Old-port firewall maintenance')} <span class="muted-text">${old.length}</span></summary><p>${t('这些端口已不在当前入站列表中，上次防火墙操作未完成。不会自动清除记录，也不代表当前节点不通。', 'These ports are no longer in the inbound list, and their last firewall update did not complete. Records are kept; this does not mean current nodes are unreachable.')}</p><ul>${old.map(([key, value]) => `<li><div><strong>${esc(key)}</strong><small>${esc(value.message || t('原因未记录，请核查服务器防火墙。', 'No reason recorded; inspect the server firewall.'))}</small><time>${esc(value.checkedAt)}</time></div>${oldFirewallCleanupAllowed(value) ? `<button type="button" class="secondary-button" data-firewall-cleanup="${esc(key)}">${icon('refresh-cw')}${t('重试撤销', 'Retry removal')}</button>` : `<small>${t('请在服务器核查此记录；不会自动撤销转发或外部规则。', 'Inspect this record on the server; forwarding and external rules are not removed automatically.')}</small>`}</li>`).join('')}</ul></details>` : '';
        const details = maintenance.querySelector('details');
        if (details) details.open = expanded;
      }
      maintenance.hidden = !old.length;
      maintenance.querySelectorAll<HTMLButtonElement>('button').forEach(button => { button.disabled = !!firewallBusy || loading || submitting || reloadRequired; });
    }
    refreshIcons();
  }
  async function refreshFirewall(): Promise<void> {
    if (!persistent) return;
    try {
      const doc = await api<{ data: FirewallReport | null }>('runtime/firewall');
      firewall = doc.data; firewallError = '';
    } catch {
      firewall = null; firewallError = t('防火墙状态读取失败，请重新读取列表。', 'Failed to read firewall status; reload the list.');
    }
    renderFirewall();
  }
  async function retryFirewall(id: string): Promise<void> {
    if (firewallBusy || loading || submitting || !loaded || reloadRequired) return;
    firewallBusy = id; renderFirewall();
    try {
      firewall = await api<FirewallReport>('runtime/firewall/retry', jsonBody('POST', { inboundId: id }));
      announce(firewall.message || t('防火墙操作完成，请重试客户端连接。', 'Firewall update finished; retry the client connection.'));
    } catch (reason) {
      await refreshFirewall();
      announce(reason instanceof Error ? reason.message : t('防火墙操作失败', 'Firewall update failed'));
    } finally { firewallBusy = ''; renderFirewall(); }
  }
  async function cleanupFirewall(endpoint: string): Promise<void> {
    if (firewallBusy || loading || submitting || !loaded || reloadRequired) return;
    if (!await confirmAction(t(`重试撤销旧端口 ${endpoint} 的 o-ui 放行规则。只处理 o-ui 自己记录的规则，保留外部规则，不重启核心。服务器会再次检查该端口未被当前入站使用。`, `Retry removing o-ui's allow rule for old port ${endpoint}. Only o-ui-owned rules are handled; external rules are kept and the core is not restarted. The server rechecks that no current inbound uses the port.`), { title: t('撤销旧端口放行', 'Remove old-port rule'), confirmLabel: t('重试撤销', 'Retry removal'), danger: true })) return;
    if (firewallBusy || loading || submitting || reloadRequired) return;
    firewallBusy = endpoint; renderFirewall();
    try {
      firewall = await api<FirewallReport>('runtime/firewall/cleanup', jsonBody('POST', { endpoint }));
      announce(firewall.message || t('旧端口防火墙操作完成。', 'Old-port firewall update finished.'));
    } catch (reason) {
      await refreshFirewall();
      announce(reason instanceof Error ? reason.message : t('撤销失败，记录已保留。', 'Removal failed; the record is kept.'));
    } finally { firewallBusy = ''; renderFirewall(); }
  }
  async function refreshTraffic(): Promise<void> {
    if (trafficLoading || document.hidden || root.hidden) return;
    trafficLoading = true;
    try {
      const previous = traffic;
      traffic = await api<TrafficSnapshot>('runtime/traffic'); trafficError = '';
      const seconds = previous?.sampledAt && traffic.sampledAt ? (Date.parse(traffic.sampledAt) - Date.parse(previous.sampledAt)) / 1000 : 0;
      speeds = {};
      if (seconds > 0 && seconds <= 30) {
        for (const [id, count] of Object.entries(traffic.totals)) {
          const before = previous!.totals[id];
          if (!before) continue;
          const up = Number(BigInt(count.uplink) - BigInt(before.uplink)), down = Number(BigInt(count.downlink) - BigInt(before.downlink));
          if (up >= 0 && down >= 0) speeds[id] = { up: up / seconds, down: down / seconds };
        }
      } if (page === 'clients') { ipView = await api<IPLimitView>('runtime/iplimit').catch(() => ipView); ipDialogRefresh?.(); subscriptionService = await api<{ data: { enabled: boolean } }>('panel/subscription').then(s => s.data.enabled, () => subscriptionService); notifyView = await api<{ clients: string[]; botEnabled: boolean }>('panel/telegram/notify').then(v => ({ clients: new Set(v.clients), botEnabled: v.botEnabled }), () => notifyView); } }
    catch (reason) { trafficError = (reason as Error).message; }
    finally {
      trafficLoading = false;
      const label = root.querySelector('#traffic-status'); if (label) label.textContent = trafficStatus(traffic, trafficError);
      // 周期更新不替换操作按钮，避免用户的键盘焦点丢失。
      if (!root.hidden && !root.contains(document.activeElement) && !document.querySelector('dialog[open]')) listRows();
      else root.querySelectorAll<HTMLElement>('[data-client-row]').forEach(row => {
        const item = state.clients.find(client => client.id === row.dataset.clientRow); if (!item) return;
        row.querySelector('[data-client-traffic]')!.innerHTML = trafficCell(item);
        row.querySelector('[data-client-last]')!.innerHTML = lastOnlineCell(item);
        const ipTarget = row.querySelector<HTMLElement>('[data-client-ip]');
        if (ipTarget && !ipTarget.contains(document.activeElement)) ipTarget.innerHTML = `${ipCell(item)}<small class="table-secondary">${subscriptionLabel(item)}</small>`;
        const notifyTarget = row.querySelector<HTMLElement>('[data-client-notify]');
        if (notifyTarget && !notifyTarget.contains(document.activeElement)) notifyTarget.innerHTML = notifyCell(item);
      });
      root.querySelectorAll<HTMLElement>('[data-inbound-traffic]').forEach(cell => { cell.innerHTML = inboundTraffic(cell.dataset.inboundTraffic!); });
    }
  }
  if (persistent) {
    window.setInterval(() => { void refreshTraffic(); }, 5000);
    window.setInterval(() => { if (!root.hidden && !document.hidden) { updateApplicationStatus(); ipDialogClock?.(); } }, 1000);
  }
  return { render(next) { if (page !== next) { search = ''; filter = 'all'; selected.clear(); if (next === 'inbounds') inboundFilter = ''; } page = next; renderList(); if (!loaded) void load(); if (persistent) void refreshTraffic(); }, setRuntime(status, at) { runtime = status; runtimeAt = at; updateApplicationStatus(); } };
}
