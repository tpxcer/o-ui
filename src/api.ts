import { lang, t } from './i18n.ts';
// 面板可部署在访问路径下（如 /abc/），接口与页面同目录。
export function apiURL(path: string): string {
  return `${(globalThis.location?.pathname ?? '/').replace(/[^/]*$/, '') || '/'}api/${path}`;
}
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  if (!['http:', 'https:'].includes(location.protocol)) throw new Error(t('离线预览不能读取真实数据，请启动本机面板服务。', 'The offline preview cannot read real data; start the local panel.'));
  const headers = new Headers(options.headers);
  headers.set('X-O-UI-Lang', lang);
  let response: Response;
  try { response = await fetch(apiURL(path), { credentials: 'same-origin', ...options, headers, signal: AbortSignal.timeout(path === 'core/install' ? 230000 : 30000) }); }
  catch { throw new Error(t('本机面板未连接或请求超时，请检查服务后重试。', 'The panel is unreachable or the request timed out; check the service and retry.')); }
  if (!(response.headers.get('content-type') ?? '').includes('application/json')) throw new Error(t('本机后端未连接，请先启动面板服务。', 'The panel backend is not connected; start the panel first.'));
  const result = await response.json();
  if (!response.ok) {
    if (response.status === 401 && !path.startsWith('auth/')) window.dispatchEvent(new Event('panel-session-expired'));
    throw new APIError(result.error || t('操作失败，请刷新状态后重试。', 'Operation failed; refresh the status and retry.'), response.status);
  }
  return result as T;
}

// 保存请求断线或报修订冲突时结果不确定：服务器可能已经保存（面板经本机代理访问时，保存会重启 Xray 切断连接，
// 浏览器还可能自动重发同一请求而得到冲突）。连接恢复后读一次，修订号与内容都正好是本次提交才视为已保存。
export const uncertainSave = (reason: unknown) => !(reason instanceof APIError) || reason.status === 409;
export async function confirmSaved<T>(path: string, sent: T, expectedRevision: number, { wait = 30000, interval = 1500 } = {}): Promise<{ revision: number; data: T } | null> {
  const deadline = Date.now() + wait, expected = JSON.stringify(sent);
  for (;;) {
    try {
      const document = await api<{ revision: number; data: T | null }>(path);
      return document.revision === expectedRevision && JSON.stringify(document.data) === expected ? { revision: document.revision, data: document.data as T } : null;
    } catch (reason) {
      if (reason instanceof APIError || Date.now() >= deadline) return null;
      await new Promise(resolve => setTimeout(resolve, interval));
    }
  }
}

export function jsonBody(method: string, value: unknown): RequestInit {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value) };
}
export class APIError extends Error {
  readonly status: number;
  constructor(message: string, status: number) { super(message); this.name = 'APIError'; this.status = status; }
}

