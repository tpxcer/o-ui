import { t } from './i18n.ts';
export type Sample = { upload: number; download: number };

export const samples: Sample[] = [
  { upload: 18, download: 15 }, { upload: 18, download: 15 },
  { upload: 20, download: 17 }, { upload: 105, download: 110 },
  { upload: 282, download: 290 }, { upload: 170, download: 185 },
  { upload: 98, download: 122 }, { upload: 102, download: 101 },
  { upload: 109, download: 92 }, { upload: 66, download: 58 },
  { upload: 14, download: 11 }, { upload: 15, download: 12 },
  { upload: 20, download: 15 }, { upload: 124, download: 130 },
  { upload: 238, download: 242 }, { upload: 152, download: 164 },
  { upload: 79, download: 123 }, { upload: 37, download: 146 },
  { upload: 12, download: 160 }, { upload: 9, download: 165 },
  { upload: 121, download: 172 }, { upload: 268, download: 174 },
];

export function percent(used: number, total: number): number {
  if (!Number.isFinite(used) || !Number.isFinite(total) || total <= 0) return 0;
  return Math.max(0, Math.min(100, (used / total) * 100));
}

export function resourceColor(value: number, normal: string): string {
  if (value >= 90) return 'var(--danger)';
  if (value >= 80) return 'var(--warning)';
  return normal;
}

export function linePath(values: readonly number[], width: number, height: number, max: number, smooth = false): string {
  if (values.length === 0 || max <= 0) return '';
  const points = values.map((value, index) => [
    (index / Math.max(1, values.length - 1)) * width,
    height - (Math.max(0, Math.min(value, max)) / max) * height,
  ]);
  const coordinate = (point: number[]) => `${point[0].toFixed(2)},${point[1].toFixed(2)}`;
  if (!smooth || points.length < 3) {
    return points.map((point, index) => `${index ? 'L' : 'M'}${coordinate(point)}`).join(' ');
  }
  const step = width / (points.length - 1);
  const slopes = points.slice(1).map((point, index) => (point[1] - points[index][1]) / step);
  const tangents = points.map((_, index) => {
    if (index === 0) return slopes[0];
    if (index === points.length - 1) return slopes.at(-1)!;
    const before = slopes[index - 1], after = slopes[index];
    return before * after <= 0 ? 0 : (2 * before * after) / (before + after);
  });
  return `M${coordinate(points[0])} ` + points.slice(1).map((point, index) => {
    const previous = points[index];
    return `C${coordinate([previous[0] + step / 3, previous[1] + tangents[index] * step / 3])} ${coordinate([point[0] - step / 3, point[1] - tangents[index + 1] * step / 3])} ${coordinate(point)}`;
  }).join(' ');
}

export const demo = {
  hostname: 'DEMO-SERVER',
  ipv4: '192.0.2.10',
  ipv6: '2001:db8::10',
  provider: t('演示服务商', 'Demo provider'),
  system: 'Debian 13 x86_64',
  location: t('美国 · 加利福尼亚州', 'United States · California'),
  network: t('演示网络', 'Demo network'),
  load: '0.12 / 0.16 / 0.18',
  trafficUsed: 208.6,
  trafficTotal: 2000,
  reset: t('2026年11月1日 00:00:00', 'Nov 1, 2026 00:00:00'),
};
