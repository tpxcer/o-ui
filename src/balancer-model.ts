import { buildSettings } from './xray-settings-model.ts';
import type { SettingsDraft } from './xray-settings-model.ts';
import { t } from './i18n.ts';

export type BalancerFields = { tag: string; selector: string; strategy: string };
export const emptyBalancer = (): BalancerFields => ({ tag: '', selector: '', strategy: '' });
type Item = Record<string, unknown>;
const object = (value: unknown): value is Item => !!value && typeof value === 'object' && !Array.isArray(value);
const tagOK = (value: string) => !!value && value.length <= 128 && !/[\s\x00-\x1f\x7f]/.test(value);

// 仅替换选中条目，保留其它高级对象的数字、转义和重复键原文。
export function balancerTexts(text: string): string[] {
  let values: unknown;
  try { values = JSON.parse(text); } catch { throw new Error(t('负载均衡不是有效JSON，请在JSON编辑中修正。', 'Balancers are not valid JSON; fix them in the JSON editor.')); }
  if (!Array.isArray(values) || !values.every(object)) throw new Error(t('负载均衡必须是JSON对象数组。', 'Balancers must be a JSON array of objects.'));
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

function balancer(fields: BalancerFields): Item {
  const tag = fields.tag.trim(), selector = fields.selector.split(/\r?\n/).map(value => value.trim()).filter(Boolean);
  if (!tagOK(tag)) throw new Error(t('均衡标识需为1–128个非空白字符。', 'Balancer tag must be 1–128 non-whitespace characters.'));
  if (!selector.length || selector.length > 64 || !selector.every(tagOK)) throw new Error(t('出站前缀请每行填写一项，最多64项，每项1–128个非空白字符。', 'Enter one outbound prefix per line, up to 64, each 1–128 non-whitespace characters.'));
  if (!['', 'random', 'roundRobin'].includes(fields.strategy)) throw new Error(t('常用表单只支持核心默认、随机或轮询；其它策略请用JSON。', 'The form supports only the core default, random or round robin; use JSON for other strategies.'));
  const result: Item = { tag, selector };
  if (fields.strategy) result.strategy = { type: fields.strategy };
  return result;
}

export function balancerFields(text: string): BalancerFields | null {
  let item: unknown;
  try { item = JSON.parse(text); } catch { return null; }
  if (!object(item) || Object.keys(item).some(key => !['tag', 'selector', 'strategy'].includes(key)) || typeof item.tag !== 'string' || !Array.isArray(item.selector) || !item.selector.every(value => typeof value === 'string')) return null;
  const fields: BalancerFields = { tag: item.tag, selector: item.selector.join('\n'), strategy: '' };
  let keys = Object.keys(item).length;
  if ('strategy' in item) {
    if (!object(item.strategy) || Object.keys(item.strategy).length !== 1 || typeof item.strategy.type !== 'string' || !item.strategy.type) return null;
    fields.strategy = item.strategy.type; keys++;
  }
  let quoted = false, escaped = false, properties = 0;
  for (const char of text) {
    if (quoted) { if (escaped) escaped = false; else if (char === '\\') escaped = true; else if (char === '"') quoted = false; }
    else if (char === '"') quoted = true;
    else if (char === ':') properties++;
  }
  if (properties !== keys) return null;
  try {
    const rebuilt = balancer(fields);
    if (Object.keys(item).some(key => JSON.stringify(item[key]) !== JSON.stringify(rebuilt[key]))) return null;
  } catch { return null; }
  return fields;
}

export function matchedOutbounds(draft: SettingsDraft, selector: string): string[] {
  let items: unknown;
  try { items = JSON.parse(draft.outbounds); } catch { throw new Error(t('出站JSON无效，请先修正出站规则。', 'Outbound JSON is invalid; fix the outbounds first.')); }
  if (!Array.isArray(items) || !items.every(item => object(item) && typeof item.tag === 'string' && item.tag.trim())) throw new Error(t('出站需为含有效tag的对象数组。', 'Outbounds must be an array of objects with valid tags.'));
  const tags = items.map(item => item.tag as string);
  if (new Set(tags).size !== tags.length) throw new Error(t('出站tag重复，请先修正出站规则。', 'Duplicate outbound tag; fix the outbounds first.'));
  const prefixes = selector.split(/\r?\n/).map(value => value.trim()).filter(Boolean);
  return tags.filter(tag => prefixes.some(prefix => tag.startsWith(prefix))).sort();
}

function referenced(draft: SettingsDraft, tag: unknown): boolean {
  let rules: unknown;
  try { rules = JSON.parse(draft.rules); } catch { throw new Error(t('路由JSON无效，无法确认均衡引用，请先修正。', 'Routing JSON is invalid, so balancer references cannot be checked; fix it first.')); }
  if (!Array.isArray(rules) || !rules.every(object)) throw new Error(t('路由需为对象数组，无法确认均衡引用。', 'Routing must be an array of objects to check balancer references.'));
  return rules.some(rule => rule.balancerTag === tag);
}
const serialize = (items: string[]) => `[${items.length ? `\n${items.join(',\n')}\n` : ''}]`;

export function putBalancer(draft: SettingsDraft, index: number, fields: BalancerFields): string {
  const items = balancerTexts(draft.balancers);
  if (!Number.isInteger(index) || index < -1 || index >= items.length) throw new Error(t('均衡列表已变化，请重新选择。', 'The balancer list changed; please select again.'));
  const result = balancer(fields);
  if (index >= 0) {
    if (!balancerFields(items[index])) throw new Error(t('此均衡含高级字段或重复键，请用JSON编辑。', 'This balancer has advanced fields or duplicate keys; edit it as JSON.'));
    const old = JSON.parse(items[index]) as Item;
    if (old.tag !== result.tag && referenced(draft, old.tag)) throw new Error(t('该均衡正被路由引用，请先移除或更改路由引用，再重命名。', 'This balancer is referenced by routing; remove or change the references before renaming it.'));
  }
  if (index === -1) items.push(JSON.stringify(result, null, 2)); else items[index] = JSON.stringify(result, null, 2);
  const text = serialize(items);
  buildSettings({ ...draft, balancers: text });
  return text;
}

export function removeBalancer(draft: SettingsDraft, index: number): string {
  const items = balancerTexts(draft.balancers);
  if (!Number.isInteger(index) || index < 0 || index >= items.length) throw new Error(t('均衡列表已变化，请重新选择。', 'The balancer list changed; please select again.'));
  if (!balancerFields(items[index])) throw new Error(t('高级均衡请用JSON编辑，以免误判引用。', 'Edit advanced balancers as JSON to avoid misjudging references.'));
  if (referenced(draft, (JSON.parse(items[index]) as Item).tag)) throw new Error(t('该均衡正被路由引用，请先移除或更改路由引用，再删除。', 'This balancer is referenced by routing; remove or change the references before deleting it.'));
  items.splice(index, 1);
  return serialize(items);
}
