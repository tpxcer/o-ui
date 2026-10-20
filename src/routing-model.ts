import { buildSettings } from './xray-settings-model.ts';
import type { SettingsDraft } from './xray-settings-model.ts';
import { t } from './i18n.ts';

export type RoutingRule = Record<string, unknown>;
export type RouteFields = { domain: string; ip: string; port: string; network: string; protocol: string; target: string };
export type RouteTarget = { value: string; label: string };
export const emptyRoute = (): RouteFields => ({ domain: '', ip: '', port: '', network: '', protocol: '', target: '' });
const object = (value: unknown): value is RoutingRule => !!value && typeof value === 'object' && !Array.isArray(value);

export function routingRules(text: string): RoutingRule[] {
  let value: unknown;
  try { value = JSON.parse(text); } catch { throw new Error(t('路由规则不是有效JSON，请在JSON编辑中修正。', 'Routing rules are not valid JSON; fix them in the JSON editor.')); }
  if (!Array.isArray(value) || !value.every(object)) throw new Error(t('路由规则必须为JSON对象数组，请在JSON编辑中修正。', 'Routing rules must be a JSON array of objects; fix them in the JSON editor.'));
  return value;
}

// 保留未编辑条目的原文，避免未知字段中的大整数被JS数值精度改写。
function ruleTexts(text: string): string[] {
  routingRules(text);
  const items: string[] = [];
  let depth = 0, start = 0, quoted = false, escaped = false;
  for (let index = 0; index < text.length; index++) {
    const char = text[index];
    if (quoted) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === '"') quoted = false;
    } else if (char === '"') quoted = true;
    else if (char === '{' || char === '[') { if (depth === 1) start = index; depth++; }
    else if (char === '}' || char === ']') { depth--; if (depth === 1) items.push(text.slice(start, index + 1)); }
  }
  return items;
}
const ruleText = (items: string[]) => `[${items.length ? `\n${items.join(',\n')}\n` : ''}]`;

export function routingTargets(draft: SettingsDraft): RouteTarget[] {
  const targets: RouteTarget[] = [];
  for (const [text, kind, label] of [[draft.outbounds, 'outboundTag', t('出站', 'Outbound')], [draft.balancers, 'balancerTag', t('均衡', 'Balancer')]] as const) {
    let items: unknown;
    try { items = JSON.parse(text); } catch { throw new Error(t('出站或负载均衡JSON无效，请先修正对应分类。', 'Outbound or balancer JSON is invalid; fix that category first.')); }
    if (!Array.isArray(items) || !items.every(item => object(item) && typeof item.tag === 'string' && item.tag.trim())) throw new Error(t('出站或负载均衡缺少有效tag，请先修正对应分类。', 'An outbound or balancer lacks a valid tag; fix that category first.'));
    for (const item of items as { tag: string }[]) targets.push({ value: `${kind}:${item.tag}`, label: `${label} · ${item.tag}` });
  }
  if (new Set(targets.map(item => item.value)).size !== targets.length) throw new Error(t('出站或负载均衡tag重复，请先修正对应分类。', 'Duplicate outbound or balancer tag; fix that category first.'));
  return targets;
}

