import { locale, t } from './i18n.ts';
export type Billing = 'both' | 'upload' | 'max';
export interface ServerPlan { node: string; name: string; resources: string; quotaGB: number; resetDay: number; billing: Billing; interfaces: string[] }
export interface PlanUsage { periodStart: string; nextReset: string; sent: string; received: string; used: string; quota: string; inexact: boolean; at: string }
export interface PlanStatus { revision: number; data: ServerPlan | null; usage: PlanUsage | null; issue: string }
export interface PublicAddress { ip: string; hostname: string; location: string; country: string; org: string }
export interface PublicInfo { checkedAt: string; ipv4: PublicAddress | null; ipv6: PublicAddress | null; issues: string[] }

export const billingLabels: Record<Billing, string> = { both: t('上传下载合计', 'Upload + download'), upload: t('仅上传（出站）', 'Upload only (outbound)'), max: t('上传下载取较大值', 'Larger of upload and download') };

// 套餐用量按服务商常用的十进制单位：1 GB = 10亿字节。
export function decimalBytes(value: string | null | undefined): string {
  if (!value || !/^\d+$/.test(value)) return '—';
  const n = Number(value);
  if (n >= 1e12) return `${(n / 1e12).toFixed(2)} TB`;
  return `${(n / 1e9).toFixed(2)} GB`;
}

export function allowance(usage: PlanUsage): { used: string; total: string; percent: number | null; reset: string } {
  const used = Number(usage.used), quota = Number(usage.quota);
  const valid = /^\d+$/.test(usage.used) && /^\d+$/.test(usage.quota);
  const reset = Number.isNaN(Date.parse(usage.nextReset)) ? '—' : new Date(usage.nextReset).toLocaleString(locale, { hour12: false, year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' });
  return {
    used: decimalBytes(usage.used),
    total: valid && quota > 0 ? decimalBytes(usage.quota) : t('不限', 'Unlimited'),
    percent: valid && quota > 0 ? Math.min(100, used / quota * 100) : null,
    reset,
  };
}

// 入站名称前缀：按公网信息的国家代码显示旗帜与中文国家名，查询不到时不冒充。
export function regionLabel(info: PublicInfo | null): string {
  const code = info?.ipv4?.country || info?.ipv6?.country || '';
  if (!/^[A-Z]{2}$/.test(code)) return t('未知地区', 'Unknown region');
  let name = code;
  try { name = new Intl.DisplayNames([locale], { type: 'region' }).of(code) ?? code; } catch { /* 无区域数据时显示代码。 */ }
  return `${String.fromCodePoint(...[...code].map(c => 0x1f1e6 + c.charCodeAt(0) - 65))} ${name}`;
}

export function planTags(resources: string): string[] {
  return resources.split(/[,，、;；]/).map(item => item.trim()).filter(Boolean).slice(0, 8);
}

// 与后端校验一致；后端仍是最终校验。
export function parsePlanForm(values: Record<string, string>): { plan: ServerPlan; usedGB: number | null } {
  const text = (key: string) => (values[key] ?? '').trim();
  for (const key of ['node', 'name', 'resources']) {
    if ([...text(key)].length > 120 || /[\u0000-\u001f\u007f]/.test(text(key))) throw new Error(t('节点、套餐名称和资源说明各限120字，且不能包含控制字符', 'Node, plan name and resources are limited to 120 characters each without control characters'));
  }
  const quotaGB = Number(text('quotaGB') || 'NaN');
  if (!Number.isFinite(quotaGB) || quotaGB < 0 || quotaGB > 1000000) throw new Error(t('月流量上限须为0至1000000 GB，0表示不限', 'Monthly quota must be 0–1000000 GB (0 means unlimited)'));
  const resetDay = Number(text('resetDay'));
  if (!Number.isInteger(resetDay) || resetDay < 1 || resetDay > 31) throw new Error(t('每月重置日须为1至31；当月没有该日期时按月末重置', 'Monthly reset day must be 1–31; months without that day reset on their last day'));
  const billing = text('billing') as Billing;
  if (!(billing in billingLabels)) throw new Error(t('计费方向无效', 'Invalid billing direction'));
  const interfaces = text('interfaces').split(/[\s,，]+/).filter(Boolean);
  if (interfaces.length > 8 || interfaces.some(name => !/^[A-Za-z0-9_.:@-]{1,15}$/.test(name)) || new Set(interfaces).size !== interfaces.length) throw new Error(t('计量网卡最多8个，名称只能含字母、数字及 _ . : @ -，且不能重复', 'At most 8 metered interfaces; names may contain only letters, digits and _ . : @ - without duplicates'));
  let usedGB: number | null = null;
  if (text('usedGB')) {
    usedGB = Number(text('usedGB'));
    if (!Number.isFinite(usedGB) || usedGB < 0 || usedGB > 1000000) throw new Error(t('本周期已用校准值须为0至1000000 GB', 'Usage calibration must be 0–1000000 GB'));
  }
  return { plan: { node: text('node'), name: text('name'), resources: text('resources'), quotaGB, resetDay, billing, interfaces }, usedGB };
}
