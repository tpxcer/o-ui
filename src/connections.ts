import { api, jsonBody } from './api.ts';
import { mountConnectionCard } from './connection-card.ts';
import { mountSubscription } from './subscriptions.ts';
import { certificateNotice } from './connection-notice.ts';
import { infoTip } from './info-tip.ts';
import { escapeHtml as esc } from './workspace.ts';
import './connections.css';
import { t } from './i18n.ts';

interface ConnectionResult { name: string; preset: string; version: string; workspaceRevision: number; settingsRevision: number; link: string; certificateTrusted?: boolean }

export function initConnections(refreshIcons: () => void): (id: string, name: string, revision: number) => void {
  const dialog = document.createElement('dialog');
  dialog.className = 'connection-dialog'; dialog.setAttribute('aria-labelledby', 'connection-title');
  document.body.append(dialog);
  let returnFocus: HTMLElement | null = null, generation = 0;
  function clear(): void { generation++; dialog.replaceChildren(); }
  dialog.addEventListener('close', () => { clear(); if (returnFocus?.isConnected) returnFocus.focus(); returnFocus = null; });
  window.addEventListener('panel-session-expired', () => { if (dialog.open) dialog.close(); clear(); });
  document.addEventListener('visibilitychange', () => { if (document.hidden && dialog.open) dialog.close(); });
  window.addEventListener('hashchange', () => { if (dialog.open) dialog.close(); });
  window.addEventListener('panel-navigate', () => { if (dialog.open) dialog.close(); });
  return (id, name, revision) => {
    clear(); returnFocus = document.activeElement as HTMLElement;
    dialog.innerHTML = `<header class="dialog-heading"><h2 id="connection-title">${t('客户端连接信息', 'Client connection info')}</h2><button class="icon-button" data-close aria-label="${t('关闭连接信息', 'Close connection info')}"><i data-lucide="x" aria-hidden="true"></i></button></header>
      <div class="connection-body"><div class="connection-security info-tip-host"><strong>${t('含认证凭据，勿公开截图或转发', 'Credentials: do not share or screenshot.')}</strong>${infoTip(t('仅导出当前已应用且正在运行的配置，使用入站的分享主机；未填写时仅回环入站使用监听 IP。分享主机与 SNI 分别保存，外部可达性尚需部署验证；订阅访问范围单独由面板设置决定。关闭弹窗或离开页面后清除显示，不自动复制；剪贴板与已下载的图片不会自动清除。二维码在本机生成，扫码设备必须能访问链接地址；第三方客户端兼容性需实测。', 'Only the applied, running configuration is exported, using the inbound share host (loopback inbounds without one use the listen IP). Share host and SNI are stored separately; external reachability requires deployment testing. Subscription access is controlled in Panel settings. Closing the dialog or leaving the page clears the display; nothing is copied automatically. Clipboard contents and downloaded images are not cleared. QR codes are generated locally; scanning devices must be able to reach the address. Third-party compatibility requires testing.'))}</div>
      <section data-subscription></section><details class="connection-section" open><summary>${t('链接 1', 'Link 1')}</summary><div class="connection-section-body"><p class="form-hint" data-loading role="status">${t('正在读取已生效配置…', 'Reading the active configuration…')}</p><p class="form-error" data-connection-error role="alert" tabindex="-1" hidden></p><button class="secondary-button" data-retry hidden>${t('重新读取连接信息', 'Reload connection info')}</button><div class="connection-card" data-connection hidden></div><div class="connection-details info-tip-host" data-connection-details hidden></div></div></details><section class="subscription-controls" data-sub-actions></section></div><footer><button class="secondary-button" data-close>${t('关闭', 'Close')}</button></footer>`;
    dialog.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => dialog.close()));
    const retry = dialog.querySelector<HTMLButtonElement>('[data-retry]')!;
    const error = dialog.querySelector<HTMLElement>('[data-connection-error]')!;
    const content = dialog.querySelector<HTMLElement>('[data-connection]')!;
    const loading = dialog.querySelector<HTMLElement>('[data-loading]')!;
    const details = dialog.querySelector<HTMLElement>('[data-connection-details]')!;
    const ticket = generation, alive = () => dialog.open && generation === ticket;
    async function load(): Promise<void> {
      retry.hidden = true; loading.hidden = false; error.hidden = true;
      try {
        const result = await api<ConnectionResult>('runtime/connection', jsonBody('POST', { clientId: id, workspaceRevision: revision }));
        if (!alive()) return;
        mountConnectionCard(content, `${name} #1`, result.link, alive);
        details.innerHTML = `${t('节点说明', 'Node details')}${infoTip(`<p>${t('运行版本', 'Running version')} ${esc(result.version)} ${t('· 入站修订', '· inbound revision')} ${result.workspaceRevision} ${t('· 设置修订', '· settings revision')} ${result.settingsRevision}</p>${result.preset === 'hy2' || result.preset === 'xhttp-tls' ? certificateNotice(result.certificateTrusted) : t('<p>REALITY 需要兼容客户端；服务端默认要求 Xray 26.3.27 或更高版本。此处只有公钥，没有服务端私钥。</p>', '<p>REALITY needs a compatible client; by default the server requires Xray 26.3.27 or later. Only the public key is shown here, never the server\'s private key.</p>')}`)}`;
        content.hidden = false; details.hidden = false;
      } catch (reason) {
        if (!alive()) return;
        error.textContent = (reason as Error).message; error.hidden = false; retry.hidden = false;
      } finally { if (alive()) loading.hidden = true; }
    }
    retry.addEventListener('click', () => { void load(); });
    refreshIcons(); dialog.showModal(); dialog.querySelector<HTMLButtonElement>('[data-close]')!.focus();
    mountSubscription(dialog.querySelector<HTMLElement>('[data-subscription]')!, dialog.querySelector<HTMLElement>('[data-sub-actions]')!, id, name, revision, alive);
    void load();
  };
}