function lines(value: string, label: string): string[] {
  const items = value.split(/\r?\n/).map(item => item.trim()).filter(Boolean);
  if (items.length > 64 || items.some(item => item.length > 256 || /[\x00-\x1f\x7f]/.test(item))) throw new Error(`${label}${t('每项最多256字符，每组最多64项；请每行填写一项。', ' entries are limited to 256 characters each, at most 64; enter one per line.')}`);
  return items;
}
function domain(value: string): boolean {
  const host = value.replace(/^(domain:|full:)/, '').replace(/\.$/, '');
  return host.length <= 253 && host.split('.').every(part => /^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/.test(part));
}
function ip(value: string): boolean {
  const parts = value.split('/');
  if (parts.length > 2) return false;
  const address = parts[0];
  const v4 = address.split('.').length === 4 && address.split('.').every(part => /^(0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255);
  let v6 = false;
  if (address.includes(':') && /^[a-fA-F0-9:.]+$/.test(address)) { try { new URL(`http://[${address}]/`); v6 = true; } catch { /* 非法IPv6。 */ } }
  return (v4 || v6) && (parts.length === 1 || /^(0|[1-9][0-9]*)$/.test(parts[1]) && Number(parts[1]) <= (v4 ? 32 : 128));
}
function ports(value: string): string {
  const clean = value.trim();
  if (!clean) return '';
  if (clean.length > 256 || !clean.split(',').every(item => {
    const match = item.trim().match(/^([0-9]+)(?:\s*-\s*([0-9]+))?$/);
    return match && Number(match[1]) >= 1 && Number(match[1]) <= 65535 && (!match[2] || Number(match[2]) >= Number(match[1]) && Number(match[2]) <= 65535);
  })) throw new Error(t('目标端口应为1–65535，多个用英文逗号分隔，范围如1000-2000。', 'Target ports must be 1–65535, separated by commas, ranges like 1000-2000.'));
  return clean.replace(/\s/g, '');
}

export function routeRule(fields: RouteFields): RoutingRule {
  const result: RoutingRule = { type: 'field' };
  const domains = lines(fields.domain, t('域名', 'Domain')), ips = lines(fields.ip, t('目标IP', 'Target IP')), protocols = lines(fields.protocol, t('嗅探协议', 'Sniffed protocol'));
  if (!domains.every(domain)) throw new Error(t('域名请每行填写主机名、domain:域名或full:域名，不包含网址/路径；其他高级写法请使用JSON。', 'Enter one hostname, domain:domain or full:domain per line, without URL or path; use JSON for advanced forms.'));
  if (!ips.every(ip)) throw new Error(t('目标IP请每行填写有效IPv4、IPv6或CIDR；Geo等高级写法请使用JSON。', 'Enter one valid IPv4, IPv6 or CIDR per line; use JSON for Geo and other advanced forms.'));
  if (!protocols.every(item => ['http', 'tls', 'quic', 'bittorrent'].includes(item))) throw new Error(t('嗅探协议仅支持http、tls、quic、bittorrent，请每行一项；其他高级写法请使用JSON。', 'Sniffed protocols support only http, tls, quic and bittorrent, one per line; use JSON for advanced forms.'));
  if (!['', 'tcp', 'udp', 'tcp,udp'].includes(fields.network)) throw new Error(t('传输只支持TCP、UDP或二者。', 'Network supports only TCP, UDP or both.'));
  if (domains.length) result.domain = domains;
  if (ips.length) result.ip = ips;
  if (protocols.length) result.protocol = protocols;
  const port = ports(fields.port);
  if (port) result.port = port;
  if (fields.network) result.network = fields.network;
  if (Object.keys(result).length === 1) throw new Error(t('至少填写一个匹配条件，空表单不会创建全流量规则。', 'Fill in at least one match condition; an empty form never creates a match-all rule.'));
  const target = fields.target.match(/^(outboundTag|balancerTag):(.+)$/s);
  if (!target) throw new Error(t('请选择有效出站或负载均衡。', 'Choose a valid outbound or balancer.'));
  result[target[1]] = target[2];
  return result;
}

// 只有表单能完整往返的规则才可编辑；其余原样留在JSON中。
export function routeFields(rule: RoutingRule): RouteFields | null {
  const allowed = ['type', 'domain', 'ip', 'port', 'network', 'protocol', 'outboundTag', 'balancerTag'];
  if (rule.type !== 'field' || Object.keys(rule).some(key => !allowed.includes(key)) || ('outboundTag' in rule) === ('balancerTag' in rule)) return null;
  const fields = emptyRoute();
  for (const key of ['domain', 'ip', 'protocol'] as const) {
    if (!(key in rule)) continue;
    const value = rule[key];
    if (!Array.isArray(value) || !value.length || !value.every(item => typeof item === 'string' && item.trim() === item && !/[\r\n]/.test(item))) return null;
    fields[key] = value.join('\n');
  }
  for (const key of ['port', 'network'] as const) {
    if (key in rule && (typeof rule[key] !== 'string' || !rule[key])) return null;
    fields[key] = String(rule[key] ?? '');
  }
  const kind = 'outboundTag' in rule ? 'outboundTag' : 'balancerTag';
  if (typeof rule[kind] !== 'string' || !rule[kind]) return null;
  fields.target = `${kind}:${rule[kind]}`;
  try {
    const rebuilt = routeRule(fields);
    if (Object.keys(rule).some(key => JSON.stringify(rule[key]) !== JSON.stringify(rebuilt[key]))) return null;
  } catch { return null; }
  return fields;
}

export function putRoute(draft: SettingsDraft, index: number, fields: RouteFields): string {
  const rules = routingRules(draft.rules);
  if (!Number.isInteger(index) || index < -1 || index >= rules.length) throw new Error(t('规则已变化，请重新选择。', 'The rules changed; please select again.'));
  if (index >= 0 && !routeFields(rules[index])) throw new Error(t('该规则含高级写法，请使用JSON编辑以保留全部字段。', 'This rule uses advanced fields; edit it as JSON to keep them all.'));
  if (!routingTargets(draft).some(item => item.value === fields.target)) throw new Error(t('所选出站或负载均衡已不存在，请重新选择。', 'The selected outbound or balancer no longer exists; select again.'));
  const rule = routeRule(fields);
  const items = ruleTexts(draft.rules);
  if (index === -1) items.push(JSON.stringify(rule, null, 2)); else items[index] = JSON.stringify(rule, null, 2);
  const text = ruleText(items);
  buildSettings({ ...draft, rules: text });
  return text;
}

export function moveRoute(text: string, index: number, offset: -1 | 1): string {
  const rules = ruleTexts(text), next = index + offset;
  if (!Number.isInteger(index) || index < 0 || index >= rules.length || next < 0 || next >= rules.length) throw new Error(t('无法移动该规则。', 'Cannot move this rule.'));
  [rules[index], rules[next]] = [rules[next], rules[index]];
  return ruleText(rules);
}

export function removeRoute(text: string, index: number): string {
  const rules = ruleTexts(text);
  if (!Number.isInteger(index) || index < 0 || index >= rules.length) throw new Error(t('规则已变化，请重新选择。', 'The rules changed; please select again.'));
  rules.splice(index, 1);
  return ruleText(rules);
}

export function routeSummary(rule: RoutingRule): string {
  const labels: [string, string][] = [['domain', t('域名', 'Domain')], ['ip', 'IP'], ['port', t('端口', 'Port')], ['network', t('传输', 'Network')], ['protocol', t('嗅探协议', 'Sniffed protocol')]];
  const parts = labels.flatMap(([key, label]) => {
    const value = rule[key];
    if (Array.isArray(value) && value.every(item => typeof item === 'string')) return [`${label}${t('：', ': ')}${value.slice(0, 2).join(t('、', ', '))}${value.length > 2 ? ` ${t('等', '(')}${value.length}${t('项', ' total)')}` : ''}`];
    return typeof value === 'string' ? [`${label}${t('：', ': ')}${value}`] : [];
  });
  return parts.join(t('；', '; ')) || t('高级匹配条件（请使用JSON查看）', 'Advanced match conditions (view as JSON)');
}
