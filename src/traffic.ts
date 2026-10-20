import { locale, t } from './i18n.ts';
export interface TrafficSnapshot {
  totals: Record<string, { uplink: string; downlink: string }>;
  sampledAt: string | null; collecting: boolean; intervalSeconds: number; issue: string; incomplete: boolean;
}

export function usedBytes(snapshot: TrafficSnapshot | null, id: string): bigint | null {
  if (!snapshot?.sampledAt) return null;
  const count = snapshot.totals[id];
  if (!count) return 0n;
  if (!/^\d+$/.test(count.uplink) || !/^\d+$/.test(count.downlink)) return null;
  return BigInt(count.uplink) + BigInt(count.downlink);
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
