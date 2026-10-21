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
export function networkSegments(points: NetworkPoint[], direction: 'upload' | 'download', max: number, end: number): [number, number][][] {
  if (!Number.isFinite(end) || !Number.isFinite(max) || max <= 0) return [];
  const start = end - 180000, segments: [number, number][][] = [];
  let previous: number | null = null;
  for (const point of points) {
    const at = Date.parse(point.at), value = point[direction];
    if (!Number.isFinite(at) || at < start || at > end || value === null || !Number.isFinite(value) || value < 0) { previous = null; continue; }
    if (previous === null || at <= previous || at - previous > 6000) segments.push([]);
    segments[segments.length - 1].push([(at - start) / 180000 * 900, 170 - Math.min(value / max, 1) * 170]);
    previous = at;
  }
  return segments;
}

// 三个及以上相连样本用单调三次曲线（同d3 curveMonotoneX）：线条平滑，但不会越过相邻样本的高低，不画出虚高峰值或负速度。
function smooth(segment: [number, number][]): string {
  const f = (n: number) => n.toFixed(2), [x0, y0] = segment[0];
  if (segment.length < 3) return `M${f(x0)},${f(y0)}${segment.length === 2 ? ` L${f(segment[1][0])},${f(segment[1][1])}` : ''}`;
  const slopes = segment.slice(1).map(([x, y], i) => (y - segment[i][1]) / (x - segment[i][0]));
  const tangents = segment.map((_, i) => {
    if (i === 0) return slopes[0];
    if (i === segment.length - 1) return slopes[i - 1];
    const [s0, s1] = [slopes[i - 1], slopes[i]], h0 = segment[i][0] - segment[i - 1][0], h1 = segment[i + 1][0] - segment[i][0];
    return (Math.sign(s0) + Math.sign(s1)) * Math.min(Math.abs(s0), Math.abs(s1), .5 * Math.abs((s0 * h1 + s1 * h0) / (h0 + h1))) || 0;
  });
  return `M${f(x0)},${f(y0)}` + segment.slice(1).map(([x, y], i) => {
    const [px, py] = segment[i], dx = (x - px) / 3;
    return ` C${f(px + dx)},${f(py + dx * tangents[i])} ${f(x - dx)},${f(y - dx * tangents[i + 1])} ${f(x)},${f(y)}`;
  }).join('');
}

export function networkPath(points: NetworkPoint[], direction: 'upload' | 'download', max: number, end: number): string {
  return networkSegments(points, direction, max, end).map(smooth).join(' ');
}

// 曲线下方的面积（每段各自闭合到底边），配合渐变填充。
export function networkArea(points: NetworkPoint[], direction: 'upload' | 'download', max: number, end: number): string {
  return networkSegments(points, direction, max, end).filter(segment => segment.length > 1)
    .map(segment => `${smooth(segment)} L${segment[segment.length - 1][0].toFixed(2)},170 L${segment[0][0].toFixed(2)},170 Z`).join(' ');
}
