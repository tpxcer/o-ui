import { defaultConnection, defaultSniffing } from './workspace.ts';
import type { Connection, Sniffing } from './workspace.ts';
import { t } from './i18n.ts';

// 只提供填写候选，不探测或保证第三方站点在服务器上可用。
export const realityTargets = ['www.amd.com', 'www.microsoft.com', 'www.apple.com', 'www.amazon.com', 'www.tesla.com'] as const;
export function nextRealityTarget(target: string): Pick<Connection, 'target' | 'serverName'> {
  const index = realityTargets.findIndex(host => `${host}:443` === target.trim());
  const host = realityTargets[(index + 1) % realityTargets.length];
  return { target: `${host}:443`, serverName: host };
}
export function realityServerName(target: string): string {
  const match = target.trim().match(/^([a-zA-Z0-9][a-zA-Z0-9.-]*):[0-9]+$/);
  if (!match || /^[0-9.]+$/.test(match[1])) return '';
  const port = Number(target.trim().slice(target.trim().lastIndexOf(':') + 1));
  return port > 0 && port <= 65535 ? match[1] : '';
}
export const newRealityConnection = (): Connection => ({ ...defaultConnection(), ...nextRealityTarget('') });

export const sniffingLabels = [
  ['enabled', t('启用嗅探', 'Enable sniffing')], ['http', 'HTTP'], ['tls', 'TLS'], ['quic', 'QUIC'],
  ['fakeDns', 'FakeDNS'], ['metadataOnly', t('仅元数据', 'Metadata only')], ['routeOnly', t('仅路由', 'Route only')],
] as const;
export function sniffingRules(value: string): string[] {
  const rules = [...new Set(value.split(/[\n,，]/).map(item => item.trim()).filter(Boolean))];
  if (rules.length > 64 || rules.some(item => item.length > 256 || /[\x00-\x1f\x7f]/.test(item))) throw new Error(t('嗅探排除规则每项最多256字符，每组最多64项。', 'Each sniffing exclusion is limited to 256 characters, at most 64 per list.'));
  return rules;
}
export function readSniffing(data: FormData): Sniffing {
  const result = defaultSniffing();
  for (const [key] of sniffingLabels) result[key] = data.has(`sniff-${key}`);
  result.ipsExcluded = sniffingRules(String(data.get('sniff-ips') ?? ''));
  result.domainsExcluded = sniffingRules(String(data.get('sniff-domains') ?? ''));
  return result;
}
