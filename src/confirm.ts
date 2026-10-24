import { createIcons, X } from 'lucide';
import { t } from './i18n.ts';
import './confirm.css';

export type ConfirmOptions = { title?: string; confirmLabel?: string; danger?: boolean };
let active: HTMLDialogElement | null = null;

export function confirmAction(message: string, options: ConfirmOptions = {}): Promise<boolean> {
  if (active) return Promise.resolve(false);
  const dialog = document.createElement('dialog');
  active = dialog;
  const returnFocus = document.activeElement as HTMLElement | null;
  dialog.className = 'confirm-dialog';
  dialog.setAttribute('aria-labelledby', 'confirm-action-title');
  dialog.setAttribute('aria-describedby', 'confirm-action-message');
  dialog.innerHTML = `<header class="dialog-heading"><h2 id="confirm-action-title"></h2><button type="button" class="icon-button" data-confirm-close aria-label="${t('关闭弹窗', 'Close dialog')}"><i data-lucide="x" aria-hidden="true"></i></button></header><div class="dialog-body"><p id="confirm-action-message"></p></div><footer><button type="button" class="secondary-button" data-confirm-cancel autofocus>${t('取消', 'Cancel')}</button><button type="button" class="${options.danger ? 'danger-button is-solid' : 'primary-button'}" data-confirm-accept></button></footer>`;
  dialog.querySelector('#confirm-action-title')!.textContent = options.title ?? t('确认操作', 'Confirm action');
  dialog.querySelector('#confirm-action-message')!.textContent = message;
  dialog.querySelector('[data-confirm-accept]')!.textContent = options.confirmLabel ?? t('确认', 'Confirm');
  document.body.append(dialog);
  createIcons({ icons: { X }, root: dialog });
  return new Promise(resolve => {
    let settled = false;
    const finish = (answer: boolean) => {
      if (settled) return;
      settled = true;
      window.removeEventListener('panel-session-expired', expired);
      if (dialog.open) dialog.close();
      dialog.remove();
      active = null;
      if (returnFocus?.isConnected && returnFocus.getClientRects().length) returnFocus.focus();
      resolve(answer);
    };
    const expired = () => finish(false);
    dialog.querySelector('[data-confirm-accept]')!.addEventListener('click', () => finish(true));
    dialog.querySelector('[data-confirm-cancel]')!.addEventListener('click', () => finish(false));
    dialog.querySelector('[data-confirm-close]')!.addEventListener('click', () => finish(false));
    dialog.addEventListener('cancel', event => { event.preventDefault(); finish(false); });
    dialog.addEventListener('close', () => finish(false));
    window.addEventListener('panel-session-expired', expired);
    dialog.showModal();
    dialog.querySelector<HTMLButtonElement>('[data-confirm-cancel]')!.focus();
  });
}
