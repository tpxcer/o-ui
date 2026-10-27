import { Check, Copy, ImageDown, createElement } from 'lucide';
import { connectionQR } from './connection-qr.ts';
import { escapeHtml as esc } from './workspace.ts';
import { t } from './i18n.ts';

export function connectionCopyButton(label: string): string {
  return `<button type="button" class="icon-button" data-copy title="${esc(label)}" aria-label="${esc(label)}"></button>`;
}

export function bindConnectionCopy(button: HTMLButtonElement, value: string, alive: () => boolean): void {
  const label = button.getAttribute('aria-label')!;
  const icon = (copied: boolean) => {
    button.replaceChildren(createElement(copied ? Check : Copy, { 'aria-hidden': 'true', 'stroke-width': 1.7 }));
    button.classList.toggle('is-copied', copied);
    button.title = copied ? t('已复制', 'Copied') : label;
    button.setAttribute('aria-label', button.title);
  };
  icon(false);
  let serial = 0;
  button.addEventListener('click', async () => {
    const ticket = ++serial;
    const row = button.closest<HTMLElement>('[data-copy-row]')!;
    row.querySelector('[data-copy-fallback]')?.remove();
    try {
      await navigator.clipboard.writeText(value);
      if (!alive() || !button.isConnected || ticket !== serial) return;
      icon(true);
      setTimeout(() => { if (alive() && button.isConnected && ticket === serial) icon(false); }, 2000);
    } catch {
      if (!alive() || !button.isConnected || ticket !== serial) return;
      icon(false);
      const fallback = document.createElement('label');
      fallback.className = 'form-field connection-copy-fallback'; fallback.dataset.copyFallback = '';
      fallback.textContent = t('无法访问剪贴板，请手动复制：', 'Clipboard unavailable; copy manually:');
      const field = document.createElement('textarea');
      field.readOnly = true; field.spellcheck = false; field.rows = 3; field.value = value; field.autocomplete = 'off';
      fallback.append(field); row.append(fallback); field.focus(); field.select();
    }
  });
}

export function mountConnectionCard(root: HTMLElement, title: string, link: string, alive: () => boolean): void {
  root.innerHTML = `<div class="connection-card-heading" data-copy-row><h3>${esc(title)}</h3><div class="connection-card-actions">${connectionCopyButton(t('复制链接', 'Copy link'))}<button type="button" class="icon-button" data-download title="${t('下载二维码图片', 'Download QR image')}" aria-label="${t('下载二维码图片', 'Download QR image')}"></button></div></div><figure class="connection-qr"><img alt="${t('含认证凭据的本地二维码', 'Local QR code containing credentials')}"></figure><p class="form-error" data-qr-error role="alert" hidden></p>`;
  const copy = root.querySelector<HTMLButtonElement>('[data-copy]')!;
  bindConnectionCopy(copy, link, alive);
  const download = root.querySelector<HTMLButtonElement>('[data-download]')!;
  download.replaceChildren(createElement(ImageDown, { 'aria-hidden': 'true', 'stroke-width': 1.7 }));
  const picture = root.querySelector<HTMLImageElement>('img')!;
  const error = root.querySelector<HTMLElement>('[data-qr-error]')!;
  try { picture.src = connectionQR(link); }
  catch {
    root.querySelector('figure')!.hidden = true; download.disabled = true;
    error.textContent = t('链接超出二维码容量，请使用复制链接。', 'The link is too long for a QR code; copy the link instead.'); error.hidden = false;
  }
  download.addEventListener('click', async () => {
    download.disabled = true;
    try {
      await picture.decode();
      if (!alive() || !root.isConnected) return;
      const canvas = document.createElement('canvas');
      canvas.width = picture.naturalWidth; canvas.height = picture.naturalHeight;
      const context = canvas.getContext('2d');
      if (!context) throw new Error();
      context.drawImage(picture, 0, 0);
      const blob = await new Promise<Blob | null>(resolve => canvas.toBlob(resolve, 'image/png'));
      if (!blob) throw new Error();
      if (!alive() || !root.isConnected) return;
      const url = URL.createObjectURL(blob), anchor = document.createElement('a');
      anchor.href = url; anchor.download = 'connection-qr.png'; anchor.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch {
      if (!alive() || !root.isConnected) return;
      error.textContent = t('二维码图片下载失败，请重试。', 'QR image download failed; retry.'); error.hidden = false;
    } finally { if (alive() && root.isConnected) download.disabled = false; }
  });
}
