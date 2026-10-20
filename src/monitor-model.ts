import { locale, t } from './i18n.ts';
export type Capacity = { used: string; total: string; percent: number | null };
export type NetworkPoint = { at: string; upload: number | null; download: number | null };
export type SystemSnapshot = {
  at: string; cpu: number | null; memory: Capacity | null; swap: Capacity | null; disk: Capacity | null;
  host: { name: string; system: string; architecture: string; cpus: number; cpuModel: string; uptime: number } | null;
  load: [number, number, number] | null;
  network: { interfaces: string[]; sent: string; received: string; upload: number | null; download: number | null } | null;
  issues: string[]; history: NetworkPoint[];
};
export type CoreMetrics = {
  at: string; running: boolean; startedAt?: string;
  memory: string | null; threads: number | null; tcp: number | null; udp: number | null; issues: string[];
};

export function coreReadings(m: CoreMetrics | null, now = Date.now()): { values: string[]; note: string } {
  const empty = ['—', '—', '—', '—'];
  if (!m) return { values: empty, note: t('核心指标尚未就绪', 'Core metrics are not ready yet') };
  if (!m.running) return { values: empty, note: m.issues.join(t('；', '; ')) || t('核心已停止', 'Core is stopped') };
  if (!fresh(m.at, now)) return { values: empty, note: m.issues.join(t('；', '; ')) || t('核心采样已过期或尚未就绪', 'Core sample is stale or not ready') };
  const count = (n: number | null) => n !== null && Number.isSafeInteger(n) && n >= 0 ? String(n) : '—';
  return { values: [bytes(m.memory), count(m.threads), count(m.tcp), count(m.udp)], note: m.issues.join(t('；', '; ')) || `${t('采样', 'Sampled')} ${new Date(m.at).toLocaleTimeString(locale, { hour12: false })}` };
}

export function bytes(value: string | number | null): string {
  if (value === null || (typeof value === 'string' && !/^\d+$/.test(value))) return '—';
  let n = Number(value);
  if (!Number.isFinite(n) || n < 0) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i ? 2 : 0)} ${units[i]}`;
}
export function duration(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds) || seconds < 0) return '—';
  const n = Math.floor(seconds);
  if (n < 60) return t(`${n}秒`, `${n}s`);
  if (n < 3600) return t(`${Math.floor(n / 60)}分钟`, `${Math.floor(n / 60)}m`);
  if (n < 86400) return t(`${Math.floor(n / 3600)}小时${Math.floor(n % 3600 / 60)}分钟`, `${Math.floor(n / 3600)}h ${Math.floor(n % 3600 / 60)}m`);
  return t(`${Math.floor(n / 86400)}天${Math.floor(n % 86400 / 3600)}小时`, `${Math.floor(n / 86400)}d ${Math.floor(n % 86400 / 3600)}h`);
}
export function fresh(at: string, now = Date.now()): boolean {
  const age = now - Date.parse(at);
  return Number.isFinite(age) && age >= -5000 && age <= 10000;
}

// 横坐标按真实时间展开；缺失/乱序/长间隔只断线，不补零或拉伸样本。
export function networkPath(points: NetworkPoint[], direction: 'upload' | 'download', max: number, end: number): string {
  if (!Number.isFinite(end) || !Number.isFinite(max) || max <= 0) return '';
  const start = end - 180000;
  let previous: number | null = null;
  return points.map(point => {
    const at = Date.parse(point.at), value = point[direction];
    if (!Number.isFinite(at) || at < start || at > end || value === null || !Number.isFinite(value) || value < 0) { previous = null; return ''; }
    const connected = previous !== null && at > previous && at - previous <= 6000;
    const command = `${connected ? 'L' : 'M'}${((at - start) / 180000 * 900).toFixed(2)},${(170 - Math.min(value / max, 1) * 170).toFixed(2)}`;
    previous = at;
    return command;
  }).filter(Boolean).join(' ');
}
