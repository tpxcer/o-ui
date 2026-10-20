import { api, jsonBody } from './api.ts';
import { t } from './i18n.ts';

interface SubscriptionStatus { revision: number; allowed: boolean; active: boolean; path?: string; clash?: boolean; json?: boolean }
type Format = 'clash' | 'json';

export function subscriptionURL(path: string, origin: string): string {
  if (!/^\/sub\/[A-Za-z0-9_-]{43}$/.test(path)) throw new Error(t('订阅链接格式无效，请重新读取状态。', 'Invalid subscription link format; reload the status.'));
  const base = new URL(origin);
  if ((base.protocol !== 'https:' && !(base.protocol === 'http:' && ['127.0.0.1', '[::1]', 'localhost'].includes(base.hostname))) || origin !== base.origin || base.username || base.password) throw new Error(t('订阅链接须使用当前 HTTPS 来源或本机 HTTP 来源，不含路径、凭据或查询。', 'Subscription links must use the current HTTPS origin or the local HTTP origin, without path, credentials or query.'));
  return new URL(path, base.origin).href;
}

// Clash/JSON与通用链接共用同一密钥，只在/sub/后加格式段。
export function formatURL(url: string, format: Format): string {
  return url.replace(/\/sub\/([A-Za-z0-9_-]{43})$/, `/sub/${format}/$1`);
}

