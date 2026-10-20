// Xray设置的结构化编辑模型（参照OUI的表格+弹窗）：在已保存草稿的JSON字符串与可编辑对象之间转换。
import type { SettingsDraft } from './xray-settings-model.ts';
import { t } from './i18n.ts';

export type Json = Record<string, unknown>;
export type Parts = { rules: Json[]; outbounds: Json[]; balancers: Json[]; dns: Json; advanced: Json };

export function parseParts(draft: SettingsDraft): Parts {
  const array = (text: string) => { const value = JSON.parse(text); if (!Array.isArray(value)) throw new Error(); return value as Json[]; };
  const object = (text: string) => { const value = JSON.parse(text); if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(); return value as Json; };
  return { rules: array(draft.rules), outbounds: array(draft.outbounds), balancers: array(draft.balancers), dns: object(draft.dns), advanced: object(draft.advanced) };
}

export function writeParts(draft: SettingsDraft, parts: Parts): SettingsDraft {
  const text = (value: unknown) => JSON.stringify(value, null, 2);
  return { ...draft, rules: text(parts.rules), outbounds: text(parts.outbounds), balancers: text(parts.balancers), dns: text(parts.dns), advanced: text(parts.advanced) };
}

const list = (value: unknown): string[] => Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];
export const lines = (text: string): string[] => text.split(/[\n,]/).map(item => item.trim()).filter(Boolean);

// ---- 基础路由（参照OUI：按出站tag与条件字段读写同一条规则） ----
export const blockIPOptions: [string, string][] = [['geoip:private', t('内网 IP', 'Private IPs')], ['geoip:cn', t('🇨🇳 中国', '🇨🇳 China')], ['geoip:ir', t('🇮🇷 伊朗', '🇮🇷 Iran')], ['geoip:ru', t('🇷🇺 俄罗斯', '🇷🇺 Russia')], ['geoip:vn', t('🇻🇳 越南', '🇻🇳 Vietnam')]];
export const blockDomainOptions: [string, string][] = [['geosite:category-ads-all', t('广告', 'Ads')], ['geosite:category-porn', t('成人内容', 'Adult +18')], ['geosite:cn', t('🇨🇳 中国网站', '🇨🇳 China')], ['regexp:.*\\.cn$', '🇨🇳 .cn'], ['geosite:category-ru', t('🇷🇺 俄罗斯网站', '🇷🇺 Russia')], ['regexp:.*\\.ir$', '🇮🇷 .ir']];
export const directIPOptions: [string, string][] = [['geoip:private', t('内网 IP', 'Private IPs')], ['geoip:cn', t('🇨🇳 中国', '🇨🇳 China')], ['geoip:ir', t('🇮🇷 伊朗', '🇮🇷 Iran')], ['geoip:ru', t('🇷🇺 俄罗斯', '🇷🇺 Russia')]];
export const directDomainOptions: [string, string][] = [['geosite:cn', t('🇨🇳 中国网站', '🇨🇳 China')], ['regexp:.*\\.cn$', '🇨🇳 .cn'], ['geosite:apple', 'Apple'], ['geosite:google', 'Google'], ['geosite:openai', 'OpenAI'], ['geosite:netflix', 'Netflix'], ['geosite:speedtest', 'Speedtest']];

export function ruleValues(rules: Json[], outboundTag: string, property: string): string[] {
  return rules.flatMap(rule => rule.outboundTag === outboundTag && property in rule ? list(rule[property]) : []);
}

export function setRuleValues(rules: Json[], outboundTag: string, property: string, values: string[]): Json[] {
  let inserted = false;
  const next: Json[] = [];
  for (const rule of rules) {
    if (rule.outboundTag === outboundTag && property in rule && Object.keys(rule).every(key => ['type', 'outboundTag', property].includes(key))) {
      if (!inserted && values.length) { next.push({ ...rule, [property]: values }); inserted = true; }
      continue;
    }
    next.push(rule);
  }
  if (!inserted && values.length) next.unshift({ type: 'field', outboundTag, [property]: values });
  return next;
}

export function ensureOutbound(outbounds: Json[], tag: string, outbound: Json): Json[] {
  return outbounds.some(item => item.tag === tag) ? outbounds : [...outbounds, outbound];
}

