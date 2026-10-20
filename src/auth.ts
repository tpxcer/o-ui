import { api, APIError, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import './auth.css';
import { languageButton, t } from './i18n.ts';

interface AuthStatus { initialized: boolean; authenticated: boolean; username: string; twoFactor?: boolean }

export function requireLogin(): Promise<boolean> {
  // 离线文件只保留明确标识的界面演示，不绕过后端认证。
  if (location.protocol === 'file:') return Promise.resolve(false);
  return new Promise(resolve => {
    const root = document.createElement('section');
    root.className = 'auth-screen'; root.setAttribute('aria-label', t('管理员登录', 'Administrator sign-in'));
    document.body.append(root);
    let status: AuthStatus | null = null;
    let error = '', busy = false, disposed = false;
    function paint(): void {
      const setup = status && !status.initialized;
      document.title = `o-ui · ${status ? setup ? t('设置管理员', 'Set up administrator') : t('管理员登录', 'Administrator sign-in') : t('连接本机面板', 'Connect to the panel')}`;
      root.innerHTML = `<div class="card auth-card"><div class="auth-heading">${languageButton()}<span>o-ui</span><h1>${status ? setup ? t('设置管理员', 'Set up administrator') : t('管理员登录', 'Administrator sign-in') : t('连接本机面板', 'Connect to the panel')}</h1><p>${setup ? t('首次使用请设置账号密码。没有默认密码，不导入旧 OUI 数据。', 'Set a username and password on first use. There is no default password.') : t('仅限本机测试，不接管旧 OUI，不连接 VPS。', 'Signing in to this panel.')}</p></div>
        ${status ? `<form id="auth-form"><label class="form-field" for="auth-username">${t('管理员名称', 'Administrator name')}<input id="auth-username" name="username" autocomplete="username" required minlength="3" maxlength="64" pattern="[a-zA-Z0-9_.\\-]+" ${busy ? 'disabled' : ''}></label>
        <label class="form-field" for="auth-password">${t('密码', 'Password')}<input id="auth-password" name="password" type="password" autocomplete="${setup ? 'new-password' : 'current-password'}" required maxlength="72" ${busy ? 'disabled' : ''}></label>
        ${!setup && status.twoFactor ? `<label class="form-field" for="auth-code">${t('验证码', 'Code')}<input id="auth-code" name="code" inputmode="numeric" autocomplete="one-time-code" required minlength="6" maxlength="6" pattern="[0-9]{6}" ${busy ? 'disabled' : ''}></label>` : ''}
        ${setup ? t('<p class="form-hint">密码需6至72字节；管理员名称需3至64位字母、数字、点、下划线或短横线。</p><label class="form-field" for="auth-confirm">确认密码<input id="auth-confirm" name="confirm" type="password" autocomplete="new-password" required maxlength="72"></label>', '<p class="form-hint">The password must be 6–72 bytes. The administrator name must be 3–64 letters, digits, dots, underscores or hyphens.</p><label class="form-field" for="auth-confirm">Confirm password<input id="auth-confirm" name="confirm" type="password" autocomplete="new-password" required maxlength="72"></label>') : ''}
        <label class="check-field"><input id="auth-visible" type="checkbox"><span>${t('显示密码', 'Show password')}</span></label><p class="form-error" id="auth-error" role="alert" tabindex="-1" ${error ? '' : 'hidden'}>${esc(error)}</p>
        <button class="primary-button" type="submit" ${busy ? 'disabled' : ''}>${busy ? t('正在验证…', 'Verifying…') : setup ? t('设置并登录', 'Set up and sign in') : t('登录', 'Sign in')}</button></form>` : `<p class="form-error" role="alert">${esc(error || t('正在读取登录状态…', 'Reading sign-in status…'))}</p>${error ? t('<button class="secondary-button" id="auth-retry">重新连接</button>', '<button class="secondary-button" id="auth-retry">Reconnect</button>') : ''}`}
        <p class="auth-boundary">${t('本地开发版 · 请勿通过端口转发或反向代理暴露到公网', 'Do not expose this panel through port forwarding or a reverse proxy')}</p></div>`;
      root.querySelector('#auth-retry')?.addEventListener('click', () => { void load(); });
      root.querySelector('#auth-visible')?.addEventListener('change', event => {
        root.querySelectorAll<HTMLInputElement>('[name="password"], [name="confirm"]').forEach(input => { input.type = (event.target as HTMLInputElement).checked ? 'text' : 'password'; });
      });
      root.querySelector<HTMLFormElement>('#auth-form')?.addEventListener('submit', event => {
        event.preventDefault(); if (busy) return;
        const form = event.target as HTMLFormElement;
        const data = new FormData(form), password = String(data.get('password') ?? '');
        if (setup && (password !== data.get('confirm') || new TextEncoder().encode(password).length < 6 || new TextEncoder().encode(password).length > 72)) {
          const message = root.querySelector<HTMLElement>('#auth-error')!;
          message.textContent = password !== data.get('confirm') ? t('两次输入的密码不一致。', 'The two passwords do not match.') : t('密码长度应为6至72字节，中文字符会占多个字节。', 'The password must be 6–72 bytes; non-ASCII characters take several bytes.'); message.hidden = false; message.focus(); return;
        }
        busy = true;
        form.querySelector<HTMLButtonElement>('[type="submit"]')!.disabled = true;
        form.querySelector<HTMLButtonElement>('[type="submit"]')!.textContent = t('正在验证…', 'Verifying…');
        form.querySelectorAll<HTMLInputElement>('input').forEach(input => { input.disabled = true; });
        const code = String(data.get('code') ?? '').trim();
        void api('auth/' + (setup ? 'setup' : 'login'), jsonBody('POST', { username: data.get('username'), password, ...(code ? { code } : {}) })).then(() => {
          disposed = true; root.remove(); resolve(true);
        }).catch(reason => { error = (reason as Error).message; if (setup && reason instanceof APIError && reason.status === 409 && status) status.initialized = true; busy = false; paint(); root.querySelector<HTMLElement>('#auth-error')?.focus(); });
      });
    }
    async function load(): Promise<void> {
      error = ''; paint();
      try {
        status = await api<AuthStatus>('auth/status');
        if (status.authenticated) { disposed = true; root.remove(); resolve(true); return; }
      } catch (reason) { error = (reason as Error).message; }
      if (!disposed) { paint(); root.querySelector<HTMLInputElement>('#auth-username')?.focus(); }
    }
    void load();
  });
}
