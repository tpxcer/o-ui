import { api, APIError, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { t } from './i18n.ts';

interface SubscriptionSettings { enabled: boolean; updateHours: number; title: string; showInfo: boolean }
interface SubscriptionStatus { revision: number; data: SubscriptionSettings }

// 订阅全局设置（参照OUI订阅页）；单个客户端的订阅链接仍在连接信息中生成。
export function initSubscriptionSettings(root: HTMLElement): { render: () => void } {
  let status: SubscriptionStatus | null = null, loading = false, saving = false, error = '', message = '';
  function paint(): void {
    const s = status?.data;
    const disabled = !status || loading || saving ? 'disabled' : '';
    root.innerHTML = `<h2 class="xray-editor-title">${t('订阅设置', 'Subscription settings')}</h2>
      <p class="form-intro">${t('订阅链接在客户端的连接信息中生成，地址为', 'Subscription links are generated in a client\'s connection info, at')} <code>${esc(location.origin)}/sub/…</code>${t('。订阅链接含代理凭据，请勿公开。', '. Subscription links contain proxy credentials; keep them private.')}</p>
      <form id="subscription-settings-form">
        <label class="check-field"><input name="enabled" type="checkbox" ${s?.enabled ? 'checked' : ''} ${disabled}><span>${t('启用订阅服务（关闭后所有订阅链接暂停返回内容）', 'Enable the subscription service (when off, every subscription link stops returning content)')}</span></label>
        <label class="form-field">${t('客户端自动更新间隔（小时）', 'Client update interval (hours)')}<input name="updateHours" type="number" min="1" max="168" step="1" required value="${s?.updateHours ?? 12}" ${disabled}></label>
        <label class="form-field">${t('订阅名称（显示在客户端）', 'Subscription title (shown in clients)')}<input name="title" maxlength="64" autocomplete="off" value="${esc(s?.title ?? '')}" ${disabled}></label>
        <label class="check-field"><input name="showInfo" type="checkbox" ${s?.showInfo ? 'checked' : ''} ${disabled}><span>${t('在客户端显示已用流量、总额度和到期时间', 'Show used traffic, quota and expiry in clients')}</span></label>
        <p class="form-error" role="alert" tabindex="-1" ${error ? '' : 'hidden'}>${esc(error)}</p><p class="core-message" role="status">${esc(loading ? t('正在读取…', 'Reading…') : message)}</p>
        <button class="primary-button" ${disabled}>${saving ? t('正在保存…', 'Saving…') : t('保存订阅设置', 'Save subscription settings')}</button>
      </form>`;
    root.querySelector<HTMLFormElement>('#subscription-settings-form')!.addEventListener('submit', event => {
      event.preventDefault(); if (!status || loading || saving) return;
      const form = event.target as HTMLFormElement, data = new FormData(form);
      const next: SubscriptionSettings = { enabled: data.has('enabled'), updateHours: Number(data.get('updateHours')), title: String(data.get('title') ?? '').trim(), showInfo: data.has('showInfo') };
      saving = true; error = ''; message = ''; paint();
      void api<SubscriptionStatus>('panel/subscription', jsonBody('PUT', { revision: status.revision, data: next })).then(saved => {
        status = saved; message = t('订阅设置已保存，立即生效。', 'Subscription settings saved and applied.');
      }).catch(reason => {
        error = (reason as Error).message;
        if (reason instanceof APIError && reason.status === 409) { error += t('；设置已在别处修改，请刷新页面。', '; the settings changed elsewhere, refresh the page.'); }
      }).finally(() => { saving = false; if (root.isConnected) paint(); });
    });
  }
  async function load(): Promise<void> {
    if (loading) return;
    loading = true; paint();
    try { status = await api<SubscriptionStatus>('panel/subscription'); error = ''; }
    catch (reason) { error = (reason as Error).message; }
    finally { loading = false; paint(); }
  }
  return { render() { if (!root.innerHTML) paint(); if (!status) void load(); } };
}
