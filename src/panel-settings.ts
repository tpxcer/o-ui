import { api, APIError, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { initPasswordChange } from './password.ts';
import { initPanelAccess } from './panel-access.ts';
import { initServerPlanSettings } from './server-plan.ts';
import { initSubscriptionSettings } from './subscription-settings.ts';
import { lang, languageNames, setLang, t, type Lang } from './i18n.ts';

export interface PanelTLSStatus {
  active: boolean; serverNames: string[]; revision: number; activeRevision: number;
  data: { certificateFile: string; keyFile: string } | null;
}
export function initPanelSettings(root: HTMLElement, persistent: boolean): { render: () => void; clearPassword: () => void } {
  let status: PanelTLSStatus | null = null, loading = false, saving = false, reloadRequired = false, error = '', message = '';
  // 参照OUI：顶部“重启面板”，下方按常规/账号安全/订阅分标签。
  const tabs: [string, string][] = [['general', t('常规', 'General')], ['account', t('账号安全', 'Account security')], ['subscription', t('订阅', 'Subscription')]];
  const top = document.createElement('div'); root.append(top);
  top.innerHTML = `<header class="page-heading"><div><h1 class="page-title" tabindex="-1">${t('面板设置', 'Panel settings')}</h1><p>${t('常规、账号安全与订阅设置', 'General, account security and subscription settings')}</p></div></header>
    ${persistent ? `<div class="card xray-settings-actions panel-restart-bar"><button type="button" class="primary-button" id="panel-restart">${t('重启面板', 'Restart panel')}</button><p class="core-warning">${t('证书和访问设置保存后，需重启面板才生效；重启后 Xray 会自动恢复运行。', 'Certificate and access settings apply after the panel restarts; Xray resumes automatically.')}</p><p class="core-message" id="panel-restart-status" role="status"></p></div>` : ''}`;
  const card = document.createElement('section'); card.className = 'card xray-settings-card'; root.append(card);
  const tablist = document.createElement('div'); card.append(tablist);
  tablist.innerHTML = `<div class="xray-settings-tabs" role="tablist" aria-label="${t('面板设置分类', 'Panel setting categories')}">${tabs.map(([id, label], index) => `<button type="button" role="tab" id="panel-tab-${id}" aria-selected="${index === 0}" data-panel-tab="${id}">${label}</button>`).join('')}</div>`;
  const panels = new Map(tabs.map(([id], index) => {
    const panel = document.createElement('div'); panel.id = `panel-panel-${id}`; panel.role = 'tabpanel'; panel.hidden = index > 0; card.append(panel);
    return [id, panel];
  }));
  const block = (tab: string) => { const element = document.createElement('div'); element.className = 'settings-block'; panels.get(tab)!.append(element); return element; };
  const tlsRoot = block('general');
  let access: ReturnType<typeof initPanelAccess> | null = null;
  if (persistent) access = initPanelAccess(block('general'));
  let plan: ReturnType<typeof initServerPlanSettings> | null = null;
  if (persistent) plan = initServerPlanSettings(block('general'));
  const language = block('general');
  language.innerHTML = `<h2 class="xray-editor-title">${t('界面语言', 'Language')}</h2><label class="form-field">${t('界面语言', 'Language')}<select id="panel-language">${(Object.keys(languageNames) as Lang[]).map(code => `<option value="${code}" ${code === lang ? 'selected' : ''}>${languageNames[code]}</option>`).join('')}</select></label>`;
  language.querySelector<HTMLSelectElement>('#panel-language')?.addEventListener('change', event => setLang((event.target as HTMLSelectElement).value as Lang));
  let password: ReturnType<typeof initPasswordChange> | null = null;
  if (persistent) password = initPasswordChange(block('account'));
  let subscription: ReturnType<typeof initSubscriptionSettings> | null = null;
  if (persistent) subscription = initSubscriptionSettings(block('subscription'));
  tablist.querySelectorAll<HTMLButtonElement>('[data-panel-tab]').forEach(button => button.addEventListener('click', () => {
    tablist.querySelectorAll<HTMLButtonElement>('[data-panel-tab]').forEach(other => other.setAttribute('aria-selected', String(other === button)));
    panels.forEach((panel, id) => { panel.hidden = id !== button.dataset.panelTab; });
    if (button.dataset.panelTab === 'subscription') subscription?.render();
  }));
  top.querySelector<HTMLButtonElement>('#panel-restart')?.addEventListener('click', event => { void restart(event.target as HTMLButtonElement); });
  async function restart(button: HTMLButtonElement): Promise<void> {
    const label = top.querySelector<HTMLElement>('#panel-restart-status')!;
    if (!window.confirm(t('确认重启面板？页面会短暂断开，Xray 会自动恢复运行。', 'Restart the panel? The page disconnects briefly and Xray resumes automatically.'))) return;
    button.disabled = true; label.textContent = t('正在重启面板…', 'Restarting the panel…');
    try { await api('panel/restart', jsonBody('POST', {})); }
    catch (reason) { label.textContent = (reason as Error).message; button.disabled = false; return; }
    const started = Date.now();
    const poll = async (): Promise<void> => {
      try { if ((await fetch('/api/auth/status', { cache: 'no-store' })).ok) { location.reload(); return; } } catch { /* 重启中暂时不可达。 */ }
      if (Date.now() - started < 60000) setTimeout(() => { void poll(); }, 1500);
      else { label.textContent = t('面板未在 60 秒内恢复；如修改了访问地址，请打开新地址。', 'The panel did not come back within 60 seconds; if you changed the access address, open the new one.'); button.disabled = false; }
    };
    setTimeout(() => { void poll(); }, 2000);
  }
  function paint(): void {
    tlsRoot.innerHTML = `<section><h2 class="xray-editor-title">${t('HTTPS 证书', 'HTTPS certificate')}</h2>
      <p class="core-warning">${t('保存后点上方“重启面板”生效。自签证书需在浏览器中信任，访问地址须与证书域名或 IP 一致。', 'Click Restart panel above to apply after saving. Self-signed certificates must be trusted in the browser, and the address must match the certificate domain or IP.')}</p>
      <p class="form-intro">${t('当前连接：', 'Current connection: ')}${location.protocol === 'https:' ? 'HTTPS' : 'HTTP'}${t('。', '. ')}${status?.active ? `${t('已加载面板证书 ·', 'Panel certificate loaded ·')} ${esc(status.serverNames.join(t('、', ', ')) || t('无可用域名', 'no usable names'))}${t('；TLS 入站可复用。', '; TLS inbounds can reuse it.')}` : t('尚无已生效的面板证书，不能自动复用。', 'No active panel certificate; it cannot be reused.')}${status?.data ? status.revision !== status.activeRevision ? t(' 有待重启生效的设置；当前仍用启动时的证书。', ' Settings are waiting for a restart; the certificate loaded at startup is still in use.') : t(' 保存设置已在本次启动加载；文件更新需再次重启。', ' Saved settings were loaded at this startup; file updates need another restart.') : ''}</p>
      ${persistent ? `<form id="panel-tls-form"><label class="form-field" for="panel-cert">${t('证书绝对路径', 'Certificate absolute path')}<input id="panel-cert" name="certificateFile" required maxlength="4096" autocomplete="off" value="${esc(status?.data?.certificateFile ?? '')}" ${!status || saving || loading ? 'disabled' : ''}></label>
        <label class="form-field" for="panel-key">${t('私钥绝对路径', 'Private key absolute path')}<input id="panel-key" name="keyFile" required maxlength="4096" autocomplete="off" value="${esc(status?.data?.keyFile ?? '')}" ${!status || saving || loading ? 'disabled' : ''}></label>
        <p class="form-hint">${t('只填本机 PEM 文件路径，不在浏览器上传或显示私钥。保存会检查配对和有效期；不自动申请证书。设置损坏或重启时文件不可读将拒绝启动，不回退到 HTTP。此设置不包含在业务备份中。', 'Enter local PEM file paths only; the private key is never uploaded or shown in the browser. Saving checks key pairing and validity; certificates are not requested automatically. If the settings are corrupted or the files are unreadable at restart, o-ui refuses to start instead of falling back to HTTP. This setting is not included in backups.')}</p>
        <p class="form-error" role="alert" tabindex="-1" ${error ? '' : 'hidden'}>${esc(error)}</p><p role="status" class="core-message">${esc(loading ? t('正在读取…', 'Reading…') : message)}</p>
        <button id="panel-tls-save" class="primary-button" ${!status || saving || loading || reloadRequired ? 'disabled' : ''}>${saving ? t('正在校验证书…', 'Verifying certificate…') : t('保存证书设置', 'Save certificate settings')}</button></form><button type="button" class="secondary-button" data-reload ${loading || saving ? 'disabled' : ''}>${t('重新读取已保存设置', 'Reload saved settings')}</button>` : t('<p class="form-hint">离线演示不保存证书设置，请使用本机服务。</p>', '<p class="form-hint">The offline demo does not save certificate settings; use the running panel.</p>')}</section>`;
    tlsRoot.querySelector('[data-reload]')?.addEventListener('click', () => { void load(); });
    tlsRoot.querySelector<HTMLFormElement>('#panel-tls-form')?.addEventListener('submit', event => {
      event.preventDefault(); if (!status || saving || loading || reloadRequired) return;
      const form = event.target as HTMLFormElement, data = new FormData(form);
      saving = true; error = ''; message = '';
      updateControls();
      void api<PanelTLSStatus>('panel/tls', jsonBody('PUT', { revision: status.revision, data: { certificateFile: String(data.get('certificateFile')).trim(), keyFile: String(data.get('keyFile')).trim() } })).then(next => {
        status = next; message = t('证书设置已保存，点“重启面板”后生效。', 'Certificate settings saved; click Restart panel to apply.');
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
    if (save) { save.disabled = !status || saving || loading || reloadRequired; save.textContent = saving ? t('正在校验证书…', 'Verifying certificate…') : t('保存证书设置', 'Save certificate settings'); }
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
