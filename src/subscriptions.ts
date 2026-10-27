import { api, jsonBody } from './api.ts';
import { bindConnectionCopy, connectionCopyButton, mountConnectionCard } from './connection-card.ts';
import { infoTip } from './info-tip.ts';
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

export function mountSubscription(root: HTMLElement, actions: HTMLElement, clientId: string, name: string, workspaceRevision: number, alive: () => boolean): void {
  root.innerHTML = `<details class="connection-section" open><summary>${t('订阅信息', 'Subscription info')}</summary><div class="connection-section-body"><p class="form-hint" data-sub-status role="status">${t('正在读取订阅状态…', 'Reading subscription status…')}</p><div class="connection-card" data-sub-secret hidden></div><div class="subscription-formats" data-sub-formats hidden></div><p class="form-error" data-sub-error role="alert" tabindex="-1" hidden></p></div></details>`;
  actions.innerHTML = `<div class="connection-details info-tip-host">${t('订阅说明', 'Subscription details')}${infoTip(t('通用二维码为 Base64 订阅；Clash/Mihomo 与 Xray JSON 地址共用同一令牌，格式在面板设置中开启。链接使用当前页面来源；跨设备访问需要非本机 HTTPS、受信任证书与可达网络。全局订阅服务开启、配置已应用、核心运行且客户端未受限时才能读取节点。令牌以可还原形式保存在数据库中；数据库或其副本泄露时，订阅地址也会泄露。旧版仅有哈希的链接须手动重置一次，之后重开弹窗即可显示。关闭弹窗只清除显示。重置会使旧订阅链接失效，撤销不会收回已下载的节点；停用客户端并应用才能停止代理访问，订阅操作本身不应用 Xray。关闭订阅选项、删除客户端或恢复业务备份会撤销旧链接；业务导出不包含订阅令牌。', 'The universal QR code is a Base64 subscription. Clash/Mihomo and Xray JSON addresses share the same token; formats are enabled in Panel settings. Links use the current page origin; access from other devices needs remote HTTPS, a trusted certificate and a reachable network. Reading nodes requires the global subscription service to be enabled, an applied configuration, a running core and an unrestricted client. Tokens are stored in recoverable form; leaking the database or its copies also exposes subscription addresses. Legacy hash-only links need one manual reset before they can be displayed on reopening. Closing clears only the display. Resetting invalidates old links; revoking does not take back downloaded nodes. Disable the client and apply to stop proxy access; subscription actions do not apply Xray. Disabling subscriptions, deleting the client or restoring a business backup revokes old links. Business exports exclude subscription tokens.'))}</div>
    <label class="check-field"><input type="checkbox" data-sub-confirm><span>${t('我确认链接持有者可取得此客户端凭据；重置使旧订阅链接失效，撤销不回收已下载的节点。', 'I understand that anyone with the link can get this client\'s credentials; resetting invalidates old links, and revoking does not take back nodes already downloaded.')}</span></label>
    <div class="subscription-actions"><button type="button" class="secondary-button" data-sub-rotate disabled>${t('生成订阅链接', 'Generate subscription link')}</button><button type="button" class="secondary-button" data-sub-revoke disabled>${t('撤销订阅链接', 'Revoke subscription link')}</button><button type="button" class="secondary-button" data-sub-refresh>${t('刷新订阅状态', 'Refresh subscription status')}</button></div><p class="form-hint" data-sub-notice role="status" hidden></p>`;
  let state: SubscriptionStatus | null = null, busy = false, serial = 0;
  const current = (ticket: number) => alive() && root.isConnected && ticket === serial;
  const status = root.querySelector<HTMLElement>('[data-sub-status]')!;
  const error = root.querySelector<HTMLElement>('[data-sub-error]')!;
  const notice = actions.querySelector<HTMLElement>('[data-sub-notice]')!;
  const confirm = actions.querySelector<HTMLInputElement>('[data-sub-confirm]')!;
  const rotate = actions.querySelector<HTMLButtonElement>('[data-sub-rotate]')!;
  const revoke = actions.querySelector<HTMLButtonElement>('[data-sub-revoke]')!;
  const refresh = actions.querySelector<HTMLButtonElement>('[data-sub-refresh]')!;
  const secret = root.querySelector<HTMLElement>('[data-sub-secret]')!;
  const formats = root.querySelector<HTMLElement>('[data-sub-formats]')!;
  function paint(): void {
    confirm.disabled = busy || !state;
    rotate.disabled = busy || !state?.allowed || !confirm.checked;
    revoke.disabled = busy || !state?.active || !confirm.checked;
    refresh.disabled = busy;
    rotate.textContent = state?.active ? t('重置订阅链接', 'Reset subscription link') : t('生成订阅链接', 'Generate subscription link');
    if (state) {
      status.hidden = !secret.hidden;
      status.textContent = !state.allowed ? t('订阅选项未开启，请编辑客户端并保存。', 'Subscriptions are off; edit and save the client to enable them.') : state.active ? t('已有有效链接（完整地址无法再次读取）；重置一次后即可显示二维码。', 'An existing link cannot be recovered. Reset it once to display its QR code.') : t('目前没有有效订阅链接，请在下方确认后生成。', 'No subscription link exists. Confirm below to generate one.');
    }
  }
  function clearSecret(): void { secret.hidden = true; secret.replaceChildren(); formats.hidden = true; formats.replaceChildren(); }
  async function request(action: 'status' | 'rotate' | 'revoke'): Promise<void> {
    if (busy || action !== 'status' && !confirm.checked) return;
    const ticket = ++serial;
    busy = true; clearSecret(); error.hidden = true; notice.hidden = true; notice.textContent = ''; paint();
    status.hidden = false; status.textContent = t('正在读取订阅状态…', 'Reading subscription status…');
    try {
      const result = await api<SubscriptionStatus>(`subscription/${action}`, jsonBody('POST', { clientId, workspaceRevision, revision: state?.revision ?? 0, confirm: action !== 'status' && confirm.checked }));
      if (!current(ticket)) return;
      state = result; confirm.checked = false;
      if (result.path) {
        const url = subscriptionURL(result.path, location.origin);
        mountConnectionCard(secret, `${name} — ${t('订阅信息', 'Subscription info')}`, url, () => current(ticket)); secret.hidden = false;
        for (const format of ['clash', 'json'] as Format[]) if (result[format]) {
          const row = document.createElement('div'); row.className = 'subscription-format'; row.dataset.copyRow = '';
          const label = format === 'clash' ? 'Clash / Mihomo' : 'Xray JSON';
          row.innerHTML = `<span>${label}</span>${connectionCopyButton(`${t('复制订阅链接', 'Copy subscription link')} · ${label}`)}`;
          formats.append(row);
          bindConnectionCopy(row.querySelector<HTMLButtonElement>('button')!, formatURL(url, format), () => current(ticket));
        }
        formats.hidden = !formats.childElementCount;
        if (action === 'rotate') notice.textContent = t('新链接已保存，之前的订阅链接已失效。', 'New link saved; previous subscription links are invalid.');
      } else if (action === 'revoke') notice.textContent = t('订阅链接已撤销；已导入的代理凭据不受此操作影响。', 'Subscription link revoked; credentials already imported are not affected.');
      notice.hidden = !notice.textContent;
    } catch (reason) {
      if (!current(ticket)) return;
      clearSecret(); state = null; status.hidden = true;
      error.textContent = `${(reason as Error).message}${action === 'rotate' ? t('若响应中断，服务端可能已生成新链接；刷新订阅状态即可重新读取。', 'If the response was interrupted, the server may have generated a new link; refresh the subscription status to retrieve it.') : ''}`;
      error.hidden = false; confirm.checked = false;
    } finally { if (current(ticket)) { busy = false; paint(); } }
  }
  confirm.addEventListener('change', paint);
  rotate.addEventListener('click', () => { void request('rotate'); });
  revoke.addEventListener('click', () => { void request('revoke'); });
  refresh.addEventListener('click', () => { void request('status'); });
  void request('status');
}
