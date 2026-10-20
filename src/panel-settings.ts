import { api, APIError, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { initPasswordChange } from './password.ts';
import { initPanelAccess } from './panel-access.ts';
import { initServerPlanSettings } from './server-plan.ts';
import { t } from './i18n.ts';

export interface PanelTLSStatus {
  active: boolean; serverNames: string[]; revision: number; activeRevision: number;
  data: { certificateFile: string; keyFile: string } | null;
}
export function initPanelSettings(root: HTMLElement, persistent: boolean): { render: () => void; clearPassword: () => void } {
  let status: PanelTLSStatus | null = null, loading = false, saving = false, reloadRequired = false, error = '', message = '';
  const tlsRoot = document.createElement('div'); root.append(tlsRoot);
  let access: ReturnType<typeof initPanelAccess> | null = null;
  if (persistent) { const section = document.createElement('section'); section.className = 'card xray-settings-card'; root.append(section); access = initPanelAccess(section); }
  let password: ReturnType<typeof initPasswordChange> | null = null;
  if (persistent) { const section = document.createElement('section'); section.className = 'card xray-settings-card'; root.append(section); password = initPasswordChange(section); }
  let plan: ReturnType<typeof initServerPlanSettings> | null = null;
  if (persistent) { const section = document.createElement('section'); section.className = 'card xray-settings-card'; root.append(section); plan = initServerPlanSettings(section); }
  function paint(): void {
    tlsRoot.innerHTML = `<header class="page-heading"><div><h1 class="page-title">${t('面板设置', 'Panel settings')}</h1><p>${t('新面板 HTTPS、管理员安全与服务器套餐', 'HTTPS, administrator security and server plan')}</p></div></header>
      <section class="card xray-settings-card"><h2 class="xray-editor-title">${t('HTTPS 证书', 'HTTPS certificate')}</h2>
      <p class="core-warning">${t('证书设置本身不改变访问范围、不读取旧 OUI。保存后需手动重启新面板才生效；不会重启当前页面或核心。自签证书需自行信任，访问地址须匹配证书域名或 IP。', 'Certificate settings do not change the access scope. They take effect after you restart o-ui manually; the current page and core are not restarted. Self-signed certificates must be trusted by you, and the address must match the certificate\'s domain or IP.')}</p>
      <p class="form-intro">${t('当前连接：', 'Current connection: ')}${location.protocol === 'https:' ? 'HTTPS' : 'HTTP'}${t('。', '. ')}${status?.active ? `${t('已加载面板证书 ·', 'Panel certificate loaded ·')} ${esc(status.serverNames.join(t('、', ', ')) || t('无可用域名', 'no usable names'))}${t('；TLS 入站可复用。', '; TLS inbounds can reuse it.')}` : t('尚无已生效的面板证书，不能自动复用。', 'No active panel certificate; it cannot be reused.')}${status?.data ? status.revision !== status.activeRevision ? t(' 有待重启生效的设置；当前仍用启动时的证书。', ' Settings are waiting for a restart; the certificate loaded at startup is still in use.') : t(' 保存设置已在本次启动加载；文件更新需再次重启。', ' Saved settings were loaded at this startup; file updates need another restart.') : ''}</p>
      ${persistent ? `<form id="panel-tls-form"><label class="form-field" for="panel-cert">${t('证书绝对路径', 'Certificate absolute path')}<input id="panel-cert" name="certificateFile" required maxlength="4096" autocomplete="off" value="${esc(status?.data?.certificateFile ?? '')}" ${!status || saving || loading ? 'disabled' : ''}></label>
        <label class="form-field" for="panel-key">${t('私钥绝对路径', 'Private key absolute path')}<input id="panel-key" name="keyFile" required maxlength="4096" autocomplete="off" value="${esc(status?.data?.keyFile ?? '')}" ${!status || saving || loading ? 'disabled' : ''}></label>
        <p class="form-hint">${t('只填本机 PEM 文件路径，不在浏览器上传或显示私钥。保存会检查配对和有效期；不自动申请证书。设置损坏或重启时文件不可读将拒绝启动，不回退到 HTTP。此设置不包含在业务备份中。', 'Enter local PEM file paths only; the private key is never uploaded or shown in the browser. Saving checks key pairing and validity; certificates are not requested automatically. If the settings are corrupted or the files are unreadable at restart, o-ui refuses to start instead of falling back to HTTP. This setting is not included in backups.')}</p>
        <p class="form-error" role="alert" tabindex="-1" ${error ? '' : 'hidden'}>${esc(error)}</p><p role="status" class="core-message">${esc(loading ? t('正在读取…', 'Reading…') : message)}</p>
        <button id="panel-tls-save" class="primary-button" ${!status || saving || loading || reloadRequired ? 'disabled' : ''}>${saving ? t('正在校验证书…', 'Verifying certificate…') : t('保存 HTTPS 设置（重启后生效）', 'Save HTTPS settings (applies after restart)')}</button></form><button type="button" class="secondary-button" data-reload ${loading || saving ? 'disabled' : ''}>${t('重新读取已保存设置', 'Reload saved settings')}</button>` : t('<p class="form-hint">离线演示不保存证书设置，请使用本机服务。</p>', '<p class="form-hint">The offline demo does not save certificate settings; use the running panel.</p>')}</section>`;
    tlsRoot.querySelector('[data-reload]')?.addEventListener('click', () => { void load(); });
    tlsRoot.querySelector<HTMLFormElement>('#panel-tls-form')?.addEventListener('submit', event => {
      event.preventDefault(); if (!status || saving || loading || reloadRequired) return;
      const form = event.target as HTMLFormElement, data = new FormData(form);
      saving = true; error = ''; message = '';
      updateControls();
      void api<PanelTLSStatus>('panel/tls', jsonBody('PUT', { revision: status.revision, data: { certificateFile: String(data.get('certificateFile')).trim(), keyFile: String(data.get('keyFile')).trim() } })).then(next => {
        status = next; message = t('证书设置已保存；当前连接未切换，请在方便时手动重启新面板。', 'Certificate settings saved; the current connection is unchanged. Restart o-ui when convenient.');
      }).catch(reason => {
        reloadRequired = !(reason instanceof APIError) || reason.status === 409 || reason.status >= 500;
        error = `${(reason as Error).message}${t('；路径输入已保留。', '; the paths you entered are kept.')}${reloadRequired ? t('请先复制需要保留的路径，再重新读取已保存设置以确认结果，不要重复提交旧修订。', ' Copy any paths you want to keep, then reload the saved settings to confirm the result; do not resubmit the old revision.') : t('请修正后重试。', ' Please correct it and retry.')}`;
      }).finally(() => {
        saving = false;
        if (root.isConnected) { if (error) updateControls(true); else paint(); }
      });
    });
  }
  function updateControls(focusError = false): void {
    tlsRoot.querySelectorAll<HTMLInputElement>('input').forEach(element => { element.disabled = !status || saving || loading; });
    const save = tlsRoot.querySelector<HTMLButtonElement>('#panel-tls-save');
    if (save) { save.disabled = !status || saving || loading || reloadRequired; save.textContent = saving ? t('正在校验证书…', 'Verifying certificate…') : t('保存 HTTPS 设置（重启后生效）', 'Save HTTPS settings (applies after restart)'); }
    const reload = tlsRoot.querySelector<HTMLButtonElement>('[data-reload]');
    if (reload) reload.disabled = loading || saving;
    const label = tlsRoot.querySelector<HTMLElement>('[role="alert"]');
    if (label) { label.textContent = error; label.hidden = !error; if (error && focusError) label.focus(); }
    const feedback = tlsRoot.querySelector<HTMLElement>('[role="status"]');
    if (feedback) feedback.textContent = loading ? t('正在读取…', 'Reading…') : saving ? t('正在校验证书…', 'Verifying certificate…') : message;
  }
  async function load(): Promise<void> {
    if (loading || saving) return;
    const reloading = status !== null;
    if (reloading && !window.confirm(t('重新读取会丢弃本页未保存的证书和私钥路径，请先复制需要保留的内容。仅读取已保存设置，不会重启面板或Xray，也不会清空密码表单。是否继续？', 'Reloading discards unsaved certificate and key paths on this page; copy anything you want to keep first. Only saved settings are read; o-ui and Xray are not restarted and the password form is not cleared. Continue?'))) return;
    loading = true; error = ''; message = ''; updateControls();
    let loaded = false;
    try {
      const next = await api<PanelTLSStatus>('panel/tls');
      status = next; reloadRequired = false; loaded = true;
      message = reloading ? t('已重新读取已保存设置；未重启面板或Xray。', 'Saved settings reloaded; o-ui and Xray were not restarted.') : '';
    } catch (reason) { error = `${(reason as Error).message}${reloading ? t('；原路径输入仍保留。', '; the paths you entered are kept.') : ''}`; }
    finally { loading = false; if (root.isConnected) { if (loaded) paint(); else updateControls(true); } }
  }
  return { render() { if (!tlsRoot.innerHTML) paint(); if (persistent && !status) void load(); access?.render(); plan?.render(); }, clearPassword() { password?.clear(); } };
}
