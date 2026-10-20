import { t } from './i18n.ts';

// “常规”页的访问/证书/会话模型：表单值与后端 /api/panel/general 文档互相转换并校验，不含DOM。
export interface AccessDraft { public: boolean; listen: string; origin: string; basePath?: string }
export interface TLSDraft { certificateFile: string; keyFile: string }
export interface GeneralStatus {
  access: { revision: number; data: AccessDraft | null; active: AccessDraft; pending: boolean };
  tls: { revision: number; data: TLSDraft | null; active: boolean; serverNames: string[]; pending: boolean };
  session: { revision: number; minutes: number };
}
export interface GeneralForm { listenIP: string; domain: string; port: string; path: string; sessionMinutes: string; certificateFile: string; keyFile: string }
export interface GeneralBody {
  accessRevision: number; tlsRevision: number; sessionRevision: number;
  access: AccessDraft; tls: TLSDraft; sessionMinutes: number; confirmation: string;
}

export const remoteConfirmation = '确认启用HTTPS非本机访问';

function splitListen(listen: string): [string, string] {
  const match = listen.match(/^\[(.+)\]:(\d+)$/) ?? listen.match(/^([^:]+):(\d+)$/);
  return match ? [match[1], match[2]] : ['', ''];
}

export function isLoopback(ip: string): boolean { return /^127\./.test(ip) || ip === '::1'; }

export function generalForm(status: GeneralStatus): GeneralForm {
  const access = status.access.data ?? status.access.active;
  const [ip, port] = splitListen(access.listen);
  let domain = '';
  if (access.public && access.origin) { try { domain = new URL(access.origin).hostname.replace(/^\[|\]$/g, ''); } catch { domain = ''; } }
  return { listenIP: ip === '0.0.0.0' ? '' : ip, domain, port, path: access.basePath || '/', sessionMinutes: String(status.session.minutes), certificateFile: status.tls.data?.certificateFile ?? '', keyFile: status.tls.data?.keyFile ?? '' };
}

// 访问地址：对外为“https://域名:端口/路径/”，仅本机为当前浏览器地址（如经SSH隧道）加路径。
export function accessURL(access: AccessDraft, here = globalThis.location): string {
  const path = access.basePath || '/';
  return access.public ? access.origin + path : `${here?.protocol ?? 'http:'}//${here?.host ?? access.listen}${path}`;
}

// 校验并组装保存请求；监听IP为回环时是仅本机，其余（含留空=0.0.0.0）为固定域名的HTTPS对外访问。
export function generalRequest(form: GeneralForm, status: GeneralStatus): { body: GeneralBody; enablesRemote: boolean } {
  const ip = form.listenIP.trim() || '0.0.0.0';
  if (!/^[0-9a-fA-F.:]+$/.test(ip) || (!ip.includes(':') && !/^\d{1,3}(\.\d{1,3}){3}$/.test(ip))) throw new Error(t('监听IP须为IPv4或IPv6地址；留空监听全部IP', 'Listen IP must be an IPv4 or IPv6 address; leave it empty to listen on all IPs'));
  const portText = form.port.trim(), port = Number(portText);
  if (!/^\d{4,5}$/.test(portText) || port < 1024 || port > 65535) throw new Error(t('端口须为1024至65535', 'Port must be 1024–65535'));
  let path = form.path.trim() || '/';
  if (!path.startsWith('/')) path = '/' + path;
  if (!path.endsWith('/')) path += '/';
  if (path !== '/' && (!/^\/[A-Za-z0-9_-]{1,64}\/$/.test(path) || ['/api/', '/sub/', '/assets/'].includes(path))) throw new Error(t('访问路径须为 / 或 /字母数字下划线短横线/（1–64位），不能是 /api/、/sub/、/assets/', 'The access path must be / or /letters-digits-underscores-hyphens/ (1–64), and cannot be /api/, /sub/ or /assets/'));
  const minutesText = form.sessionMinutes.trim(), minutes = Number(minutesText);
  if (!/^\d+$/.test(minutesText) || minutes < 5 || minutes > 43200) throw new Error(t('会话时长须为5至43200分钟', 'Session duration must be 5–43200 minutes'));
  const certificateFile = form.certificateFile.trim(), keyFile = form.keyFile.trim();
  if (Boolean(certificateFile) !== Boolean(keyFile)) throw new Error(t('证书公钥和私钥路径需同时填写或同时留空', 'Fill in both certificate and key paths, or leave both empty'));
  if ((certificateFile && !certificateFile.startsWith('/')) || (keyFile && !keyFile.startsWith('/'))) throw new Error(t('证书路径须为以 / 开头的绝对路径', 'Certificate paths must be absolute paths starting with /'));
  const local = isLoopback(ip);
  let origin = '';
  if (!local) {
    const domain = form.domain.trim().toLowerCase();
    if (!domain) throw new Error(t('对外访问须填写监听域名（证书中的域名或IP）；仅本机请把监听IP设为127.0.0.1', 'Remote access needs a listen domain (a domain or IP in the certificate); for local only set the listen IP to 127.0.0.1'));
    if (!certificateFile) throw new Error(t('对外访问需要HTTPS证书，请填写证书路径', 'Remote access needs an HTTPS certificate; fill in the certificate paths'));
    origin = `https://${domain.includes(':') ? `[${domain}]` : domain}:${port}`;
  }
  const access: AccessDraft = { public: !local, listen: ip.includes(':') ? `[${ip}]:${port}` : `${ip}:${port}`, origin, ...(path === '/' ? {} : { basePath: path }) };
  const saved = status.access.data ?? status.access.active;
  return {
    body: { accessRevision: status.access.revision, tlsRevision: status.tls.revision, sessionRevision: status.session.revision, access, tls: { certificateFile, keyFile }, sessionMinutes: minutes, confirmation: local ? '' : remoteConfirmation },
    enablesRemote: !local && !saved.public,
  };
}
