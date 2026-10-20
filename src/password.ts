import { api, APIError, jsonBody } from './api.ts';
import { t } from './i18n.ts';

export function passwordChangeError(current: string, next: string, confirm: string): string {
  if (!current) return t('请输入当前密码。', 'Enter the current password.');
  if (next !== confirm) return t('两次输入的新密码不一致。', 'The two new passwords do not match.');
  const length = new TextEncoder().encode(next).length;
  if (length < 12 || length > 72) return t('新密码需为12至72字节，中文字符会占多个字节。', 'The new password must be 12–72 bytes; non-ASCII characters take several bytes.');
  if (current === next) return t('新密码不能与当前密码相同。', 'The new password must differ from the current one.');
  return '';
}

export function initPasswordChange(root: HTMLElement): { clear: () => void } {
  root.innerHTML = `<h2 class="xray-editor-title">${t('修改管理员密码', 'Change administrator password')}</h2><p class="core-warning">${t('仅本机测试。必须验证当前密码；成功后所有旧登录立即失效并返回登录页。不会停止Xray、重置节点凭据或撤销订阅链接。', 'The current password is required. On success all existing sessions end immediately and you return to the sign-in page. Xray keeps running; node credentials and subscription links are not changed.')}</p>
    <p class="form-hint" id="password-hint">${t('新密码需12至72字节，请使用独立的长密码；输入不保存到浏览器存储，切换页面会清空。如请求超时，结果可能已提交，请先尝试新密码登录确认，不要反复重试。', 'The new password must be 12–72 bytes; use a long, unique password. Inputs are not stored in the browser and are cleared when you leave the page. If the request times out it may have succeeded; try signing in with the new password before retrying.')}</p>
    <form id="password-change-form">
      <label class="form-field" for="password-current">${t('当前密码', 'Current password')}<input id="password-current" name="current" type="password" autocomplete="current-password" required maxlength="72" aria-describedby="password-hint"></label>
      <label class="form-field" for="password-next">${t('新密码', 'New password')}<input id="password-next" name="next" type="password" autocomplete="new-password" required maxlength="72" aria-describedby="password-hint"></label>
      <label class="form-field" for="password-confirm">${t('确认新密码', 'Confirm new password')}<input id="password-confirm" name="confirm" type="password" autocomplete="new-password" required maxlength="72" aria-describedby="password-hint"></label>
      <p class="form-error" id="password-error" role="alert" tabindex="-1" hidden></p><p class="core-message" id="password-message" role="status"></p>
      <button type="submit" class="primary-button">${t('修改密码并重新登录', 'Change password and sign in again')}</button> <button type="reset" class="secondary-button">${t('清空输入', 'Clear')}</button>
    </form>`;
  const form = root.querySelector<HTMLFormElement>('form')!;
  const error = root.querySelector<HTMLElement>('#password-error')!;
  const message = root.querySelector<HTMLElement>('#password-message')!;
  let busy = false;
  const clear = () => { form.querySelectorAll<HTMLInputElement>('input').forEach(input => { input.value = ''; }); };
  const showError = (text: string) => { error.textContent = text; error.hidden = false; error.focus(); };
  form.addEventListener('reset', () => { error.hidden = true; error.textContent = ''; message.textContent = ''; });
  form.addEventListener('submit', event => {
    event.preventDefault(); if (busy) return;
    const data = new FormData(form), current = String(data.get('current') ?? ''), next = String(data.get('next') ?? '');
    const issue = passwordChangeError(current, next, String(data.get('confirm') ?? ''));
    if (issue) { showError(issue); return; }
    busy = true; error.hidden = true; error.textContent = ''; message.textContent = t('正在验证并修改密码…', 'Verifying and changing the password…');
    form.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input,button').forEach(element => { element.disabled = true; });
    void api('auth/password', jsonBody('POST', { currentPassword: current, newPassword: next })).then(() => {
      clear(); window.dispatchEvent(new Event('panel-session-expired'));
    }).catch(reason => {
      clear();
      if (reason instanceof APIError && reason.status === 401) { window.dispatchEvent(new Event('panel-session-expired')); return; }
      if (!root.isConnected) return;
      message.textContent = '';
      const uncertain = !(reason instanceof APIError) || reason.status >= 500;
      showError(`${(reason as Error).message}${t('；密码输入已清空。', '; password fields cleared. ')}${uncertain ? t('无法确认修改结果，请先尝试新密码登录确认。', 'The result is uncertain; first try signing in with the new password.') : ''}`);
    }).finally(() => {
      busy = false;
      form.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input,button').forEach(element => { element.disabled = false; });
    });
  });
  return { clear };
}
