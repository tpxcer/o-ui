import { clientInboundIds, type Client } from './workspace.ts';
import type { Parts, Json } from './xray-config.ts';

// 配置标记：列出该客户端可能使用的代理出站，不代表某条实时连接已命中路由。
export function clientProxyTargets(client: Client, parts: Parts): string[] {
  const targets = new Set<string>();
  const isProxy = (tag: string, seen = new Set<string>()): boolean => {
    if (seen.has(tag)) return false;
    seen.add(tag);
    const outbound = parts.outbounds.find(item => item.tag === tag);
    if (!outbound) return false;
    if (['http', 'socks', 'vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria', 'wireguard'].includes(String(outbound.protocol))) return true;
    if (outbound.protocol !== 'freedom') return false;
    const proxy = outbound.proxySettings as Json | undefined;
    const socket = (outbound.streamSettings as Json | undefined)?.sockopt as Json | undefined;
    return [proxy?.tag, socket?.dialerProxy].some(dependency => typeof dependency === 'string' && isProxy(dependency, seen));
  };
  const add = (tag: unknown) => { if (typeof tag === 'string' && isProxy(tag)) targets.add(tag); };
  const route = (rule: Json) => {
    if (rule.outboundTag) { add(rule.outboundTag); return; }
    const balancer = parts.balancers.find(item => item.tag === rule.balancerTag);
    if (!balancer) return;
    const selectors = Array.isArray(balancer.selector) ? balancer.selector : [];
    parts.outbounds.filter(item => selectors.some(prefix => typeof prefix === 'string' && String(item.tag).startsWith(prefix))).forEach(item => add(item.tag));
    add(balancer.fallbackTag);
  };
  for (const inbound of clientInboundIds(client)) {
    let terminal = false;
    for (const rule of parts.rules) {
      if (Array.isArray(rule.user) && !rule.user.includes(`client-${client.id}`)) continue;
      if (Array.isArray(rule.inboundTag) && !rule.inboundTag.includes(`in-${inbound}`)) continue;
      route(rule);
      // 用户和入站之外的条件依赖实际连接，继续保留后续规则及默认出站的可能性。
      if (Object.keys(rule).every(key => ['type', 'user', 'inboundTag', 'outboundTag', 'balancerTag'].includes(key))) { terminal = true; break; }
    }
    if (!terminal) add(parts.outbounds[0]?.tag);
  }
  return [...targets];
}
