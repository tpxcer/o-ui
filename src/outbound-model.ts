import { buildSettings } from './xray-settings-model.ts';
import type { SettingsDraft } from './xray-settings-model.ts';
import { t } from './i18n.ts';

export type OutboundFields = { tag: string; protocol: string; address: string; port: string; user: string; pass: string };
export const emptyOutbound = (): OutboundFields => ({ tag: '', protocol: 'freedom', address: '', port: '', user: '', pass: '' });
export const outboundProtocols = [['freedom', t('直连', 'Direct')], ['blackhole', t('阻断', 'Block')], ['http', t('HTTP 代理', 'HTTP proxy')], ['socks', t('SOCKS5 代理', 'SOCKS5 proxy')]] as const;
type Item = Record<string, unknown>;
const object = (value: unknown): value is Item => !!value && typeof value === 'object' && !Array.isArray(value);

export function outboundTexts(text: string): string[] {
  let values: unknown;
  try { values = JSON.parse(text); } catch { throw new Error(t('出站不是有效JSON，请在JSON编辑中修正。', 'Outbounds are not valid JSON; fix them in the JSON editor.')); }
  if (!Array.isArray(values) || !values.every(object)) throw new Error(t('出站必须为JSON对象数组。', 'Outbounds must be a JSON array of objects.'));
  const items: string[] = [];
  let depth = 0, start = 0, quoted = false, escaped = false;
  for (let index = 0; index < text.length; index++) {
    const char = text[index];
    if (quoted) { if (escaped) escaped = false; else if (char === '\\') escaped = true; else if (char === '"') quoted = false; }
    else if (char === '"') quoted = true;
    else if (char === '{' || char === '[') { if (depth === 1) start = index; depth++; }
    else if (char === '}' || char === ']') { depth--; if (depth === 1) items.push(text.slice(start, index + 1)); }
  }
  return items;
}