// ---- 路由规则 ----
export type RuleForm = { domain: string; ip: string; port: string; sourceIP: string; sourcePort: string; network: string; protocol: string[]; inboundTag: string[]; user: string; target: string };
export const emptyRule = (): RuleForm => ({ domain: '', ip: '', port: '', sourceIP: '', sourcePort: '', network: '', protocol: [], inboundTag: [], user: '', target: '' });
const ruleKeys = ['type', 'domain', 'ip', 'port', 'source', 'sourcePort', 'network', 'protocol', 'inboundTag', 'user', 'outboundTag', 'balancerTag'];

export function ruleForm(rule: Json): RuleForm | null {
  if (Object.keys(rule).some(key => !ruleKeys.includes(key))) return null;
  const text = (value: unknown) => typeof value === 'string' ? value : '';
  return {
    domain: list(rule.domain).join('\n'), ip: list(rule.ip).join('\n'), port: text(rule.port), sourceIP: list(rule.source).join('\n'), sourcePort: text(rule.sourcePort),
    network: text(rule.network), protocol: list(rule.protocol), inboundTag: list(rule.inboundTag), user: list(rule.user).join('\n'),
    target: typeof rule.balancerTag === 'string' ? `balancer:${rule.balancerTag}` : typeof rule.outboundTag === 'string' ? `outbound:${rule.outboundTag}` : '',
  };
}

export function ruleFromForm(form: RuleForm): Json {
  const rule: Json = { type: 'field' };
  const arrays: [string, string[]][] = [['domain', lines(form.domain)], ['ip', lines(form.ip)], ['source', lines(form.sourceIP)], ['protocol', form.protocol], ['inboundTag', form.inboundTag], ['user', lines(form.user)]];
  for (const [key, values] of arrays) if (values.length) rule[key] = values;
  for (const [key, value] of [['port', form.port], ['sourcePort', form.sourcePort], ['network', form.network]] as const) if (value.trim()) rule[key] = value.trim();
  if (Object.keys(rule).length === 1) throw new Error(t('至少填写一个匹配条件。', 'Fill in at least one match condition.'));
  const [kind, tag] = [form.target.split(':')[0], form.target.slice(form.target.indexOf(':') + 1)];
  if (!tag || (kind !== 'outbound' && kind !== 'balancer')) throw new Error(t('请选择出站或负载均衡。', 'Choose an outbound or balancer.'));
  rule[kind === 'outbound' ? 'outboundTag' : 'balancerTag'] = tag;
  return rule;
}

export function ruleSummary(rule: Json): string {
  const parts: string[] = [];
  const add = (label: string, values: string[]) => { if (values.length) parts.push(`${label} ${values.slice(0, 3).join(', ')}${values.length > 3 ? ` +${values.length - 3}` : ''}`); };
  add(t('域名', 'Domain'), list(rule.domain)); add('IP', list(rule.ip)); add(t('来源', 'Source'), list(rule.source)); add(t('协议', 'Protocol'), list(rule.protocol));
  add(t('入站', 'Inbound'), list(rule.inboundTag)); add(t('用户', 'User'), list(rule.user));
  for (const [label, key] of [[t('端口', 'Port'), 'port'], [t('来源端口', 'Source port'), 'sourcePort'], [t('网络', 'Network'), 'network']] as const) if (typeof rule[key] === 'string') parts.push(`${label} ${rule[key]}`);
  return parts.join(' · ') || t('（高级条件）', '(advanced conditions)');
}

