import { lang, t } from './i18n.ts';
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  if (!['http:', 'https:'].includes(location.protocol)) throw new Error(t('离线预览不能读取真实数据，请启动本机面板服务。', 'The offline preview cannot read real data; start the local panel.'));
  const headers = new Headers(options.headers);
  headers.set('X-O-UI-Lang', lang);
  let response: Response;
  try { response = await fetch(`/api/${path}`, { credentials: 'same-origin', ...options, headers, signal: AbortSignal.timeout(path === 'core/install' ? 230000 : 30000) }); }
  catch { throw new Error(t('本机面板未连接或请求超时，请检查服务后重试。', 'The panel is unreachable or the request timed out; check the service and retry.')); }
  if (!(response.headers.get('content-type') ?? '').includes('application/json')) throw new Error(t('本机后端未连接，请先启动面板服务。', 'The panel backend is not connected; start the panel first.'));
  const result = await response.json();
  if (!response.ok) {
    if (response.status === 401 && !path.startsWith('auth/')) window.dispatchEvent(new Event('panel-session-expired'));
    throw new APIError(result.error || t('操作失败，请刷新状态后重试。', 'Operation failed; refresh the status and retry.'), response.status);
  }
  return result as T;
}

export function jsonBody(method: string, value: unknown): RequestInit {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value) };
}
export class APIError extends Error {
  readonly status: number;
  constructor(message: string, status: number) { super(message); this.name = 'APIError'; this.status = status; }
}

