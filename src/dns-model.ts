import { t } from './i18n.ts';
export type DNSFields = { servers: string; queryStrategy: string; disableCache: string; disableFallback: string };
export const dnsStrategies = ['', 'UseIP', 'UseIPv4', 'UseIPv6'];
const keys = ['servers', 'queryStrategy', 'disableCache', 'disableFallback'];

function ip(value: string): boolean {
  if (value.includes(':') && /^[a-fA-F0-9:.]+$/.test(value)) {
    try { new URL(`http://[${value}]/`); return true; } catch { return false; }
  }
  return value.split('.').length === 4 && value.split('.').every(part => /^(0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255);
}
function server(value: string): boolean {
  if (!value || value.length > 2048 || /[\s\x00-\x1f\x7f]/.test(value)) return false;
  if (value === 'localhost' || ip(value)) return true;
  try {
    const url = new URL(value);
    if (!['tcp:', 'tcp+local:', 'https:', 'https+local:'].includes(url.protocol) || !url.hostname || url.username || url.password || value.includes('?') || value.includes('#') || (url.port && (!/^\d+$/.test(url.port) || Number(url.port) < 1 || Number(url.port) > 65535))) return false;
    const host = url.hostname.replace(/^\[|\]$/g, '');
    if (url.protocol.startsWith('tcp')) return ip(host) && !url.pathname;
    return ip(host) || host.length <= 253 && host.split('.').every(part => /^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/.test(part));
  } catch { return false; }
}

export function buildDNS(fields: DNSFields): string {
  const servers = fields.servers.split(/\r?\n/).map(value => value.trim()).filter(Boolean);
  if (!servers.length || servers.length > 64 || !servers.every(server)) throw new Error(t('DNS服务器请每行填写localhost、IPv4/IPv6、tcp://IP:端口、tcp+local://IP:端口或无账号/查询参数的HTTPS地址，最多64项；其它写法请用JSON。', 'Enter one DNS server per line: localhost, IPv4/IPv6, tcp://IP:port, tcp+local://IP:port, or an HTTPS address without credentials/query, up to 64; use JSON for anything else.'));
  if (!dnsStrategies.includes(fields.queryStrategy)) throw new Error(t('DNS查询策略仅支持核心默认、UseIP、UseIPv4或UseIPv6。', 'DNS query strategy supports only the core default, UseIP, UseIPv4 or UseIPv6.'));
  const value: Record<string, unknown> = { servers };
  if (fields.queryStrategy) value.queryStrategy = fields.queryStrategy;
  for (const key of ['disableCache', 'disableFallback'] as const) {
    if (!['', 'true', 'false'].includes(fields[key])) throw new Error(t('DNS缓存与回退选项无效。', 'Invalid DNS cache and fallback options.'));
    if (fields[key]) value[key] = fields[key] === 'true';
  }
  return JSON.stringify(value, null, 2);
}

// 未知字段、对象服务器及重复键均留给JSON，不以表单静默改写。
export function dnsFields(text: string): DNSFields | null {
  let value: Record<string, unknown>;
  try { value = JSON.parse(text); } catch { return null; }
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).some(key => !keys.includes(key)) || !Array.isArray(value.servers) || !value.servers.every(item => typeof item === 'string' && item.trim() === item && !/[\r\n]/.test(item))) return null;
  let depth = 0, quoted = false, escaped = false, properties = 0;
  for (let index = 0; index < text.length; index++) {
    const char = text[index];
    if (quoted) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === '"') { quoted = false; if (depth === 1 && text.slice(index + 1).trimStart().startsWith(':')) properties++; }
    } else if (char === '"') quoted = true;
    else if (char === '{' || char === '[') depth++;
    else if (char === '}' || char === ']') depth--;
  }
  if (properties !== Object.keys(value).length) return null;
  const fields: DNSFields = { servers: value.servers.join('\n'), queryStrategy: '', disableCache: '', disableFallback: '' };
  if ('queryStrategy' in value) {
    if (typeof value.queryStrategy !== 'string' || !value.queryStrategy) return null;
    fields.queryStrategy = value.queryStrategy;
  }
  for (const key of ['disableCache', 'disableFallback'] as const) {
    if (key in value && typeof value[key] !== 'boolean') return null;
    if (key in value) fields[key] = String(value[key]);
  }
  try { buildDNS(fields); return fields; } catch { return null; }
}