// ---- 出站 ----
export const reverseTagPattern = /^[A-Za-z0-9_.-]{1,64}$/;
// 桥接端出站的反向代理标签（只有简化写法的VLESS出站有）。
export const outboundReverseTag = (outbound: Json): string => outbound.protocol === 'vless' ? str(record(record(outbound.settings).reverse).tag) : '';
export const outboundProtocolOptions: [string, string][] = [['freedom', t('直连 freedom', 'Direct (freedom)')], ['blackhole', t('阻断 blackhole', 'Block (blackhole)')], ['vless', 'VLESS'], ['vmess', 'VMess'], ['trojan', 'Trojan'], ['shadowsocks', 'Shadowsocks'], ['hysteria', 'Hysteria2'], ['socks', 'SOCKS'], ['http', 'HTTP']];
export type OutboundForm = {
  tag: string; protocol: string; address: string; port: string; id: string; flow: string; password: string; method: string; user: string; pass: string;
  network: string; path: string; host: string; serviceName: string; mode: string; security: string; serverName: string; fingerprint: string; alpn: string; allowInsecure: boolean;
  publicKey: string; shortId: string; spiderX: string; domainStrategy: string; reverseTag: string;
};
export const emptyOutbound = (): OutboundForm => ({ tag: '', protocol: 'vless', address: '', port: '443', id: '', flow: '', password: '', method: '2022-blake3-aes-128-gcm', user: '', pass: '', network: 'tcp', path: '/', host: '', serviceName: '', mode: 'auto', security: 'none', serverName: '', fingerprint: 'chrome', alpn: '', allowInsecure: false, publicKey: '', shortId: '', spiderX: '/', domainStrategy: '', reverseTag: '' });
export const ssMethods = ['2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305', 'aes-128-gcm', 'aes-256-gcm', 'chacha20-poly1305', 'xchacha20-poly1305', 'none'];
export const proxyProtocols = ['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria', 'socks', 'http'];
export const streamProtocols = ['vless', 'vmess', 'trojan', 'shadowsocks'];

const record = (value: unknown): Json => value && typeof value === 'object' && !Array.isArray(value) ? value as Json : {};
const str = (value: unknown) => typeof value === 'string' ? value : typeof value === 'number' ? String(value) : '';

export function outboundAddress(outbound: Json): string {
  const settings = record(outbound.settings);
  if (Array.isArray(settings.peers)) return str(record(settings.peers[0]).endpoint);
  const server = Array.isArray(settings.vnext) ? record(settings.vnext[0]) : Array.isArray(settings.servers) ? record(settings.servers[0]) : settings;
  const address = str(server.address), port = str(server.port);
  return address ? `${address}${port ? `:${port}` : ''}` : '';
}

export function outboundForm(outbound: Json): OutboundForm | null {
  const form = emptyOutbound();
  const protocol = str(outbound.protocol);
  if (!outboundProtocolOptions.some(([key]) => key === protocol)) return null;
  Object.assign(form, { tag: str(outbound.tag), protocol });
  const settings = record(outbound.settings), stream = record(outbound.streamSettings);
  if (protocol === 'freedom') { form.domainStrategy = str(settings.domainStrategy); return form; }
  if (protocol === 'blackhole') return form;
  const first = (value: unknown) => record(Array.isArray(value) ? value[0] : undefined);
  if (protocol === 'vless' && !settings.vnext) {
    // 简化写法（反向代理只能用这种写法）；reverse里有tag以外的字段时改用JSON编辑。
    const reverse = record(settings.reverse);
    if (Object.keys(reverse).some(key => key !== 'tag')) return null;
    Object.assign(form, { address: str(settings.address), port: str(settings.port), id: str(settings.id), flow: str(settings.flow), reverseTag: str(reverse.tag) });
  } else if (protocol === 'vless' || protocol === 'vmess') {
    const server = first(settings.vnext), user = first(server.users);
    Object.assign(form, { address: str(server.address), port: str(server.port), id: str(user.id), flow: str(user.flow) });
  } else if (protocol === 'hysteria') {
    Object.assign(form, { address: str(settings.address), port: str(settings.port), password: str(record(stream.hysteriaSettings).auth) });
  } else {
    const server = first(settings.servers), user = first(server.users);
    Object.assign(form, { address: str(server.address), port: str(server.port), password: str(server.password), method: str(server.method) || form.method, user: str(user.user), pass: str(user.pass) });
  }
  form.network = str(stream.network) || 'tcp';
  form.security = str(stream.security) || 'none';
  const ws = record(stream.wsSettings), hu = record(stream.httpupgradeSettings), xh = record(stream.xhttpSettings), grpc = record(stream.grpcSettings);
  if (form.network === 'ws') Object.assign(form, { path: str(ws.path) || '/', host: str(record(ws.headers).Host) || str(ws.host) });
  if (form.network === 'httpupgrade') Object.assign(form, { path: str(hu.path) || '/', host: str(hu.host) });
  if (form.network === 'xhttp') Object.assign(form, { path: str(xh.path) || '/', host: str(xh.host), mode: str(xh.mode) || 'auto' });
  if (form.network === 'grpc') form.serviceName = str(grpc.serviceName);
  const tls = record(stream.tlsSettings), reality = record(stream.realitySettings);
  if (form.security === 'tls') Object.assign(form, { serverName: str(tls.serverName), fingerprint: str(tls.fingerprint) || form.fingerprint, alpn: list(tls.alpn).join(','), allowInsecure: tls.allowInsecure === true });
  if (form.security === 'reality') Object.assign(form, { serverName: str(reality.serverName), fingerprint: str(reality.fingerprint) || form.fingerprint, publicKey: str(reality.password) || str(reality.publicKey), shortId: str(reality.shortId), spiderX: str(reality.spiderX) || '/' });
  return form;
}

