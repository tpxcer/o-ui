import { api, APIError, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { t } from './i18n.ts';

interface AccessDraft { public: boolean; listen: string; origin: string }
interface AccessStatus { active: AccessDraft; activeRevision: number; revision: number; data: AccessDraft | null }

export function initPanelAccess(root: HTMLElement): { render: () => void } {
  let status: AccessStatus | null = null, loading = false, saving = false, reloadRequired = false, error = '', message = '';
  const input = (id: string) => root.querySelector<HTMLInputElement>(`#access-${id}`)!;
  function controls(focus = false): void {
    root.querySelectorAll<HTMLInputElement>('input').forEach(element => { element.disabled = !status || loading || saving; });
    const save = root.querySelector<HTMLButtonElement>('#access-save');
    if (save) { save.disabled = !status || loading || saving || reloadRequired; save.textContent = saving ? t('正在校验并保存…', 'Verifying and saving…') : t('保存访问设置（重启后生效）', 'Save access settings (applies after restart)'); }
    const reload = root.querySelector<HTMLButtonElement>('[data-access-reload]');
    if (reload) reload.disabled = loading || saving;
    const alert = root.querySelector<HTMLElement>('[role="alert"]');
    if (alert) { alert.textContent = error; alert.hidden = !error; if (focus && error) alert.focus(); }
    const feedback = root.querySelector<HTMLElement>('[role="status"]');
    if (feedback) feedback.textContent = loading ? t('正在读取…', 'Reading…') : saving ? t('正在校验并保存…', 'Verifying and saving…') : message;
  }
  function paint(): void {
    const draft = status?.data ?? status?.active;
    root.innerHTML = `<h2 class="xray-editor-title">${t('访问范围', 'Access scope')}</h2>
      <p class="core-warning">${t('默认仅本机。非本机 HTTPS 会增加管理员登录与订阅的暴露面；持有订阅链接者可获取代理凭据。仅保存新面板设置，不修改防火墙、VPS 或旧 OUI，不自动重启。', 'Local only by default. Remote HTTPS increases the exposure of administrator sign-in and subscriptions; anyone with a subscription link can get proxy credentials. Only o-ui settings are saved; the firewall is not changed and nothing restarts automatically.')}</p>
      <p class="form-intro">${t('本次启动：', 'This run: ')}${status ? `${status.active.public ? t('固定来源 HTTPS', 'Fixed-origin HTTPS') : t('仅本机', 'Local only')} · ${esc(status.active.listen)}${status.active.origin ? ` · ${esc(status.active.origin)}` : ''}${t('。', '. ')}${status.revision !== status.activeRevision ? t('有待重启生效的访问设置。', 'Access settings are waiting for a restart.') : t('已保存设置与本次启动一致。', 'Saved settings match this run.')}` : t('尚未读取。', 'Not read yet.')}</p>
      <form id="access-form"><label class="check-field"><input id="access-public" type="checkbox" ${draft?.public ? 'checked' : ''}><span>${t('允许固定来源的非本机 HTTPS 管理与订阅', 'Allow remote HTTPS management and subscriptions from a fixed origin')}</span></label>
      <label class="form-field" for="access-listen">${t('监听 IP 与端口', 'Listen IP and port')}<input id="access-listen" required maxlength="100" autocomplete="off" value="${esc(draft?.listen ?? '')}"></label>
      <label class="form-field" for="access-origin">${t('HTTPS 访问来源', 'HTTPS origin')}<input id="access-origin" maxlength="300" autocomplete="off" placeholder="https://panel.example.com:5180" value="${esc(draft?.origin ?? '')}"></label>
      <p class="form-hint">${t('仅本机模式须回环 IP，访问来源留空。非本机模式须先保存有效且匹配来源的证书；使用直接监听的相同端口（1024–65535），不含路径，暂不支持反向代理或端口映射。保存不代表网络可达或证书已获设备信任。', 'Local-only mode needs a loopback IP and an empty origin. Remote mode first needs a saved valid certificate matching the origin, and the same directly listened port (1024–65535) without a path; reverse proxies and port mapping are not supported. Saving does not mean the network is reachable or devices trust the certificate.')}</p>
      <label class="check-field"><input id="access-confirm" type="checkbox"><span>${t('我确认启用 HTTPS 非本机访问的风险（每次保存启用设置均需勾选）。', 'I understand the risks of enabling remote HTTPS access (required on every save that enables it).')}</span></label>
      <p class="form-error" role="alert" tabindex="-1" hidden></p><p class="core-message" role="status"></p>
      <button id="access-save" class="primary-button">${t('保存访问设置（重启后生效）', 'Save access settings (applies after restart)')}</button></form>
      <button type="button" class="secondary-button" data-access-reload>${t('重新读取访问设置', 'Reload access settings')}</button>`;
    controls();
    root.querySelector('#access-form')!.addEventListener('submit', event => {
      event.preventDefault(); if (!status || loading || saving || reloadRequired) return;
      const data: AccessDraft = { public: input('public').checked, listen: input('listen').value.trim(), origin: input('origin').value.trim() };
      if (data.public && !input('confirm').checked) { error = t('请先勾选非本机访问风险确认；当前监听未改变。', 'Tick the remote access risk confirmation first; the listen address is unchanged.'); controls(true); return; }
      saving = true; error = ''; message = ''; controls();
      void api<AccessStatus>('panel/access', jsonBody('PUT', { revision: status.revision, data, confirmation: data.public ? '确认启用HTTPS非本机访问' : '' })).then(next => {
        status = next; message = t('访问设置已保存；当前监听未改变，请手动重启新面板后使用配置地址。', 'Access settings saved; the listen address is unchanged. Restart o-ui manually, then use the configured address.');
      }).catch(reason => {
        reloadRequired = !(reason instanceof APIError) || reason.status === 409 || reason.status >= 500;
        error = `${(reason as Error).message}${t('；输入已保留。', '; your input is kept.')}${reloadRequired ? t('请先复制需要保留的输入，再重新读取访问设置确认结果，不要重复提交旧修订。', ' Copy anything you want to keep, then reload the access settings to confirm the result; do not resubmit the old revision.') : t('请修正后重试。', ' Please correct it and retry.')}`;
      }).finally(() => { saving = false; if (root.isConnected) { if (error) controls(true); else paint(); } });
    });
    root.querySelector('[data-access-reload]')!.addEventListener('click', () => { void load(); });
  }
  async function load(): Promise<void> {
    if (loading || saving) return;
    if (status && !window.confirm(t('重新读取会丢弃未保存的访问设置，请先复制需要保留的内容。不重启面板，不影响证书或密码表单，是否继续？', 'Reloading discards unsaved access settings; copy anything you want to keep first. o-ui does not restart and the certificate and password forms are not affected. Continue?'))) return;
    loading = true; error = ''; message = ''; controls();
    let loaded = false;
    try { status = await api<AccessStatus>('panel/access'); reloadRequired = false; loaded = true; }
    catch (reason) { error = `${(reason as Error).message}${t('；原输入仍保留。', '; your previous input is kept.')}`; }
    finally { loading = false; if (root.isConnected) { if (loaded) paint(); else controls(true); } }
  }
  return { render() { if (!root.innerHTML) paint(); if (!status) void load(); } };
}
