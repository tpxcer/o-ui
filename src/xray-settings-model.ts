import { t } from './i18n.ts';
export type SettingsDraft = {
  freedomStrategy: string; routingStrategy: string; probeURL: string;
  statistics: boolean; logLevel: string; accessLog: boolean; dnsLog: boolean;
  rules: string; outbounds: string; balancers: string; dns: string; advanced: string;
};
export const freedomStrategies = ['AsIs', 'UseIP', 'UseIPv4', 'UseIPv6', 'UseIPv4v6', 'UseIPv6v4', 'ForceIP', 'ForceIPv4', 'ForceIPv6', 'ForceIPv4v6', 'ForceIPv6v4'];
export const routingStrategies = ['AsIs', 'IPIfNonMatch', 'IPOnDemand'];

export function defaultSettings(): SettingsDraft {
  return {
    freedomStrategy: 'AsIs', routingStrategy: 'AsIs', probeURL: 'https://www.google.com/generate_204',
    statistics: true, logLevel: 'warning', accessLog: false, dnsLog: false,
    rules: '[]', outbounds: '[\n  { "tag": "direct", "protocol": "freedom", "settings": {} },\n  { "tag": "block", "protocol": "blackhole", "settings": {} }\n]',
    balancers: '[]', dns: '{\n  "servers": ["localhost"],\n  "queryStrategy": "UseIP"\n}', advanced: '{}',
  };
}

type ObjectValue = Record<string, unknown>;
function object(value: unknown): value is ObjectValue { return !!value && typeof value === 'object' && !Array.isArray(value); }
function parse(value: string, label: string, array: boolean): ObjectValue | ObjectValue[] {
  let result: unknown;
  try { result = JSON.parse(value); } catch { throw new Error(`${label}${t('不是有效JSON，请检查括号、引号和逗号。', ' is not valid JSON; check brackets, quotes and commas.')}`); }
  if (array ? (!Array.isArray(result) || !result.every(object)) : !object(result)) throw new Error(`${label}${t('必须是', ' must be a ')}${array ? t('JSON对象数组', 'JSON array of objects') : t('JSON对象', 'JSON object')}${t('。', '. ')}`);
  return result as ObjectValue | ObjectValue[];
}
function tags(items: ObjectValue[], label: string): Set<string> {
  const result = new Set<string>();
  for (const item of items) {
    if (typeof item.tag !== 'string' || !item.tag.trim()) throw new Error(`${label}${t('每项都要有非空tag。', ': every entry needs a non-empty tag.')}`);
    if (result.has(item.tag)) throw new Error(`${label}${t('tag重复：', ' duplicate tag: ')}${item.tag}${t('。', '. ')}`);
    result.add(item.tag);
  }
  return result;
}