export function outboundFromForm(form: OutboundForm): Json {
  const tag = form.tag.trim();
  if (!tag || /\s/.test(tag) || tag.length > 128) throw new Error(t('出站标识需为1–128个非空白字符。', 'Outbound tag must be 1–128 non-whitespace characters.'));
  if (form.protocol === 'freedom') return { tag, protocol: 'freedom', settings: form.domainStrategy ? { domainStrategy: form.domainStrategy } : {} };
  if (form.protocol === 'blackhole') return { tag, protocol: 'blackhole', settings: {} };
  const address = form.address.trim(), port = Number(form.port);
  if (!address || /[\s/]/.test(address)) throw new Error(t('请填写服务器地址（域名或 IP，不含端口和路径）。', 'Enter the server address (domain or IP, without port or path).'));
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error(t('端口需为 1–65535。', 'Port must be 1–65535.'));
  const out: Json = { tag, protocol: form.protocol };
  const need = (value: string, message: string) => { if (!value.trim()) throw new Error(message); return value.trim(); };
  const reverseTag = form.protocol === 'vless' ? form.reverseTag.trim() : '';
  if (reverseTag && !reverseTagPattern.test(reverseTag)) throw new Error(t('反向代理标签只能含字母、数字、点、下划线和连字符（最多64个）。', 'Reverse tags may contain only letters, digits, dots, underscores and hyphens (up to 64).'));
  // Xray只允许简化写法带reverse：此时出站作为反向代理桥接端，隧道里的连接以该标签作为入站进入路由。
  if (form.protocol === 'vless' && reverseTag) out.settings = { address, port, id: need(form.id, t('请填写 UUID。', 'Enter the UUID.')), encryption: 'none', ...(form.flow ? { flow: form.flow } : {}), reverse: { tag: reverseTag } };
  else if (form.protocol === 'vless') out.settings = { vnext: [{ address, port, users: [{ id: need(form.id, t('请填写 UUID。', 'Enter the UUID.')), encryption: 'none', ...(form.flow ? { flow: form.flow } : {}) }] }] };
  if (form.protocol === 'vmess') out.settings = { vnext: [{ address, port, users: [{ id: need(form.id, t('请填写 UUID。', 'Enter the UUID.')), security: 'auto' }] }] };
  if (form.protocol === 'trojan') out.settings = { servers: [{ address, port, password: need(form.password, t('请填写密码。', 'Enter the password.')) }] };
  if (form.protocol === 'shadowsocks') out.settings = { servers: [{ address, port, method: form.method, password: need(form.password, t('请填写密码。', 'Enter the password.')) }] };
  if (form.protocol === 'socks' || form.protocol === 'http') out.settings = { servers: [{ address, port, ...(form.user || form.pass ? { users: [{ user: form.user, pass: form.pass }] } : {}) }] };
  if (form.protocol === 'hysteria') {
    out.settings = { version: 2, address, port };
    out.streamSettings = { network: 'hysteria', security: 'tls', hysteriaSettings: { version: 2, auth: need(form.password, t('请填写认证密码。', 'Enter the auth password.')) }, tlsSettings: tlsSettings(form) };
    return out;
  }
  if (!streamProtocols.includes(form.protocol)) return out;
  const stream: Json = { network: form.network, security: form.security };
  if (form.network === 'ws') stream.wsSettings = { path: form.path || '/', ...(form.host ? { headers: { Host: form.host } } : {}) };
  if (form.network === 'httpupgrade') stream.httpupgradeSettings = { path: form.path || '/', ...(form.host ? { host: form.host } : {}) };
  if (form.network === 'xhttp') stream.xhttpSettings = { path: form.path || '/', mode: form.mode || 'auto', ...(form.host ? { host: form.host } : {}) };
  if (form.network === 'grpc') stream.grpcSettings = { serviceName: form.serviceName };
  if (form.security === 'tls') stream.tlsSettings = tlsSettings(form);
  if (form.security === 'reality') stream.realitySettings = { serverName: need(form.serverName, t('请填写 SNI。', 'Enter the SNI.')), fingerprint: form.fingerprint || 'chrome', password: need(form.publicKey, t('请填写 REALITY 公钥。', 'Enter the REALITY public key.')), shortId: form.shortId, spiderX: form.spiderX || '/' };
  out.streamSettings = stream;
  return out;
}

