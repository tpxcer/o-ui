import { t } from './i18n.ts';
export type ProxyEndpoint = { listen?: string; shareHost?: string };

function ipAddress(value: string): string | null {
  if (/^(?:\d{1,3}\.){3}\d{1,3}$/.test(value)) {
    return value.split('.').every(part => Number(part) <= 255 && String(Number(part)) === part) ? value : null;
  }
  if (!value.includes(':') || /[^0-9a-f:.]/i.test(value)) return null;
  try { return new URL(`http://[${value}]/`).hostname.slice(1, -1); } catch { return null; }
}
function mappedIPv4(ip: string): string {
  const parts = /^::ffff:([0-9a-f]+):([0-9a-f]+)$/.exec(ip);
  if (!parts) return ip;
  const a = parseInt(parts[1], 16), b = parseInt(parts[2], 16);
  return `${a >> 8}.${a & 255}.${b >> 8}.${b & 255}`;
}
export function isLoopbackListen(value = ''): boolean {
  const ip = ipAddress(value || '127.0.0.1');
  return ip !== null && (ip === '::1' || mappedIPv4(ip).startsWith('127.'));
}
function multicast(ip: string): boolean {
  const value = mappedIPv4(ip);
  return value.includes(':') ? value.startsWith('ff') : Number(value.split('.')[0]) >= 224 && Number(value.split('.')[0]) <= 239;
}
export function validateProxyEndpoint(endpoint: ProxyEndpoint): void {
  const ip = ipAddress(endpoint.listen || '127.0.0.1');
  if (!ip || multicast(ip) || mappedIPv4(ip) === '255.255.255.255') throw new Error(t('代理监听地址须为IPv4或IPv6，不含端口、方括号或区域标识。', 'Proxy listen address must be IPv4 or IPv6 without port, brackets or zone.'));
  const host = endpoint.shareHost || '';
  if (!host) {
    if (!isLoopbackListen(endpoint.listen)) throw new Error(t('非回环代理监听必须明确填写分享主机（域名或IP）。', 'A non-loopback proxy listen address requires an explicit share host (domain or IP).'));
    return;
  }
  const address = ipAddress(host);
  const valid = address ? !['0.0.0.0', '::', '255.255.255.255'].includes(mappedIPv4(address)) && !multicast(address)
    : host.length <= 253 && !/[^a-z0-9.-]/i.test(host) && !/^[\d.]+$/.test(host) && host.split('.').every(label => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(label));
  if (!valid) throw new Error(t('分享主机须为有效域名或单播IP，不含协议、端口、路径、凭据或方括号。', 'Share host must be a valid domain or unicast IP without scheme, port, path, credentials or brackets.'));
}
export function readProxyEndpoint(data: FormData): Required<ProxyEndpoint> {
  const endpoint = { listen: String(data.get('listen') ?? '').trim() || '127.0.0.1', shareHost: String(data.get('shareHost') ?? '').trim() };
  validateProxyEndpoint(endpoint);
  return endpoint;
}