export function mountSubscription(root: HTMLElement, clientId: string, workspaceRevision: number, alive: () => boolean): void {
  root.innerHTML = `<h3 id="subscription-title">${t('客户端订阅', 'Client subscription')}</h3><p class="form-hint">${t('每个链接只读取此客户端的一个已应用节点：通用链接为 Base64 分享链接，开启后同一密钥另有 Clash/Mihomo 与 Xray JSON 地址（面板设置→订阅）。无需管理员登录，持有链接者就能获取代理凭据；不要公开或截屏。使用当前页面来源；跨设备访问须先配置非本机 HTTPS、受信任证书与可达网络，本机地址不能直接给手机使用。', 'Each link returns this client\'s single applied node: the universal link is a Base64 share link, and when enabled the same key also serves Clash/Mihomo and Xray JSON addresses (Panel settings → Subscription). No sign-in is needed, so anyone with the link gets the proxy credentials; do not publish or screenshot it. It uses the current page origin; access from other devices requires remote HTTPS, a trusted certificate and a reachable network (a local address cannot be used by a phone).')}</p>
    <p class="form-hint" data-sub-status role="status">${t('正在读取订阅状态…', 'Reading subscription status…')}</p><p class="form-error" data-sub-error role="alert" tabindex="-1" hidden></p>
    <label class="check-field"><input type="checkbox" data-sub-confirm><span>${t('我确认链接持有者可取得此客户端凭据；重置使旧订阅链接失效，撤销不回收已下载的节点。', 'I understand that anyone with the link can get this client\'s credentials; resetting invalidates old links, and revoking does not take back nodes already downloaded.')}</span></label>
    <div class="subscription-actions"><button type="button" class="secondary-button" data-sub-rotate disabled>${t('生成订阅链接', 'Generate subscription link')}</button><button type="button" class="secondary-button" data-sub-revoke disabled>${t('撤销订阅链接', 'Revoke subscription link')}</button><button type="button" class="secondary-button" data-sub-refresh>${t('刷新订阅状态', 'Refresh subscription status')}</button></div>
    <div data-sub-secret hidden><p class="form-hint">${t('新订阅链接（仅本次显示）', 'New subscription links (shown only once)')}</p>
      <label class="form-field" for="subscription-url">${t('通用（Base64：Shadowrocket、v2rayN 等）', 'Universal (Base64: Shadowrocket, v2rayN and others)')}<textarea id="subscription-url" readonly spellcheck="false" rows="3" autocomplete="off"></textarea></label><button type="button" class="secondary-button" data-sub-copy="subscription-url">${t('复制订阅链接', 'Copy subscription link')}</button>
      <div data-sub-format="clash" hidden><label class="form-field" for="subscription-url-clash">Clash / Mihomo<textarea id="subscription-url-clash" readonly spellcheck="false" rows="3" autocomplete="off"></textarea></label><button type="button" class="secondary-button" data-sub-copy="subscription-url-clash">${t('复制 Clash 链接', 'Copy Clash link')}</button></div>
      <div data-sub-format="json" hidden><label class="form-field" for="subscription-url-json">Xray JSON<textarea id="subscription-url-json" readonly spellcheck="false" rows="3" autocomplete="off"></textarea></label><button type="button" class="secondary-button" data-sub-copy="subscription-url-json">${t('复制 JSON 链接', 'Copy JSON link')}</button></div></div>
    <p class="form-hint">${t('完整链接只在生成或重置成功时返回，关闭后无法从数据库取回；请自行安全保存，遗失可重置。关闭订阅选项、删除客户端或恢复业务备份会撤销旧链接。停用客户端并应用，才能停止其代理访问；订阅操作本身不应用 Xray。', 'The full link is returned only when generated or reset and cannot be retrieved later; store it safely and reset it if lost. Disabling the subscription option, deleting the client or restoring a backup revokes old links. To stop proxy access, disable the client and apply; subscription actions do not apply Xray.')}</p><p class="form-hint" data-sub-notice role="status"></p>`;
  let state: SubscriptionStatus | null = null, busy = false, serial = 0;
  const current = (ticket: number) => alive() && root.isConnected && ticket === serial;
  const status = root.querySelector<HTMLElement>('[data-sub-status]')!;
  const error = root.querySelector<HTMLElement>('[data-sub-error]')!;
  const notice = root.querySelector<HTMLElement>('[data-sub-notice]')!;
  const confirm = root.querySelector<HTMLInputElement>('[data-sub-confirm]')!;
  const rotate = root.querySelector<HTMLButtonElement>('[data-sub-rotate]')!;
  const revoke = root.querySelector<HTMLButtonElement>('[data-sub-revoke]')!;
  const refresh = root.querySelector<HTMLButtonElement>('[data-sub-refresh]')!;
  const secret = root.querySelector<HTMLElement>('[data-sub-secret]')!;
  const field = root.querySelector<HTMLTextAreaElement>('#subscription-url')!;
  const formats = (['clash', 'json'] as Format[]).map(format => ({ format, row: root.querySelector<HTMLElement>(`[data-sub-format="${format}"]`)!, field: root.querySelector<HTMLTextAreaElement>(`#subscription-url-${format}`)! }));
  function paint(): void {
    confirm.disabled = busy || !state;
    rotate.disabled = busy || !state?.allowed || !confirm.checked;
    revoke.disabled = busy || !state?.active || !confirm.checked;
    refresh.disabled = busy;
    rotate.textContent = state?.active ? t('重置订阅链接', 'Reset subscription link') : t('生成订阅链接', 'Generate subscription link');
    if (state) status.textContent = `${state.active ? t('已有有效链接；完整地址无法再次读取。', 'A valid link exists; the full address cannot be read again. ') : t('目前没有有效订阅链接。', 'There is no valid subscription link. ')}${!state.allowed ? t('订阅选项未开启，请编辑客户端并保存。', 'Subscriptions are off; edit and save the client to enable them.') : t('读取节点还需配置已应用、核心运行且客户端未受限。', 'Reading the node also requires an applied configuration, a running core and an unrestricted client.')}`;
  }
  function clearSecret(): void { secret.hidden = true; field.value = ''; for (const item of formats) { item.field.value = ''; item.row.hidden = true; } }
  async function request(action: 'status' | 'rotate' | 'revoke'): Promise<void> {
    if (busy || action !== 'status' && !confirm.checked) return;
    const ticket = ++serial;
    busy = true; clearSecret(); error.hidden = true; notice.textContent = ''; paint();
    try {
      const result = await api<SubscriptionStatus>(`subscription/${action}`, jsonBody('POST', { clientId, workspaceRevision, revision: state?.revision ?? 0, confirm: action !== 'status' && confirm.checked }));
      if (!current(ticket)) return;
      state = result; confirm.checked = false;
      if (result.path) {
        field.value = subscriptionURL(result.path, location.origin); secret.hidden = false; field.focus();
        for (const item of formats) if (result[item.format]) { item.field.value = formatURL(field.value, item.format); item.row.hidden = false; }
        notice.textContent = t('新链接已保存，之前的订阅链接已失效。请自行保管；关闭后清除显示，不自动复制。', 'New link saved; previous subscription links are invalid. Keep it safe; it is cleared when closed and not copied automatically.');
      } else if (action === 'revoke') notice.textContent = t('订阅链接已撤销；已导入的代理凭据不受此操作影响。', 'Subscription link revoked; credentials already imported are not affected.');
    } catch (reason) {
      if (!current(ticket)) return;
      if (action !== 'status') state = null;
      error.textContent = `${(reason as Error).message}${action === 'rotate' ? t('若响应中断，服务端可能已生成新链接；刷新状态后可重新确认重置。', 'If the response was interrupted, the server may have generated a new link; refresh the status and confirm the reset again.') : ''}`;
      error.hidden = false; error.focus(); confirm.checked = false;
    } finally { if (current(ticket)) { busy = false; paint(); } }
  }
  confirm.addEventListener('change', paint);
  rotate.addEventListener('click', () => { void request('rotate'); });
  revoke.addEventListener('click', () => { void request('revoke'); });
  refresh.addEventListener('click', () => { void request('status'); });
  root.querySelectorAll<HTMLButtonElement>('[data-sub-copy]').forEach(button => button.addEventListener('click', async () => {
    const ticket = serial, value = root.querySelector<HTMLTextAreaElement>(`#${button.dataset.subCopy}`)?.value ?? '';
    if (!value || secret.hidden) return;
    let message = t('无法访问剪贴板，请手动选中复制。', 'Cannot access the clipboard; select and copy manually.');
    try { await navigator.clipboard.writeText(value); message = t('已复制。关闭弹窗不会清除剪贴板，请妥善保管。', 'Copied. Closing the dialog does not clear the clipboard; keep it safe.'); } catch { /* 保留手动复制说明。 */ }
    if (current(ticket)) notice.textContent = message;
  }));
  void request('status');
}