// 只作面板草稿结构/引用校验，不替代所选官方核心的完整预检。
// reverseTags是入站客户端的反向代理标签：核心为每个桥接连接生成同名出站，路由可直接引用。
export function buildSettings(draft: SettingsDraft, reverseTags: string[] = []): ObjectValue {
  if (!freedomStrategies.includes(draft.freedomStrategy) || !routingStrategies.includes(draft.routingStrategy)) throw new Error(t('协议或路由域策略无效。', 'Invalid protocol or routing domain strategy.'));
  let url: URL;
  try { url = new URL(draft.probeURL); } catch { throw new Error(t('出站测试URL应为有效的HTTP/HTTPS地址。', 'Outbound test URL must be a valid HTTP/HTTPS address.')); }
  if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) throw new Error(t('出站测试URL只允许无账号密码的HTTP/HTTPS地址。', 'Outbound test URL must be an HTTP/HTTPS address without credentials.'));
  if (!['none', 'error', 'warning', 'info', 'debug'].includes(draft.logLevel)) throw new Error(t('日志级别无效。', 'Invalid log level.'));
  const rules = parse(draft.rules, t('路由规则', 'Routing rules'), true) as ObjectValue[];
  const outbounds = parse(draft.outbounds, t('出站规则', 'Outbounds'), true) as ObjectValue[];
  const balancers = parse(draft.balancers, t('负载均衡', 'Balancers'), true) as ObjectValue[];
  const dns = parse(draft.dns, 'DNS', false) as ObjectValue;
  const advanced = parse(draft.advanced, t('高级配置', 'Advanced'), false) as ObjectValue;
  if (!outbounds.length) throw new Error(t('至少保留一个明确出站，不自动补充直连。', 'Keep at least one explicit outbound; Direct is not added automatically.'));
  const outboundTags = tags(outbounds, t('出站规则', 'Outbounds'));
  const balancerTags = tags(balancers, t('负载均衡', 'Balancers'));
  const clash = reverseTags.find(tag => outboundTags.has(tag));
  if (clash) throw new Error(`${t('反向代理标签与出站标识重复：', 'A reverse tag duplicates an outbound tag: ')}${clash}`);
  for (const outbound of outbounds) {
    if (typeof outbound.protocol !== 'string' || !outbound.protocol.trim()) throw new Error(t('每个出站必须声明protocol。', 'Every outbound must declare a protocol.'));
    if (outbound.protocol === 'freedom') {
      if (outbound.settings !== undefined && !object(outbound.settings)) throw new Error(t('Freedom出站settings必须为对象。', 'Freedom outbound settings must be an object.'));
      const settings = { ...((outbound.settings ?? {}) as ObjectValue), targetStrategy: draft.freedomStrategy };
      delete (settings as ObjectValue).domainStrategy;
      outbound.settings = settings;
    }
  }
  for (const balancer of balancers) {
    if (!Array.isArray(balancer.selector) || !balancer.selector.length || !balancer.selector.every(item => typeof item === 'string' && item.length > 0)) throw new Error(t('每个负载均衡必须有非空selector数组。', 'Every balancer needs a non-empty selector array.'));
    for (const prefix of balancer.selector as string[]) {
      if (![...outboundTags].some(tag => tag.startsWith(prefix))) throw new Error(`${t('负载均衡selector未匹配出站：', 'Balancer selector matches no outbound: ')}${prefix}${t('。', '. ')}`);
    }
    if (balancer.fallbackTag !== undefined && !outboundTags.has(String(balancer.fallbackTag))) throw new Error(t('负载均衡fallbackTag未匹配出站。', 'Balancer fallbackTag matches no outbound.'));
    if (balancer.strategy !== undefined && (!object(balancer.strategy) || !['random', 'roundRobin', 'leastPing', 'leastLoad'].includes(String(balancer.strategy.type)))) throw new Error(t('负载均衡策略应为random、roundRobin、leastPing或leastLoad。', 'Balancer strategy must be random, roundRobin, leastPing or leastLoad.'));
  }
  for (const rule of rules) {
    if (rule.type !== 'field') throw new Error(t('当前草稿路由规则仅支持type为field。', 'Draft routing rules support only type field.'));
    if (('outboundTag' in rule) === ('balancerTag' in rule)) throw new Error(t('每条路由规则必须且只能指定outboundTag或balancerTag。', 'Each routing rule must specify exactly one of outboundTag or balancerTag.'));
    if ('outboundTag' in rule && (typeof rule.outboundTag !== 'string' || !(outboundTags.has(rule.outboundTag) || reverseTags.includes(rule.outboundTag)))) throw new Error(t('路由引用了不存在的出站，或outboundTag不是字符串。', 'A routing rule references a missing outbound, or outboundTag is not a string.'));
    if ('balancerTag' in rule && (typeof rule.balancerTag !== 'string' || !balancerTags.has(rule.balancerTag))) throw new Error(t('路由引用了不存在的负载均衡，或balancerTag不是字符串。', 'A routing rule references a missing balancer, or balancerTag is not a string.'));
    const arrayConditions = ['domain', 'ip', 'source', 'sourceIP', 'protocol', 'inboundTag', 'user', 'process'];
    const textConditions = ['port', 'sourcePort', 'vlessRoute', 'network'];
    for (const key of arrayConditions) {
      if (key in rule && (!Array.isArray(rule[key]) || !rule[key].length || !rule[key].every(value => typeof value === 'string' && value.trim()))) throw new Error(`${t('路由', 'Routing ')}${key}${t('必须为非空字符串数组。', ' must be a non-empty string array.')}`);
    }
    for (const key of textConditions) {
      if (key in rule && (typeof rule[key] !== 'string' || !rule[key].trim())) throw new Error(`${t('路由', 'Routing ')}${key}${t('必须为非空字符串。', ' must be a non-empty string.')}`);
    }
    // attrs为“名称→值”对象（官方26.7.28实测：字符串写法预检失败）。
    if ('attrs' in rule && (!object(rule.attrs) || !Object.keys(rule.attrs).length || Object.entries(rule.attrs).some(([name, value]) => !name.trim() || typeof value !== 'string'))) throw new Error(t('路由属性必须为非空的名称与值。', 'Routing attrs must be non-empty name/value pairs.'));
    if (typeof rule.network === 'string' && !rule.network.split(',').every(value => ['tcp', 'udp'].includes(value.trim()))) throw new Error(t('路由network只接受tcp、udp或tcp,udp。', 'Routing network accepts only tcp, udp or tcp,udp.'));
    if (![...arrayConditions, ...textConditions, 'attrs'].some(key => key in rule)) throw new Error(t('路由规则至少需要一个匹配条件。', 'A routing rule needs at least one match condition.'));
  }
  if (!Array.isArray(dns.servers) || !dns.servers.length || !dns.servers.every(server => typeof server === 'string' ? !!server.trim() : object(server) && typeof server.address === 'string' && !!server.address.trim())) throw new Error(t('DNS需要非空servers数组，成员为非空地址字符串或带address的对象。', 'DNS needs a non-empty servers array of address strings or objects with address.'));
  if (dns.queryStrategy !== undefined && !['UseIP', 'UseIPv4', 'UseIPv6'].includes(String(dns.queryStrategy))) throw new Error(t('DNS查询策略应为UseIP、UseIPv4或UseIPv6。', 'DNS query strategy must be UseIP, UseIPv4 or UseIPv6.'));
  const allowedAdvanced = new Set(['observatory', 'burstObservatory', 'fakedns', 'transport']);
  for (const [key, value] of Object.entries(advanced)) {
    if (!allowedAdvanced.has(key)) throw new Error(`${t('高级配置暂不接受', 'Advanced configuration does not accept ')}${key}${t('，避免覆盖其他分类或写入入站/控制接口。', ', to avoid overriding other categories or writing inbounds/control APIs.')}`);
    if (key === 'fakedns' ? !Array.isArray(value) : !object(value)) throw new Error(`${t('高级字段', 'Advanced field ')}${key}${t('结构不正确。', ' has an invalid structure.')}`);
  }
  for (const balancer of balancers) {
    const strategy = (balancer.strategy as ObjectValue | undefined)?.type;
    if (['leastPing', 'leastLoad'].includes(String(strategy)) && !advanced.observatory && !advanced.burstObservatory) throw new Error(t('leastPing/leastLoad需要在高级配置中提供观测配置；当前只校验存在，不执行测速。', 'leastPing/leastLoad need an observatory in the advanced configuration; only its presence is checked, no speed test is run.'));
  }
  return {
    ...advanced,
    log: { loglevel: draft.logLevel, access: draft.accessLog ? '' : 'none', dnsLog: draft.dnsLog },
    outbounds, routing: { domainStrategy: draft.routingStrategy, rules, balancers }, dns,
    // 统计常开：流量额度与首页业务网速都依赖它。
    stats: {}, policy: { levels: { '0': { statsUserUplink: true, statsUserDownlink: true } }, system: { statsInboundUplink: true, statsInboundDownlink: true, statsOutboundUplink: true, statsOutboundDownlink: true } },
  };
}
