import { api, APIError, jsonBody } from './api.ts';
import { t } from './i18n.ts';

export function passwordChangeError(current: string, next: string, confirm: string, username = ''): string {
  if (!current) return t('请输入当前密码。', 'Enter the current password.');
  if (username && !/^[a-zA-Z0-9_.-]{3,64}$/.test(username)) return t('用户名需为3至64位字母、数字、点、下划线或短横线。', 'Username must be 3–64 letters, digits, dots, underscores or hyphens.');
  if (!next && !confirm) return username ? '' : t('请填写新用户名或新密码。', 'Enter a new username or a new password.');
  if (next !== confirm) return t('两次输入的新密码不一致。', 'The two new passwords do not match.');
  const length = new TextEncoder().encode(next).length;
  if (length < 6 || length > 72) return t('新密码需为6至72字节，中文字符会占多个字节。', 'The new password must be 6–72 bytes; non-ASCII characters take several bytes.');
  if (current === next) return t('新密码不能与当前密码相同。', 'The new password must differ from the current one.');
  return '';
}

export function initPasswordChange(root: HTMLElement): { clear: () => void } {
  root.innerHTML = `<h2 class="xray-editor-title">${t('修改用户名和密码', 'Change username and password')}</h2><p class="core-warning">${t('需要验证当前密码；成功后所有登录失效，需用新账号重新登录。不影响 Xray、节点和订阅链接。', 'The current password is required. On success every session ends and you sign in again with the new credentials. Xray, nodes and subscription links are not affected.')}</p>
    <p class="form-hint" id="password-hint">${t('新用户名或新密码可只填一项；密码需6至72字节。', 'You can change just the username or just the password; passwords must be 6–72 bytes.')}</p>
    <form id="password-change-form">
      <label class="form-field" for="password-current">${t('当前密码', 'Current password')}<input id="password-current" name="current" type="password" autocomplete="current-password" required maxlength="72" aria-describedby="password-hint"></label>
      <label class="form-field" for="password-username">${t('新用户名（不改可留空）', 'New username (leave empty to keep)')}<input id="password-username" name="username" autocomplete="username" maxlength="64" aria-describedby="password-hint"></label>
      <label class="form-field" for="password-next">${t('新密码', 'New password')}<input id="password-next" name="next" type="password" autocomplete="new-password" maxlength="72" aria-describedby="password-hint"></label>
      <label class="form-field" for="password-confirm">${t('确认新密码', 'Confirm new password')}<input id="password-confirm" name="confirm" type="password" autocomplete="new-password" maxlength="72" aria-describedby="password-hint"></label>
      <p class="form-error" id="password-error" role="alert" tabindex="-1" hidden></p><p class="core-message" id="password-message" role="status"></p>
      <button type="submit" class="primary-button">${t('保存并重新登录', 'Save and sign in again')}</button> <button type="reset" class="secondary-button">${t('清空输入', 'Clear')}</button>
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
    const data = new FormData(form), current = String(data.get('current') ?? ''), next = String(data.get('next') ?? ''), username = String(data.get('username') ?? '').trim();
    const issue = passwordChangeError(current, next, String(data.get('confirm') ?? ''), username);
    if (issue) { showError(issue); return; }
    busy = true; error.hidden = true; error.textContent = ''; message.textContent = t('正在验证并保存…', 'Verifying and saving…');
    form.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input,button').forEach(element => { element.disabled = true; });
    void api('auth/password', jsonBody('POST', { currentPassword: current, newPassword: next, ...(username ? { newUsername: username } : {}) })).then(() => {
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
