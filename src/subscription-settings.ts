import { t } from './i18n.ts';

// 订阅全局设置模型（参照OUI订阅页）；单个客户端的订阅链接仍在连接信息中生成。
export interface SubscriptionSettings {
  enabled: boolean; updateHours: number; title: string; showInfo: boolean; remarkClientName: boolean;
  clashEnabled: boolean; jsonEnabled: boolean; jsonFragment: boolean; fragmentPackets: string; fragmentLength: string; fragmentInterval: string;
  jsonDirectIP: string[]; jsonDirectDomain: string[];
}
export interface SubscriptionStatus { revision: number; data: SubscriptionSettings }
export interface SubscriptionForm {
  enabled: boolean; title: string; updateHours: string; showInfo: boolean; remarkClientName: boolean;
  clashEnabled: boolean; jsonEnabled: boolean; jsonFragment: boolean; fragmentPackets: string; fragmentLength: string; fragmentInterval: string;
  jsonDirectIP: string; jsonDirectDomain: string;
}

export function subscriptionForm(data: SubscriptionSettings): SubscriptionForm {
  return {
    enabled: data.enabled, title: data.title, updateHours: String(data.updateHours), showInfo: data.showInfo, remarkClientName: data.remarkClientName,
    clashEnabled: data.clashEnabled, jsonEnabled: data.jsonEnabled, jsonFragment: data.jsonFragment, fragmentPackets: data.fragmentPackets, fragmentLength: data.fragmentLength, fragmentInterval: data.fragmentInterval,
    jsonDirectIP: (data.jsonDirectIP ?? []).join('\n'), jsonDirectDomain: (data.jsonDirectDomain ?? []).join('\n'),
  };
}

const range = /^[0-9]{1,5}(-[0-9]{1,5})?$/;
const direct = /^(?:geoip:|geosite:|domain:|full:|keyword:)?[A-Za-z0-9][A-Za-z0-9._:/@!-]{0,127}$/;
const lines = (text: string) => text.split('\n').map(line => line.trim()).filter(Boolean);

export function subscriptionRequest(form: SubscriptionForm): SubscriptionSettings {
  const hoursText = form.updateHours.trim(), hours = Number(hoursText);
  if (!/^\d+$/.test(hoursText) || hours < 1 || hours > 168) throw new Error(t('订阅更新间隔须为1至168小时', 'Subscription update interval must be 1–168 hours'));
  const title = form.title.trim();
  if ([...title].length > 64 || /[\u0000-\u001f\u007f]/.test(title)) throw new Error(t('订阅标题最多64个字符，不能包含控制字符', 'Subscription title must be at most 64 characters without control characters'));
  const packets = form.fragmentPackets.trim(), length = form.fragmentLength.trim(), interval = form.fragmentInterval.trim();
  if (packets !== 'tlshello' && !range.test(packets) || !range.test(length) || !range.test(interval)) throw new Error(t('分片参数须为数字或“最小-最大”范围（packets另可为tlshello）', 'Fragment values must be a number or a min-max range (packets may also be tlshello)'));
  const directIP = lines(form.jsonDirectIP), directDomain = lines(form.jsonDirectDomain);
  if (directIP.length > 64 || directDomain.length > 64) throw new Error(t('直连规则每组最多64项', 'Each direct rule list allows at most 64 entries'));
  if (![...directIP, ...directDomain].every(entry => direct.test(entry))) throw new Error(t('直连规则只能填写geoip:/geosite:、IP/CIDR或域名', 'Direct rules accept only geoip:/geosite: entries, IP/CIDR or domains'));
  return {
    enabled: form.enabled, updateHours: hours, title, showInfo: form.showInfo, remarkClientName: form.remarkClientName,
    clashEnabled: form.clashEnabled, jsonEnabled: form.jsonEnabled, jsonFragment: form.jsonFragment, fragmentPackets: packets, fragmentLength: length, fragmentInterval: interval,
    jsonDirectIP: directIP, jsonDirectDomain: directDomain,
  };
}