function tlsSettings(form: OutboundForm): Json {
  return { ...(form.serverName ? { serverName: form.serverName } : {}), ...(form.fingerprint ? { fingerprint: form.fingerprint } : {}), ...(form.alpn ? { alpn: lines(form.alpn) } : {}), ...(form.allowInsecure ? { allowInsecure: true } : {}) };
}

// 从分享链接导入（vless/vmess/trojan/ss/hysteria2），参照OUI出站弹窗的“导入链接”。
export function outboundFromLink(link: string): OutboundForm {
  const form = emptyOutbound();
  const text = link.trim();
  if (text.startsWith('vmess://')) {
    let data: Json;
    try { data = JSON.parse(atob(text.slice(8).replace(/-/g, '+').replace(/_/g, '/'))); } catch { throw new Error(t('无法解析 VMess 链接。', 'Cannot parse the VMess link.')); }
    Object.assign(form, { protocol: 'vmess', tag: str(data.ps) || 'vmess', address: str(data.add), port: str(data.port), id: str(data.id), network: str(data.net) || 'tcp', path: str(data.path) || '/', host: str(data.host), serviceName: str(data.path), security: str(data.tls) === 'tls' ? 'tls' : 'none', serverName: str(data.sni), alpn: str(data.alpn), fingerprint: str(data.fp) || 'chrome' });
    return form;
  }
  let url: URL;
  try { url = new URL(text); } catch { throw new Error(t('链接格式无效。', 'Invalid link.')); }
  const scheme = url.protocol.slice(0, -1), q = url.searchParams, user = decodeURIComponent(url.username);
  const host = url.hostname.replace(/^\[|\]$/g, ''), port = url.port || '443', name = decodeURIComponent(url.hash.slice(1));
  const stream = () => Object.assign(form, { network: q.get('type') || 'tcp', path: q.get('path') || '/', host: q.get('host') || '', serviceName: q.get('serviceName') || '', mode: q.get('mode') || 'auto', security: q.get('security') || 'none', serverName: q.get('sni') || '', fingerprint: q.get('fp') || 'chrome', alpn: q.get('alpn') || '', publicKey: q.get('pbk') || '', shortId: q.get('sid') || '', spiderX: q.get('spx') || '/', allowInsecure: q.get('allowInsecure') === '1' || q.get('insecure') === '1' });
  if (scheme === 'vless') { Object.assign(form, { protocol: 'vless', id: user, flow: q.get('flow') || '' }); stream(); }
  else if (scheme === 'trojan') { Object.assign(form, { protocol: 'trojan', password: user }); stream(); if (!q.get('security')) form.security = 'tls'; }
  else if (scheme === 'hysteria2' || scheme === 'hy2') { Object.assign(form, { protocol: 'hysteria', password: user || decodeURIComponent(url.password), security: 'tls', serverName: q.get('sni') || '', allowInsecure: q.get('insecure') === '1', alpn: q.get('alpn') || '' }); }
  else if (scheme === 'ss') {
    let method = '', password = '';
    try { [method, password] = (url.password ? [user, decodeURIComponent(url.password)] : atob(user.replace(/-/g, '+').replace(/_/g, '/')).split(/:(.*)/s)) as [string, string]; } catch { throw new Error(t('无法解析 Shadowsocks 链接。', 'Cannot parse the Shadowsocks link.')); }
    Object.assign(form, { protocol: 'shadowsocks', method, password });
  } else throw new Error(t('只支持 vless://、vmess://、trojan://、ss://、hysteria2:// 链接。', 'Only vless://, vmess://, trojan://, ss:// and hysteria2:// links are supported.'));
  Object.assign(form, { tag: name || form.protocol, address: host, port });
  return form;
}

