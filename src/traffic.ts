import { locale, t } from './i18n.ts';
export type TrafficPeriod = 'total' | 'day' | 'week' | 'month';
export interface PeriodTraffic {
  start: string; end: string; since: string; partial: boolean;
  totals: Record<string, { uplink: string; downlink: string }>;
}
export interface TrafficSnapshot {
  totals: Record<string, { uplink: string; downlink: string }>;
  periods?: Partial<Record<Exclude<TrafficPeriod, 'total'>, PeriodTraffic>> | null;
  lastOnline?: Record<string, string> | null;
  sampledAt: string | null; collecting: boolean; intervalSeconds: number; issue: string; incomplete: boolean;
}

export function usedBytes(snapshot: TrafficSnapshot | null, id: string, period: TrafficPeriod = 'total'): bigint | null {
  if (!snapshot?.sampledAt) return null;
  const totals = period === 'total' ? snapshot.totals : snapshot.periods?.[period]?.totals;
  if (!totals) return null;
  const count = totals[id];
  if (!count) return 0n;
  if (!/^\d+$/.test(count.uplink) || !/^\d+$/.test(count.downlink)) return null;
  return BigInt(count.uplink) + BigInt(count.downlink);
}

export function trafficPeriodHint(snapshot: TrafficSnapshot | null, period: TrafficPeriod): string {
  if (period === 'total') return t('累计用量用于流量限额判断。', 'Lifetime usage is used for quota enforcement.');
  const p = snapshot?.periods?.[period];
  if (!p) return t('周期统计尚未开始；首次采样后起算。', 'Period statistics start after the first sample.');
  // 保留后端时间及其偏移量，不能把浏览器时区误称为服务器时区。
  const serverTime = (value: string) => value.replace('T', ' ').replace(/\.\d+/, '');
  return `${t('服务器周期', 'Server period')} ${serverTime(p.start)} → ${serverTime(p.end)}${p.partial ? ` · ${t('部分统计，起算', 'Partial statistics, started')} ${serverTime(p.since)}` : ''} · ${t('按采样入账，边界可能有约2秒偏差；仅重置统计，不影响累计限额。', 'Recorded at sample time, boundaries may differ by about 2 seconds; only statistics reset, lifetime quotas are unchanged.')}`;
}

export function formatTrafficGB(bytes: bigint | null): string {
  if (bytes === null) return t('未采集', 'Not collected');
  const decimal = (bytes % 1_000_000_000n).toString().padStart(9, '0').replace(/0+$/, '');
  return `${bytes / 1_000_000_000n}${decimal ? `.${decimal}` : ''}`;
}

export function trafficStatus(snapshot: TrafficSnapshot | null, error = '', now = Date.now()): string {
  if (error) return `${t('流量读取失败：', 'Traffic read failed: ')}${error}${t('；保留上次累计，不视为零用量。', '; previous totals are kept and not treated as zero usage.')}`;
  if (!snapshot) return t('正在读取真实流量累计…', 'Reading traffic totals…');
  const sampled = snapshot.sampledAt ? Date.parse(snapshot.sampledAt) : NaN;
  const state = snapshot.collecting ? Number.isFinite(sampled) && now - sampled <= 10_000 ? t('正在采集', 'Collecting') : t('采样已过旧', 'Sample is stale') : t('未采集（核心停止或统计关闭）', 'Not collected (core stopped or statistics off)');
  return `${state} · ${Number.isFinite(sampled) ? `${t('最后采样', 'Last sample')} ${new Date(sampled).toLocaleString(locale)}` : t('尚无采样', 'No samples yet')}${snapshot.issue ? ` · ${snapshot.issue}` : ''}${snapshot.incomplete ? t(' · 历史存在未知尾流，累计可能不完整', ' · history has unmeasured traffic, totals may be incomplete') : ''}`;
}

// 流量按大小自动选单位（十进制，与流量上限的GB一致）：B、KB、MB、GB、TB。
export function formatBytes(bytes: bigint | number | null): string {
  if (bytes === null) return t('未采集', 'Not collected');
  const n = Number(bytes);
  if (!Number.isFinite(n) || n < 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = n, unit = 0;
  while (value >= 1000 && unit < units.length - 1) { value /= 1000; unit++; }
  const fixed = unit === 0 ? String(Math.round(value)) : value < 10 ? value.toFixed(2) : value < 100 ? value.toFixed(1) : value.toFixed(0);
  const text = fixed.includes('.') ? fixed.replace(/\.?0+$/, '') : fixed;
  return `${text} ${units[unit]}`;
}

// OUI的在线判定：最近2分钟内有流量。
export const onlineWindowMs = 2 * 60 * 1000;

export function trafficSpeedVisible(online: boolean, rate?: { up: number; down: number }): boolean {
  return online && !!rate && Number.isFinite(rate.up) && Number.isFinite(rate.down) && rate.up >= 0 && rate.down >= 0;
}

// 相对时间：刚刚、N分钟前、N小时前、N天前。
export function relativeTime(at: number, now = Date.now()): string {
  const minutes = Math.floor(Math.max(0, now - at) / 60000);
  if (minutes < 1) return t('刚刚', 'just now');
  if (minutes < 60) return `${minutes} ${t('分钟前', 'min ago')}`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} ${t('小时前', 'h ago')}`;
  return `${Math.floor(hours / 24)} ${t('天前', 'd ago')}`;
}

// 到期剩余：N天后 / N小时后 / 已到期。
export function expiryLeft(expiresAt: number, now = Date.now()): string {
  const left = expiresAt - now;
  if (left <= 0) return t('已到期', 'Expired');
  const hours = left / 3600000;
  if (hours < 24) return `${Math.max(1, Math.ceil(hours))} ${t('小时后到期', 'h left')}`;
  return `${Math.ceil(hours / 24)} ${t('天后到期', 'days left')}`;
}
