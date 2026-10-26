import { api, jsonBody } from './api.ts';
import { connectionQR } from './connection-qr.ts';
import { escapeHtml as esc } from './workspace.ts';
import { mountSubscription } from './subscriptions.ts';
import { certificateNotice } from './connection-notice.ts';
import './connections.css';
import { t } from './i18n.ts';

interface ConnectionResult { name: string; preset: string; version: string; workspaceRevision: number; settingsRevision: number; link: string; outbound: Record<string, unknown>; certificateTrusted?: boolean }

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
      <div class="connection-body"><p class="connection-name">${esc(name)}</p><p class="form-hint">${t('仅导出当前已应用且正在运行的配置，使用入站的分享主机；未填写时仅回环入站使用监听 IP。分享主机与 SNI 分别保存，外部可达性尚需部署验证；订阅访问范围单独由面板设置决定。连接信息含认证凭据，请勿公开截图或转发。关闭弹窗或离开页面后清除显示，不自动复制。', 'Only the applied, running configuration is exported, using the inbound\'s share host (loopback inbounds without one use the listen IP). Share host and SNI are stored separately; external reachability still has to be verified after deployment, and subscription access is controlled in Panel settings. Connection info contains credentials: do not share screenshots or forward it. It is cleared when the dialog closes or you leave the page, and nothing is copied automatically.')}</p>
      <button class="secondary-button" data-reveal>${t('显示连接信息', 'Show connection info')}</button><p class="form-error" role="alert" tabindex="-1" hidden></p><div data-connection hidden></div><p data-notice role="status" class="form-hint"></p><section class="subscription-section" data-subscription aria-labelledby="subscription-title"></section></div><footer><button class="primary-button" data-close>${t('关闭', 'Close')}</button></footer>`;
    dialog.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => dialog.close()));
    const reveal = dialog.querySelector<HTMLButtonElement>('[data-reveal]')!;
    const error = dialog.querySelector<HTMLElement>('.form-error')!;
    const content = dialog.querySelector<HTMLElement>('[data-connection]')!;
    const ticket = generation;
    reveal.addEventListener('click', async () => {
      reveal.disabled = true; reveal.textContent = t('正在读取已生效配置…', 'Reading the active configuration…'); error.hidden = true;
      try {
        const result = await api<ConnectionResult>('runtime/connection', jsonBody('POST', { clientId: id, workspaceRevision: revision }));
        if (!dialog.open || generation !== ticket) return;
        content.innerHTML = `<p class="form-hint">${t('运行版本', 'Running version')} ${esc(result.version)} ${t('· 入站修订', '· inbound revision')} ${result.workspaceRevision} ${t('· 设置修订', '· settings revision')} ${result.settingsRevision}</p>
          ${result.preset === 'hy2' || result.preset === 'xhttp-tls' ? certificateNotice(result.certificateTrusted) : t('<p class="form-hint">REALITY 需要兼容客户端；服务端默认要求 Xray 26.3.27 或更高版本。此处只有公钥，没有服务端私钥。</p>', '<p class="form-hint">REALITY needs a compatible client; by default the server requires Xray 26.3.27 or later. Only the public key is shown here, never the server\'s private key.</p>')}
          <label class="form-field" for="connection-link">${t('分享链接', 'Share link')}<textarea id="connection-link" readonly spellcheck="false" rows="4"></textarea></label><button class="secondary-button" data-copy-link>${t('复制链接', 'Copy link')}</button>
          <figure class="connection-qr"><img alt="${t('含客户端认证信息的本地分享二维码', 'Local share QR code containing client credentials')}"><figcaption>${t('二维码仅在本机生成；扫码设备也必须能访问链接地址。', 'The QR code is generated locally; the scanning device must be able to reach the link address.')}</figcaption></figure>
          <label class="form-field" for="connection-json">${t('官方 Xray 客户端出站 JSON', 'Official Xray client outbound JSON')}<textarea id="connection-json" readonly spellcheck="false" rows="10"></textarea></label><button class="secondary-button" data-copy-json>${t('复制出站 JSON', 'Copy outbound JSON')}</button><p class="form-hint">${t('这是出站片段，不是完整客户端配置。通用链接和二维码的第三方客户端兼容性仍需各客户端实测。', 'This is an outbound snippet, not a complete client config. Third-party compatibility of links and QR codes must be tested per client.')}</p>`;
        content.querySelector<HTMLTextAreaElement>('#connection-link')!.value = result.link;
        const json = JSON.stringify(result.outbound, null, 2);
        content.querySelector<HTMLTextAreaElement>('#connection-json')!.value = json;
        try { content.querySelector<HTMLImageElement>('img')!.src = connectionQR(result.link); }
        catch { content.querySelector('figure')!.textContent = t('链接超出二维码容量，请使用复制链接或出站 JSON。', 'The link is too long for a QR code; copy the link or the outbound JSON instead.'); }
        const copy = async (value: string) => {
          let message = t('无法访问剪贴板，请手动选中复制。', 'Cannot access the clipboard; select and copy manually.');
          try { await navigator.clipboard.writeText(value); message = t('已复制。剪贴板不会随关闭弹窗自动清除，请妥善保管。', 'Copied. The clipboard is not cleared when the dialog closes; keep it safe.'); } catch { /* 保留手动复制提示。 */ }
          if (generation === ticket) dialog.querySelector('[data-notice]')!.textContent = message;
        };
        content.querySelector('[data-copy-link]')!.addEventListener('click', () => { void copy(result.link); });
        content.querySelector('[data-copy-json]')!.addEventListener('click', () => { void copy(json); });
        content.hidden = false; reveal.hidden = true;
        content.querySelector<HTMLTextAreaElement>('textarea')!.focus();
      } catch (reason) {
        if (!dialog.open || generation !== ticket) return;
        error.textContent = (reason as Error).message; error.hidden = false; error.focus(); reveal.disabled = false; reveal.textContent = t('重新读取连接信息', 'Reload connection info');
      }
    });
    refreshIcons(); dialog.showModal(); reveal.focus();
    mountSubscription(dialog.querySelector<HTMLElement>('[data-subscription]')!, id, revision, () => dialog.open && generation === ticket);
  };
}