// ---- 负载均衡（leastPing/leastLoad时自动维护observatory） ----
export const balancerStrategies: [string, string][] = [['random', t('随机', 'Random')], ['roundRobin', t('轮询', 'Round robin')], ['leastPing', t('最低延迟', 'Least ping')], ['leastLoad', t('最低负载', 'Least load')]];
export type BalancerForm = { tag: string; strategy: string; selector: string[]; fallbackTag: string };
export const emptyBalancer = (): BalancerForm => ({ tag: '', strategy: 'random', selector: [], fallbackTag: '' });

export function balancerForm(balancer: Json): BalancerForm {
  return { tag: str(balancer.tag), strategy: str(record(balancer.strategy).type) || 'random', selector: list(balancer.selector), fallbackTag: str(balancer.fallbackTag) };
}

export function balancerFromForm(form: BalancerForm): Json {
  if (!form.tag.trim() || /\s/.test(form.tag)) throw new Error(t('均衡标识需为非空白字符。', 'Balancer tag must be non-whitespace characters.'));
  if (!form.selector.length) throw new Error(t('至少选择一个出站。', 'Select at least one outbound.'));
  return { tag: form.tag.trim(), selector: form.selector, ...(form.strategy && form.strategy !== 'random' ? { strategy: { type: form.strategy } } : {}), ...(form.fallbackTag ? { fallbackTag: form.fallbackTag } : {}) };
}

export function syncObservatory(parts: Parts, probeURL: string): Parts {
  const selectors = [...new Set(parts.balancers.filter(item => ['leastPing', 'leastLoad'].includes(str(record(item.strategy).type))).flatMap(item => list(item.selector)))];
  const advanced = { ...parts.advanced };
  if (selectors.length) advanced.observatory = { ...record(advanced.observatory), subjectSelector: selectors, probeURL, probeInterval: str(record(advanced.observatory).probeInterval) || '1m' };
  else delete advanced.observatory;
  return { ...parts, advanced };
}

// ---- DNS ----
export type DNSServerForm = { address: string; port: string; domains: string; expectIPs: string; skipFallback: boolean };
export const emptyDNSServer = (): DNSServerForm => ({ address: '', port: '', domains: '', expectIPs: '', skipFallback: false });
export const dnsPresets: [string, string][] = [['Google', 'https://dns.google/dns-query'], ['Cloudflare', 'https://1.1.1.1/dns-query'], ['Quad9', 'https://dns.quad9.net/dns-query'], ['AdGuard', 'https://dns.adguard-dns.com/dns-query'], [t('系统 DNS', 'System DNS'), 'localhost']];

export function dnsServerForm(server: unknown): DNSServerForm {
  if (typeof server === 'string') return { ...emptyDNSServer(), address: server };
  const value = record(server);
  return { address: str(value.address), port: str(value.port), domains: list(value.domains).join('\n'), expectIPs: list(value.expectIPs).join('\n'), skipFallback: value.skipFallback === true };
}

export function dnsServerFromForm(form: DNSServerForm): unknown {
  const address = form.address.trim();
  if (!address || /\s/.test(address)) throw new Error(t('请填写 DNS 地址。', 'Enter the DNS address.'));
  const domains = lines(form.domains), expectIPs = lines(form.expectIPs), port = form.port.trim();
  if (port && (!Number.isInteger(Number(port)) || Number(port) < 1 || Number(port) > 65535)) throw new Error(t('端口需为 1–65535。', 'Port must be 1–65535.'));
  if (!port && !domains.length && !expectIPs.length && !form.skipFallback) return address;
  return { address, ...(port ? { port: Number(port) } : {}), ...(domains.length ? { domains } : {}), ...(expectIPs.length ? { expectIPs } : {}), ...(form.skipFallback ? { skipFallback: true } : {}) };
}