export function outbound(fields: OutboundFields): Item {
  const tag = fields.tag.trim();
  if (!tag || tag.length > 128 || /[\s\x00-\x1f\x7f]/.test(tag)) throw new Error(t('出站标识需为1–128个非空白字符。', 'Outbound tag must be 1–128 non-whitespace characters.'));
  if (!outboundProtocols.some(([value]) => value === fields.protocol)) throw new Error(t('此协议请使用高级JSON编辑。', 'Use the advanced JSON editor for this protocol.'));
  const result: Item = { tag, protocol: fields.protocol, settings: {} };
  if (fields.protocol === 'http' || fields.protocol === 'socks') {
    const address = fields.address.trim();
    let valid = address.length <= 253 && address.split('.').every(part => /^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/.test(part));
    if (/^[0-9.]+$/.test(address)) valid = address.split('.').length === 4 && address.split('.').every(part => /^(0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255);
    if (address.includes(':')) { try { new URL(`http://[${address}]/`); valid = /^[a-fA-F0-9:.]+$/.test(address); } catch { valid = false; } }
    if (!valid) throw new Error(t('代理地址请填域名、IPv4或不带方括号的IPv6，不含协议、端口或路径。', 'Proxy address must be a domain, IPv4 or IPv6 without brackets, scheme, port or path.'));
    if (!/^[0-9]+$/.test(fields.port) || Number(fields.port) < 1 || Number(fields.port) > 65535) throw new Error(t('代理端口需为1–65535的整数。', 'Proxy port must be an integer from 1 to 65535.'));
    const limit = fields.protocol === 'socks' ? 255 : 1024;
    if ([fields.user, fields.pass].some(value => new TextEncoder().encode(value).length > limit || /[\x00-\x1f\x7f]/.test(value)) || fields.protocol === 'http' && fields.user.includes(':')) throw new Error(`${t('代理凭据不能含控制字符，长度最多', 'Proxy credentials must not contain control characters and are limited to ')}${limit}${t('字节；HTTP用户名不能含冒号。', ' bytes; HTTP usernames cannot contain colons.')}`);
    if (!!fields.user !== !!fields.pass) throw new Error(t('认证需要同时填写用户名和密码；无需认证时两项都留空。', 'Authentication needs both username and password; leave both empty for none.'));
    const server: Item = { address, port: Number(fields.port) };
    if (fields.user) server.users = [{ user: fields.user, pass: fields.pass }];
    result.settings = { servers: [server] };
  }
  return result;
}

// 仅可完整表达的对象进入表单，重复键（含转义键）及高级字段留在JSON。
export function outboundFields(text: string): OutboundFields | null {
  let item: unknown;
  try { item = JSON.parse(text); } catch { return null; }
  if (!object(item) || Object.keys(item).some(key => !['tag', 'protocol', 'settings'].includes(key)) || typeof item.tag !== 'string' || typeof item.protocol !== 'string') return null;
  const fields = { ...emptyOutbound(), tag: item.tag, protocol: item.protocol };
  if (item.protocol === 'http' || item.protocol === 'socks') {
    if (!object(item.settings) || !Array.isArray(item.settings.servers) || item.settings.servers.length !== 1 || !object(item.settings.servers[0])) return null;
    const server = item.settings.servers[0];
    if (typeof server.address !== 'string' || typeof server.port !== 'number') return null;
    fields.address = server.address; fields.port = String(server.port);
    if ('users' in server) {
      if (!Array.isArray(server.users) || server.users.length !== 1 || !object(server.users[0]) || typeof server.users[0].user !== 'string' || typeof server.users[0].pass !== 'string') return null;
      fields.user = server.users[0].user; fields.pass = server.users[0].pass;
    }
  }
  const pending: unknown[] = [item];
  let keys = 0;
  while (pending.length) {
    const value = pending.pop();
    if (Array.isArray(value)) { for (const part of value) pending.push(part); }
    else if (object(value)) { keys += Object.keys(value).length; for (const part of Object.values(value)) pending.push(part); }
  }
  let quoted = false, escaped = false, properties = 0;
  for (const char of text) {
    if (quoted) { if (escaped) escaped = false; else if (char === '\\') escaped = true; else if (char === '"') quoted = false; }
    else if (char === '"') quoted = true;
    else if (char === ':') properties++;
  }
  if (properties !== keys) return null;
  const same = (a: unknown, b: unknown): boolean => object(a) && object(b) ? Object.keys(a).length === Object.keys(b).length && Object.keys(a).every(key => key in b && same(a[key], b[key])) : Array.isArray(a) && Array.isArray(b) ? a.length === b.length && a.every((part, index) => same(part, b[index])) : a === b;
  try {
    const rebuilt = outbound(fields);
    if (!('settings' in item)) delete rebuilt.settings;
    if (!same(item, rebuilt)) return null;
  } catch { return null; }
  return fields;
}

function referenced(draft: SettingsDraft, tag: string): boolean {
  let rules: unknown, balancers: unknown, outbounds: unknown, advanced: unknown;
  try { rules = JSON.parse(draft.rules); balancers = JSON.parse(draft.balancers); outbounds = JSON.parse(draft.outbounds); advanced = JSON.parse(draft.advanced); }
  catch { throw new Error(t('路由、均衡或高级JSON无效，无法确认出站引用，请先修正。', 'Routing, balancer or advanced JSON is invalid, so outbound references cannot be checked; fix it first.')); }
  if (!Array.isArray(rules) || !rules.every(object) || !Array.isArray(balancers) || !balancers.every(object) || !Array.isArray(outbounds) || !outbounds.every(object) || !object(advanced)) throw new Error(t('引用配置结构无效，请先修正JSON。', 'Referencing configuration is invalid; fix the JSON first.'));
  if (rules.some(rule => rule.outboundTag === tag) || balancers.some(item => item.fallbackTag === tag || Array.isArray(item.selector) && item.selector.some(prefix => typeof prefix === 'string' && tag.startsWith(prefix)))) return true;
  // 高级链式出站或观测不能由常用表单安全改写引用。
  const uses = (value: unknown): boolean => {
    const pending: unknown[] = [value];
    while (pending.length) {
      const item = pending.pop();
      if (Array.isArray(item)) { for (const part of item) pending.push(part); }
      else if (object(item)) for (const [key, part] of Object.entries(item)) {
        if (['tag', 'dialerProxy'].includes(key) && part === tag || key === 'subjectSelector' && Array.isArray(part) && part.some(prefix => typeof prefix === 'string' && tag.startsWith(prefix))) return true;
        pending.push(part);
      }
    }
    return false;
  };
  return outbounds.some(item => Object.entries(item).some(([key, part]) => key !== 'tag' && uses(part))) || uses(advanced);
}
const serialize = (items: string[]) => `[${items.length ? `\n${items.join(',\n')}\n` : ''}]`;
export function putOutbound(draft: SettingsDraft, index: number, fields: OutboundFields): string {
  const items = outboundTexts(draft.outbounds);
  if (!Number.isInteger(index) || index < -1 || index >= items.length) throw new Error(t('出站列表已变化，请重新选择。', 'The outbound list changed; please select again.'));
  const result = outbound(fields);
  if (index >= 0) {
    const old = outboundFields(items[index]);
    if (!old) throw new Error(t('高级出站请用JSON编辑，原文未修改。', 'Edit advanced outbounds as JSON; the original was not changed.'));
    if (old.tag !== result.tag && referenced(draft, old.tag)) throw new Error(t('此出站被路由、均衡或高级配置引用，请先修改引用，再重命名。', 'This outbound is referenced by routing, balancers or advanced config; update the references before renaming it.'));
  }
  if (index === -1) items.push(JSON.stringify(result, null, 2)); else items[index] = JSON.stringify(result, null, 2);
  const text = serialize(items); buildSettings({ ...draft, outbounds: text }); return text;
}
export function removeOutbound(draft: SettingsDraft, index: number): string {
  const items = outboundTexts(draft.outbounds);
  if (!Number.isInteger(index) || index < 0 || index >= items.length) throw new Error(t('出站列表已变化，请重新选择。', 'The outbound list changed; please select again.'));
  const fields = outboundFields(items[index]);
  if (!fields) throw new Error(t('高级出站请用JSON编辑，原文未修改。', 'Edit advanced outbounds as JSON; the original was not changed.'));
  if (items.length === 1) throw new Error(t('至少保留一个出站，不自动补充直连。', 'Keep at least one outbound; Direct is not added automatically.'));
  if (index === 0) throw new Error(t('默认出站不能直接删除，请先明确调整出站顺序。', 'The default outbound cannot be deleted directly; reorder the outbounds first.'));
  if (referenced(draft, fields.tag)) throw new Error(t('此出站被路由、均衡或高级配置引用，请先修改引用，再删除。', 'This outbound is referenced by routing, balancers or advanced config; update the references before deleting it.'));
  items.splice(index, 1);
  const text = serialize(items); buildSettings({ ...draft, outbounds: text }); return text;
}
export function moveOutbound(text: string, index: number, offset: -1 | 1): string {
  const items = outboundTexts(text), next = index + offset;
  if (!Number.isInteger(index) || ![-1, 1].includes(offset) || index < 0 || index >= items.length || next < 0 || next >= items.length) throw new Error(t('无法移动该出站。', 'Cannot move this outbound.'));
  [items[index], items[next]] = [items[next], items[index]];
  return serialize(items);
}
