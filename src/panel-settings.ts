import { api, apiURL, APIError, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { lang, languageNames, setLang, t, type Lang } from './i18n.ts';
import { settingsToolbar, settingSection, settingRow, toggle, selectControl, textControl } from './settings-ui.ts';
import { accessURL, generalForm, generalRequest, type GeneralForm, type GeneralStatus } from './panel-access.ts';
import { subscriptionForm, subscriptionRequest, type SubscriptionForm, type SubscriptionStatus } from './subscription-settings.ts';
import { planFields, planForm, planRows } from './server-plan.ts';
import { telegramForm, telegramRequest, type TelegramForm, type TelegramStatus } from './telegram-settings.ts';
import { parsePlanForm, type PlanStatus } from './server-plan-model.ts';
import { passwordChangeError } from './password.ts';
import { connectionQR } from './connection-qr.ts';
import './panel-settings.css';

export interface PanelTLSStatus {
  active: boolean; serverNames: string[]; revision: number; activeRevision: number;
  data: { certificateFile: string; keyFile: string } | null;
}
type Tab = 'general' | 'security' | 'subscription' | 'telegram';
interface Values { general: GeneralForm; plan: Record<string, string>; language: string; subscription: SubscriptionForm; telegram: TelegramForm }
type Section = keyof Values;
// 表单字段所属分区；账号表单与两步验证不在其中，它们是即时操作。
const sections: Record<string, Section> = {
  listenIP: 'general', domain: 'general', port: 'general', path: 'general', sessionMinutes: 'general', certificateFile: 'general', keyFile: 'general',
  planMode: 'plan', node: 'plan', name: 'plan', resources: 'plan', quotaGB: 'plan', resetDay: 'plan', billing: 'plan', interfaces: 'plan', usedGB: 'plan', providerURL: 'plan', providerVEID: 'plan', providerKey: 'plan',
  language: 'language', enabled: 'subscription', title: 'subscription', updateHours: 'subscription', showInfo: 'subscription', remarkClientName: 'subscription',
  clashEnabled: 'subscription', jsonEnabled: 'subscription', jsonFragment: 'subscription', fragmentPackets: 'subscription', fragmentLength: 'subscription', fragmentInterval: 'subscription',
  jsonDirectIP: 'subscription', jsonDirectDomain: 'subscription',
  tgEnabled: 'telegram', tgToken: 'telegram', tgChatIds: 'telegram', tgReportTime: 'telegram', tgNotifyLogin: 'telegram', tgCpuThreshold: 'telegram', tgBackupEnabled: 'telegram', tgBackupPassword: 'telegram', tgClearBackupPassword: 'telegram',
};
const copy = (values: Values): Values => JSON.parse(JSON.stringify(values));

// 参照OUI的面板设置：顶部“保存/重启面板”，常规、安全、订阅三个标签，每项为“标题+一行说明 | 控件”。
// 整页共用一个“保存”；表单值保存在draft中，重绘不丢未保存的修改。
export function initPanelSettings(root: HTMLElement, persistent: boolean): { render: () => void; clearPassword: () => void } {
  let general: GeneralStatus | null = null, plan: PlanStatus | null = null, subscription: SubscriptionStatus | null = null, telegram: TelegramStatus | null = null;
  let interfaces: string[] = [], username = '', twoFactor = false;
  let loading = false, saving = false, reloadRequired = false, error = '', note = '', tab: Tab = 'general';
  let baseline: Values | null = null, draft: Values | null = null;
  const dialog = document.createElement('dialog');
  dialog.className = 'twofactor-dialog';
  document.body.append(dialog);
  window.addEventListener('panel-session-expired', () => { if (dialog.open) dialog.close(); });

  const saved = (): Values => ({ general: generalForm(general!), plan: planForm(plan), language: lang, subscription: subscriptionForm(subscription!.data), telegram: telegramForm(telegram!.data) });
  const changed = (key: Section) => Boolean(draft && baseline) && JSON.stringify(draft![key]) !== JSON.stringify(baseline![key]);
  const dirty = () => (['general', 'plan', 'language', 'subscription', 'telegram'] as Section[]).some(changed);

  function status(): void {
    const save = root.querySelector<HTMLButtonElement>('#settings-save');
    if (save) { save.disabled = !draft || saving || loading || reloadRequired || !dirty(); save.textContent = saving ? t('正在保存…', 'Saving…') : t('保存', 'Save'); }
    const reload = root.querySelector<HTMLButtonElement>('#settings-reload'), restart = root.querySelector<HTMLButtonElement>('#panel-restart');
    if (reload) reload.disabled = !persistent || saving || loading;
    if (restart) restart.disabled = !persistent || saving;
    const alert = root.querySelector<HTMLElement>('#settings-error'), message = root.querySelector<HTMLElement>('#settings-note');
    if (alert) { alert.textContent = error; alert.hidden = !error; }
    if (message) message.textContent = loading ? t('正在读取…', 'Reading…') : note;
  }

  function generalTab(v: GeneralForm, language: string): string {
    const names = general?.tls.active ? general.tls.serverNames.join(', ') : '';
    const pending = general && (general.access.pending || general.tls.pending) ? t('（有已保存但未生效的设置，重启面板后生效）', ' (saved settings are waiting for a panel restart)') : '';
    return `<form id="settings-general" novalidate>${settingSection(t('常规', 'General') + pending, [
      settingRow(t('面板监听 IP', 'Listen IP'), t('留空监听全部 IP；填 127.0.0.1 为仅本机访问', 'Leave empty to listen on all IPs; 127.0.0.1 means local access only'), textControl('listenIP', v.listenIP, t('面板监听 IP', 'Listen IP'), 'maxlength="45" placeholder="0.0.0.0"')),
      settingRow(t('面板监听域名', 'Listen domain'), t('浏览器访问用的域名或 IP，须与证书一致；仅本机时不用填', 'The domain or IP used in the browser; it must match the certificate. Not needed for local only'), textControl('domain', v.domain, t('面板监听域名', 'Listen domain'), 'maxlength="253" placeholder="panel.example.com"')),
      settingRow(t('面板监听端口', 'Listen port'), t('1024–65535，须未被占用', '1024–65535; must be unused'), textControl('port', v.port, t('面板监听端口', 'Listen port'), 'type="number" min="1024" max="65535" inputmode="numeric"')),
      settingRow(t('面板访问路径', 'URI path'), t("以 '/' 开头和结尾，如 /abc123/；/ 为根路径", "Begins and ends with '/', e.g. /abc123/; / is the root"), textControl('path', v.path, t('面板访问路径', 'URI path'), 'maxlength="66"')),
      settingRow(t('会话时长', 'Session duration'), t('保持登录的时长（分钟），对之后的登录生效', 'How long you stay signed in (minutes); applies to new sign-ins'), textControl('sessionMinutes', v.sessionMinutes, t('会话时长', 'Session duration'), 'type="number" min="5" max="43200" inputmode="numeric"')),
      settingRow(t('语言', 'Language'), '', selectControl('language', language, (Object.keys(languageNames) as Lang[]).map(code => [code, languageNames[code]]), t('语言', 'Language'))),
    ].join(''))}${settingSection(t('证书', 'Certificates'), [
      settingRow(t('面板证书公钥文件路径', 'Public key path'), names ? `${t('当前已加载：', 'Loaded now: ')}${names}` : t("填写一个 '/' 开头的绝对路径", "An absolute path beginning with '/'"), textControl('certificateFile', v.certificateFile, t('面板证书公钥文件路径', 'Public key path'), 'maxlength="4096"')),
      settingRow(t('面板证书密钥文件路径', 'Private key path'), t("填写一个 '/' 开头的绝对路径", "An absolute path beginning with '/'"), textControl('keyFile', v.keyFile, t('面板证书密钥文件路径', 'Private key path'), 'maxlength="4096"')),
    ].join(''))}${settingSection(t('服务器套餐', 'Server plan'), planRows(draft?.plan ?? planForm(null), plan, interfaces), false)}</form>`;
  }

  function securityTab(): string {
    return `<form id="settings-account" novalidate>${settingSection(t('管理员凭据', 'Admin credentials'), [
      settingRow(t('原用户名', 'Current username'), '', `<span class="setting-value">${esc(username || '—')}</span>`),
      settingRow(t('原密码', 'Current password'), '', textControl('current', '', t('原密码', 'Current password'), 'type="password" autocomplete="current-password" maxlength="72"')),
      settingRow(t('新用户名', 'New username'), t('不改可留空', 'Leave empty to keep'), textControl('username', '', t('新用户名', 'New username'), 'autocomplete="username" maxlength="64"')),
      settingRow(t('新密码', 'New password'), t('6–72 字节；不改可留空', '6–72 bytes; leave empty to keep'), textControl('next', '', t('新密码', 'New password'), 'type="password" autocomplete="new-password" maxlength="72"')),
      settingRow(t('确认新密码', 'Confirm new password'), '', textControl('confirm', '', t('确认新密码', 'Confirm new password'), 'type="password" autocomplete="new-password" maxlength="72"')),
      twoFactor ? settingRow(t('验证码', 'Code'), t('两步验证已启用，请输入验证器中的验证码', 'Two-factor is on; enter the code from your authenticator'), textControl('code', '', t('验证码', 'Code'), 'inputmode="numeric" autocomplete="one-time-code" maxlength="6"')) : '',
      settingRow('', t('修改后所有登录失效，需用新账号重新登录', 'All sessions end and you sign in again with the new credentials'), `<button type="submit" class="primary-button" id="account-save">${t('确认', 'Confirm')}</button>`),
    ].join(''))}</form>
    <form id="settings-twofactor">${settingSection(t('双重验证', 'Two-factor authentication'), settingRow(t('启用2FA', 'Enable 2FA'), t('增加额外的验证层以提高安全性；登录时需输入验证器中的验证码', 'Adds another verification step; signing in then needs a code from your authenticator'), toggle('twoFactor', twoFactor, t('启用2FA', 'Enable 2FA'))))}</form>`;
  }

  function subscriptionTab(v: SubscriptionForm): string {
    return `<form id="settings-subscription" novalidate>${settingSection(t('订阅', 'Subscription'), [
      settingRow(t('启用订阅服务', 'Subscription service'), t('关闭后所有订阅链接暂停返回内容', 'When off, every subscription link stops returning content'), toggle('enabled', v.enabled, t('启用订阅服务', 'Subscription service'))),
      settingRow(t('订阅标题', 'Subscription title'), t('在客户端中显示的标题', 'Title shown in clients'), textControl('title', v.title, t('订阅标题', 'Subscription title'), 'maxlength="64"')),
      settingRow(t('更新间隔', 'Update interval'), t('客户端自动更新订阅的间隔（小时）', 'How often clients refresh the subscription (hours)'), textControl('updateHours', v.updateHours, t('更新间隔', 'Update interval'), 'type="number" min="1" max="168" inputmode="numeric"')),
    ].join(''))}${settingSection(t('信息', 'Information'), [
      settingRow(t('显示使用信息', 'Show usage info'), t('客户端中显示已用流量、总额度和到期时间', 'Clients show used traffic, quota and expiry'), toggle('showInfo', v.showInfo, t('显示使用信息', 'Show usage info'))),
      settingRow(t('节点名称包含客户端名', 'Client name in node name'), t('节点名称为“入站名-客户端名”，关闭则只用入站名', 'Node names become "inbound-client"; when off only the inbound name is used'), toggle('remarkClientName', v.remarkClientName, t('节点名称包含客户端名', 'Client name in node name'))),
    ].join(''))}${settingSection(t('订阅格式', 'Subscription formats'), [
      settingRow(t('Clash / Mihomo 订阅', 'Clash / Mihomo subscription'), t('同一订阅链接加 /clash/，返回 Clash 配置（Mihomo、Clash Verge、Stash 等）', 'Add /clash/ to the same link to get a Clash profile (Mihomo, Clash Verge, Stash and others)'), toggle('clashEnabled', v.clashEnabled, t('Clash / Mihomo 订阅', 'Clash / Mihomo subscription'))),
      settingRow(t('Xray JSON 订阅', 'Xray JSON subscription'), t('同一订阅链接加 /json/，返回完整 Xray 客户端配置（v2rayN、v2rayNG 等）', 'Add /json/ to the same link to get a full Xray client config (v2rayN, v2rayNG and others)'), toggle('jsonEnabled', v.jsonEnabled, t('Xray JSON 订阅', 'Xray JSON subscription'))),
    ].join(''))}${settingSection(t('JSON 订阅选项', 'JSON subscription options'), [
      settingRow(t('TLS 握手分片', 'TLS handshake fragment'), t('把 TLS 握手拆成小片发送，可绕过部分按 SNI 的封锁；只作用于 TCP 节点，HY2 不受影响', 'Splits the TLS handshake into small pieces to get around some SNI blocking; TCP nodes only, HY2 is not affected'), toggle('jsonFragment', v.jsonFragment, t('TLS 握手分片', 'TLS handshake fragment'))),
      settingRow(t('分片包', 'Packets'), t('tlshello 只分片握手，或填包序号范围如 1-3', 'tlshello fragments only the handshake; or a packet range such as 1-3'), textControl('fragmentPackets', v.fragmentPackets, t('分片包', 'Packets'), 'maxlength="11"')),
      settingRow(t('分片长度', 'Length'), t('每片字节数，如 100-200', 'Bytes per piece, e.g. 100-200'), textControl('fragmentLength', v.fragmentLength, t('分片长度', 'Length'), 'maxlength="11"')),
      settingRow(t('分片间隔', 'Interval'), t('片间隔毫秒，如 10-20', 'Milliseconds between pieces, e.g. 10-20'), textControl('fragmentInterval', v.fragmentInterval, t('分片间隔', 'Interval'), 'maxlength="11"')),
      settingRow(t('直连 IP', 'Direct IPs'), t('每行一项，如 geoip:private、geoip:cn 或 IP/CIDR；匹配的目标不走代理', 'One per line, e.g. geoip:private, geoip:cn or IP/CIDR; matching targets bypass the proxy'), `<textarea name="jsonDirectIP" rows="3" spellcheck="false" aria-label="${t('直连 IP', 'Direct IPs')}" placeholder="geoip:private">${esc(v.jsonDirectIP)}</textarea>`),
      settingRow(t('直连域名', 'Direct domains'), t('每行一项，如 geosite:private、geosite:cn 或域名；匹配的目标不走代理', 'One per line, e.g. geosite:private, geosite:cn or a domain; matching targets bypass the proxy'), `<textarea name="jsonDirectDomain" rows="3" spellcheck="false" aria-label="${t('直连域名', 'Direct domains')}" placeholder="geosite:cn">${esc(v.jsonDirectDomain)}</textarea>`),
    ].join(''), false)}</form>`;
  }

  function telegramTab(v: TelegramForm): string {
    const view = telegram?.data;
    const secret = (name: string, value: string, set: boolean | undefined, label: string) => `<input name="${name}" type="password" value="${esc(value)}" aria-label="${label}" autocomplete="new-password" spellcheck="false" placeholder="${set ? t('已设置，留空保持不变', 'Set; leave empty to keep') : t('未设置', 'Not set')}">`;
    return `<form id="settings-telegram" novalidate>${settingSection(t('Telegram 机器人', 'Telegram bot'), [
      settingRow(t('启用机器人', 'Enable bot'), t('发送通知；管理员给机器人发 /start 打开管理菜单（参照 OUI：状态、流量排行、在线、客户端启停与节点链接、一键创建入站、备份、重启 Xray、升级面板）。只响应下面的管理员 Chat ID', 'Sends notifications; admins send /start to the bot for the admin menu (like OUI: status, traffic ranking, online clients, enable/disable clients and node links, quick create inbound, backup, restart Xray, update the panel). Only the admin chat IDs below are answered'), toggle('tgEnabled', v.tgEnabled, t('启用机器人', 'Enable bot'))),
      settingRow('Bot Token', t('在 Telegram 找 @BotFather 创建机器人后获得；只写不读', 'Create a bot with @BotFather in Telegram to get it; write-only'), secret('tgToken', v.tgToken, view?.tokenSet, 'Bot Token')),
      settingRow(t('管理员 Chat ID', 'Admin chat IDs'), t('给机器人发 /id 可查看自己的 ID（也可问 @userinfobot）；多个用英文逗号分隔', 'Send /id to the bot to see your ID (or ask @userinfobot); separate several with commas'), textControl('tgChatIds', v.tgChatIds, t('管理员 Chat ID', 'Admin chat IDs'), 'maxlength="230" inputmode="numeric" placeholder="123456789"')),
    ].join(''))}${settingSection(t('通知', 'Notifications'), [
      settingRow(t('登录提醒', 'Sign-in alerts'), t('面板登录成功或失败时发送用户名、IP 和时间', 'Sends the username, IP and time on each successful or failed sign-in'), toggle('tgNotifyLogin', v.tgNotifyLogin, t('登录提醒', 'Sign-in alerts'))),
      settingRow(t('每日报告时间', 'Daily report time'), t('服务器本地时间；报告含 Xray 状态、资源、业务流量和即将到期的客户端。留空不发送', 'Server local time; includes Xray status, resources, proxy traffic and clients expiring soon. Leave empty to disable'), textControl('tgReportTime', v.tgReportTime, t('每日报告时间', 'Daily report time'), 'type="time"')),
      settingRow(t('CPU 提醒阈值（%）', 'CPU alert threshold (%)'), t('CPU 使用率达到该值时提醒，30 分钟内不重复；0 表示关闭', 'Alerts when CPU usage reaches this value, at most once per 30 minutes; 0 turns it off'), textControl('tgCpuThreshold', v.tgCpuThreshold, t('CPU 提醒阈值（%）', 'CPU alert threshold (%)'), 'type="number" min="0" max="100" inputmode="numeric"')),
    ].join(''))}${settingSection(t('备份', 'Backup'), [
      settingRow(t('随每日报告发送备份', 'Send backup with the daily report'), t('设了口令发送加密备份（.ixpb），没设则发送不加密备份（.json，含私钥）；都可在“备份和恢复”中恢复', 'Sends an encrypted backup (.ixpb) when a password is set, otherwise an unencrypted one (.json, contains private keys); restore either under Backup and restore'), toggle('tgBackupEnabled', v.tgBackupEnabled, t('随每日报告发送备份', 'Send backup with the daily report'))),
      settingRow(t('备份加密口令（可选）', 'Backup password (optional)'), t('留空则不加密；长度不限，遗忘则加密备份无法恢复。只写不读', 'Leave empty for unencrypted backups; any length, encrypted backups cannot be restored without it. Write-only'), secret('tgBackupPassword', v.tgBackupPassword, view?.backupPasswordSet, t('备份加密口令', 'Backup password'))),
      view?.backupPasswordSet ? settingRow(t('清除备份口令', 'Clear backup password'), t('打开并保存后清除已设置的口令，之后 Telegram 备份不加密', 'Turn on and save to remove the saved password; Telegram backups are then unencrypted'), toggle('tgClearBackupPassword', v.tgClearBackupPassword, t('清除备份口令', 'Clear backup password'))) : '',
    ].join(''))}</form>
    <div class="settings-table-actions"><button type="button" class="secondary-button" id="telegram-test">${t('发送测试消息', 'Send test message')}</button><button type="button" class="secondary-button" id="telegram-report">${t('立即发送报告', 'Send report now')}</button></div>`;
  }

  const tabs: [Tab, string][] = [['general', t('常规', 'General')], ['security', t('安全', 'Security')], ['subscription', t('订阅', 'Subscription')], ['telegram', 'Telegram']];
  function paint(): void {
    const v = draft ?? { general: { listenIP: '', domain: '', port: '', path: '/', sessionMinutes: '720', certificateFile: '', keyFile: '' }, plan: planForm(null), language: lang, subscription: { enabled: true, title: 'o-ui', updateHours: '12', showInfo: true, remarkClientName: true, clashEnabled: true, jsonEnabled: true, jsonFragment: false, fragmentPackets: 'tlshello', fragmentLength: '100-200', fragmentInterval: '10-20', jsonDirectIP: '', jsonDirectDomain: '' }, telegram: { tgEnabled: false, tgToken: '', tgChatIds: '', tgReportTime: '', tgNotifyLogin: false, tgCpuThreshold: '0', tgBackupEnabled: false, tgBackupPassword: '', tgClearBackupPassword: false } };
    root.innerHTML = `<header class="page-heading"><div><h1 class="page-title" tabindex="-1">${t('面板设置', 'Panel settings')}</h1></div></header>
      ${settingsToolbar(`<button type="button" class="primary-button" id="settings-save" disabled>${t('保存', 'Save')}</button><button type="button" class="secondary-button" id="settings-reload">${t('重新读取', 'Reload')}</button><button type="button" class="danger-button" id="panel-restart">${t('重启面板', 'Restart panel')}</button>`, t('此处的修改需要保存，重启面板后生效', 'Every change made here needs to be saved. Restart the panel to apply changes.'))}
      <p class="form-error settings-feedback" id="settings-error" role="alert" tabindex="-1" ${error ? '' : 'hidden'}>${esc(error)}</p><p class="core-message settings-feedback" id="settings-note" role="status">${esc(note)}</p>
      <section class="card xray-settings-card"><div class="xray-settings-tabs" role="tablist" aria-label="${t('面板设置分类', 'Panel setting categories')}">${tabs.map(([id, label]) => `<button type="button" role="tab" aria-selected="${id === tab}" data-settings-tab="${id}">${label}</button>`).join('')}</div>
      ${!persistent ? `<p class="form-hint">${t('离线演示不读取或保存设置，请使用面板服务。', 'The offline demo does not read or save settings; use the running panel.')}</p>` : ''}
      <div role="tabpanel" data-settings-panel="general" ${tab === 'general' ? '' : 'hidden'}>${generalTab(v.general, v.language)}</div>
      <div role="tabpanel" data-settings-panel="security" ${tab === 'security' ? '' : 'hidden'}>${securityTab()}</div>
      <div role="tabpanel" data-settings-panel="subscription" ${tab === 'subscription' ? '' : 'hidden'}>${subscriptionTab(v.subscription)}</div>
      <div role="tabpanel" data-settings-panel="telegram" ${tab === 'telegram' ? '' : 'hidden'}>${telegramTab(v.telegram)}</div></section>`;
    root.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLButtonElement>('#settings-general input, #settings-general select, #settings-subscription input, #settings-subscription textarea, #settings-telegram input, #telegram-test, #telegram-report, #settings-account input, #settings-account button, #settings-twofactor input').forEach(element => { element.disabled = !draft; });
    status();
  }

  function edit(target: HTMLInputElement): void {
    const section = sections[target.name];
    if (!draft || !section || !target.closest?.('#settings-general, #settings-subscription, #settings-telegram')) return;
    const value = target.type === 'checkbox' ? target.checked : target.value;
    if (section === 'language') draft.language = String(value);
    else (draft[section] as unknown as Record<string, string | boolean>)[target.name] = value;
    // 切换套餐方式时只换下面的设置行，其它输入不受影响。
    if (target.name === 'planMode') { const fields = root.querySelector('[data-plan-fields]'); if (fields) fields.innerHTML = planFields(draft.plan, plan, interfaces); }
    note = '';
    status();
  }
  root.addEventListener('input', event => edit(event.target as HTMLInputElement));
  root.addEventListener('change', event => {
    const target = event.target as HTMLInputElement;
    if (target.name === 'twoFactor' && target.closest?.('#settings-twofactor')) { target.checked = twoFactor; void openTwoFactor(!twoFactor); return; }
    edit(target);
  });
  root.addEventListener('click', event => {
    const target = event.target as HTMLElement;
    const tabButton = target.closest<HTMLButtonElement>('[data-settings-tab]');
    if (tabButton) {
      tab = tabButton.dataset.settingsTab as Tab;
      root.querySelectorAll<HTMLButtonElement>('[data-settings-tab]').forEach(button => button.setAttribute('aria-selected', String(button === tabButton)));
      root.querySelectorAll<HTMLElement>('[data-settings-panel]').forEach(panel => { panel.hidden = panel.dataset.settingsPanel !== tab; });
      return;
    }
    if (target.closest('#settings-save')) void save();
    else if (target.closest('#settings-reload')) void reload();
    else if (target.closest('#panel-restart')) void restart(target.closest<HTMLButtonElement>('#panel-restart')!);
    else if (target.closest('#telegram-test')) void testTelegram(false);
    else if (target.closest('#telegram-report')) void testTelegram(true);
  });
  root.addEventListener('submit', event => {
    event.preventDefault();
    if ((event.target as HTMLElement).id === 'settings-account') void changeAccount(event.target as HTMLFormElement);
  });

  // 冲突、网络中断或服务器错误时结果不确定：保留输入但禁止再写旧修订，须先重新读取。
  function fail(reason: unknown): void {
    error = (reason as Error).message;
    if (!(reason instanceof APIError) || reason.status === 409 || reason.status >= 500) {
      reloadRequired = true;
      error += t('；结果无法确认或设置已在别处修改，已保留你的输入。请先复制需要保留的内容，再点“重新读取”。', '; the result is uncertain or the settings changed elsewhere. Your input is kept; copy what you need, then click Reload.');
    }
    paint();
    root.querySelector<HTMLElement>('#settings-error')?.focus();
  }

  async function save(): Promise<void> {
    if (saving || loading || reloadRequired || !draft || !baseline || !general || !plan || !subscription || !telegram) return;
    const now = copy(draft);
    let generalBody: ReturnType<typeof generalRequest> | null = null, planBody: ReturnType<typeof parsePlanForm> | null = null, subscriptionBody: ReturnType<typeof subscriptionRequest> | null = null, telegramBody: ReturnType<typeof telegramRequest> | null = null;
    try {
      if (changed('general')) generalBody = generalRequest(now.general, general);
      if (changed('plan')) planBody = parsePlanForm(now.plan);
      if (changed('subscription')) subscriptionBody = subscriptionRequest(now.subscription);
      if (changed('telegram')) telegramBody = telegramRequest(now.telegram, telegram.data);
    } catch (reason) { note = ''; error = (reason as Error).message; status(); root.querySelector<HTMLElement>('#settings-error')?.focus(); return; }
    if (generalBody?.enablesRemote && !window.confirm(t('确认允许从其他设备经 HTTPS 访问面板和订阅？持有订阅链接者可取得代理凭据。', 'Allow the panel and subscriptions to be reached over HTTPS from other devices? Anyone with a subscription link can obtain proxy credentials.'))) return;
    saving = true; error = ''; note = ''; status();
    try {
      // 每个分区保存成功即更新基线，后续分区失败时不会重复提交已保存的部分。
      if (generalBody) { general = await api<GeneralStatus>('panel/general', jsonBody('PUT', generalBody.body)); baseline.general = now.general; }
      if (planBody) {
        plan = await api<PlanStatus>('system/plan', jsonBody('PUT', { revision: plan.revision, data: planBody.plan, usedGB: planBody.usedGB }));
        baseline.plan = planForm(plan); draft.plan = { ...draft.plan, usedGB: '' };
      }
      if (subscriptionBody) { subscription = await api<SubscriptionStatus>('panel/subscription', jsonBody('PUT', { revision: subscription.revision, data: subscriptionBody })); baseline.subscription = now.subscription; }
      // 令牌与口令只写不读：保存后清空输入框，只显示“已设置”。
      if (telegramBody) { telegram = await api<TelegramStatus>('panel/telegram', jsonBody('PUT', { revision: telegram.revision, data: telegramBody })); baseline.telegram = telegramForm(telegram.data); draft.telegram = telegramForm(telegram.data); }
      saving = false;
      if (changed('language')) { setLang(now.language as Lang); return; }
      note = generalBody ? t('已保存。监听、域名、端口、路径与证书在重启面板后生效；会话时长对之后的登录生效。', 'Saved. Listen, domain, port, path and certificates apply after the panel restarts; the session duration applies to new sign-ins.') : t('已保存。', 'Saved.');
      paint();
    } catch (reason) { saving = false; fail(reason); }
  }

  // 按已保存的令牌与Chat ID发送；有未保存的Telegram修改时先提示保存。
  async function testTelegram(report: boolean): Promise<void> {
    if (saving || loading || !telegram) return;
    if (changed('telegram')) { note = ''; error = t('请先保存 Telegram 设置，再发送测试。', 'Save the Telegram settings before sending a test.'); status(); return; }
    error = ''; note = t('正在发送…', 'Sending…'); status();
    try {
      const result = await api<{ sent: number; total: number }>(`panel/telegram/test${report ? '?report=1' : ''}`, jsonBody('POST', {}));
      note = `${t('已发送到 ', 'Sent to ')}${result.sent}/${result.total}${t(' 个 Chat。', ' chats.')}`;
    } catch (reason) { note = ''; error = (reason as Error).message; }
    status();
  }

  async function reload(): Promise<void> {
    if (saving || loading || !persistent) return;
    if (dirty() && !window.confirm(t('重新读取会丢弃未保存的修改，请先复制需要保留的内容。是否继续？', 'Reloading discards unsaved changes; copy anything you want to keep first. Continue?'))) return;
    await load();
  }

  async function changeAccount(target: HTMLFormElement): Promise<void> {
    const value = (name: string) => String(new FormData(target).get(name) ?? '');
    const current = value('current'), next = value('next'), name = value('username').trim(), code = value('code').trim();
    const issue = passwordChangeError(current, next, value('confirm'), name) || (twoFactor && !/^\d{6}$/.test(code) ? t('请输入6位验证码。', 'Enter the 6-digit code.') : '');
    if (issue) { error = issue; status(); return; }
    const button = target.querySelector<HTMLButtonElement>('#account-save')!;
    button.disabled = true; error = ''; note = t('正在验证并保存…', 'Verifying and saving…'); status();
    try {
      await api('auth/password', jsonBody('POST', { currentPassword: current, newPassword: next, ...(name ? { newUsername: name } : {}), ...(twoFactor ? { code } : {}) }));
      window.dispatchEvent(new Event('panel-session-expired'));
    } catch (reason) {
      target.querySelectorAll<HTMLInputElement>('input').forEach(input => { input.value = ''; });
      note = '';
      if (reason instanceof APIError && reason.status === 401) { window.dispatchEvent(new Event('panel-session-expired')); return; }
      error = (reason as Error).message; status();
    } finally { button.disabled = false; }
  }

  // 两步验证：服务器生成密钥，扫码后输入一次验证码确认才启用；停用同样需要验证码。
  async function openTwoFactor(enable: boolean): Promise<void> {
    let setup: { secret: string; uri: string } | null = null;
    if (enable) {
      try { setup = await api<{ secret: string; uri: string }>('auth/2fa/setup', jsonBody('POST', {})); } catch (reason) { error = (reason as Error).message; status(); return; }
    }
    dialog.innerHTML = `<form method="dialog" class="twofactor-form"><div class="dialog-heading"><h2>${enable ? t('启用双重认证', 'Enable two-factor authentication') : t('停用双重认证', 'Disable two-factor authentication')}</h2></div><div class="dialog-body">
      ${setup ? `<p>${t('要设定双重认证，请执行以下步骤：', 'To set up two-factor authentication, perform a few steps:')}</p><p>${t('1. 在认证应用中扫描此二维码，或复制二维码下方的密钥粘贴到应用中', '1. Scan this QR code in your authenticator app, or copy the key below it into the app')}</p>
      <figure class="twofactor-qr"><img src="${connectionQR(setup.uri)}" width="180" height="180" alt="${t('两步验证二维码', 'Two-factor QR code')}"><figcaption><code>${esc(setup.secret)}</code></figcaption></figure><p>${t('2. 输入应用中的验证码', '2. Enter the code from the app')}</p>` : `<p>${t('输入应用中的验证码以移除双重认证。', 'Enter the code from the app to remove two-factor authentication.')}</p>`}
      <input name="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required aria-label="${t('验证码', 'Code')}"><p class="form-error" role="alert" hidden></p></div>
      <footer><button type="button" class="secondary-button" data-twofactor-cancel>${t('取消', 'Cancel')}</button> <button class="primary-button">${t('确认', 'Confirm')}</button></footer></form>`;
    const body = dialog.querySelector<HTMLFormElement>('form')!, alert = dialog.querySelector<HTMLElement>('[role="alert"]')!, input = dialog.querySelector<HTMLInputElement>('[name="code"]')!;
    dialog.querySelector('[data-twofactor-cancel]')!.addEventListener('click', () => dialog.close());
    let busy = false;
    body.addEventListener('submit', event => {
      event.preventDefault();
      if (busy) return;
      const code = input.value.trim();
      if (!/^\d{6}$/.test(code)) { alert.textContent = t('请输入6位验证码。', 'Enter the 6-digit code.'); alert.hidden = false; return; }
      busy = true;
      body.querySelectorAll<HTMLButtonElement | HTMLInputElement>('button, input').forEach(element => { element.disabled = true; });
      void api('auth/2fa/' + (enable ? 'enable' : 'disable'), jsonBody('POST', { code })).then(() => {
        twoFactor = enable; dialog.close();
        error = ''; note = enable ? t('双因素认证已成功建立；下次登录需输入验证码。', 'Two-factor authentication is set up; signing in now needs a code.') : t('双因素认证已成功删除。', 'Two-factor authentication has been removed.');
        paint();
      }).catch(reason => {
        alert.textContent = (reason as Error).message; alert.hidden = false;
        body.querySelectorAll<HTMLButtonElement | HTMLInputElement>('button, input').forEach(element => { element.disabled = false; });
      }).finally(() => { busy = false; });
    });
    dialog.showModal();
    input.focus();
  }

  async function restart(button: HTMLButtonElement): Promise<void> {
    if (saving) return;
    if (dirty() && !window.confirm(t('还有未保存的修改，重启后会丢失。仍要重启面板？', 'You have unsaved changes that will be lost. Restart the panel anyway?'))) return;
    if (!window.confirm(t('确定要重启面板吗？若重启后无法访问面板，请到服务器上查看面板日志。', 'Restart the panel? If you cannot reach it afterwards, check the panel log on the server.'))) return;
    const access = general ? general.access.data ?? general.access.active : null;
    const target = general && access && (general.access.pending || general.tls.pending) ? accessURL(access) : '';
    button.disabled = true; error = ''; note = t('正在重启面板…', 'Restarting the panel…'); status();
    try { await api('panel/restart', jsonBody('POST', {})); } catch (reason) { button.disabled = false; note = ''; error = (reason as Error).message; status(); return; }
    // 访问地址改变时新地址与当前页面不同源，不能轮询，直接前往新地址。
    if (target && target !== `${location.origin}${location.pathname}`) {
      note = `${t('面板正在以新设置重启，即将前往：', 'The panel is restarting with the new settings; opening: ')}${target}`; status();
      setTimeout(() => { location.href = target; }, 4000);
      return;
    }
    const started = Date.now();
    const poll = async (): Promise<void> => {
      try { if ((await fetch(apiURL('auth/status'), { cache: 'no-store' })).ok) { location.reload(); return; } } catch { /* 重启中暂时不可达。 */ }
      if (Date.now() - started < 60000) setTimeout(() => { void poll(); }, 1500);
      else { note = t('面板未在 60 秒内恢复，请到服务器上查看面板日志。', 'The panel did not come back within 60 seconds; check the panel log on the server.'); button.disabled = false; status(); }
    };
    setTimeout(() => { void poll(); }, 2000);
  }

  async function load(): Promise<void> {
    if (loading) return;
    loading = true; error = ''; note = ''; status();
    try {
      const [g, p, s, auth, factor, tg] = await Promise.all([
        api<GeneralStatus>('panel/general'), api<PlanStatus>('system/plan'), api<SubscriptionStatus>('panel/subscription'),
        api<{ username: string }>('auth/status'), api<{ enabled: boolean }>('auth/2fa'), api<TelegramStatus>('panel/telegram'),
      ]);
      general = g; plan = p; subscription = s; username = auth.username; twoFactor = factor.enabled; telegram = tg;
      try { interfaces = (await api<{ interfaces: string[] }>('system/status')).interfaces ?? []; } catch { interfaces = []; }
      baseline = saved(); draft = copy(baseline); reloadRequired = false;
    } catch (reason) { error = `${(reason as Error).message}${t('；点“重新读取”重试。', '; click Reload to retry.')}`; }
    finally { loading = false; paint(); }
  }

  return {
    render() { if (!root.innerHTML) paint(); if (persistent && !general && !loading) void load(); },
    clearPassword() { root.querySelector('#settings-account')?.querySelectorAll<HTMLInputElement>('input').forEach(input => { input.value = ''; }); },
  };
}
