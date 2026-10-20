import test from 'node:test';
import assert from 'node:assert/strict';
import { demo, linePath, percent, resourceColor, samples } from '../src/model.ts';

test('资源和流量百分比限制在有效范围内', () => {
  assert.equal(percent(1, 4), 25);
  assert.equal(percent(-1, 4), 0);
  assert.equal(percent(10, 4), 100);
  assert.equal(percent(1, 0), 0);
  assert.equal(percent(Number.NaN, 4), 0);
  assert.equal(percent(1, Number.POSITIVE_INFINITY), 0);
  assert.equal(percent(demo.trafficUsed, demo.trafficTotal).toFixed(1), '10.4');
});

test('资源 80% 警告、90% 危险，不能一直显示正常色', () => {
  assert.equal(resourceColor(79, 'blue'), 'blue');
  assert.equal(resourceColor(80, 'blue'), 'var(--warning)');
  assert.equal(resourceColor(90, 'blue'), 'var(--danger)');
});

test('趋势路径支持空数据和单点，不复制样本来填充历史', () => {
  assert.equal(linePath([], 100, 50, 100), '');
  assert.equal(linePath([25], 100, 50, 100), 'M0.00,37.50');
  assert.equal(linePath([0, 100], 100, 50, 100), 'M0.00,50.00 L100.00,0.00');
  assert.equal(linePath([0, 200], 100, 50, 100), 'M0.00,50.00 L100.00,0.00');
  assert.equal(samples.length, 22);
});

test('演示使用文档保留地址，不包含生产主机数据', () => {
  assert.equal(demo.ipv4, '192.0.2.10');
  assert.equal(demo.ipv6, '2001:db8::10');
  assert.equal(demo.hostname, 'DEMO-SERVER');
});

test('平滑趋势保留端点与峰值，不越过相邻样本范围', () => {
  assert.equal(linePath([0, 100, 0], 100, 50, 100, true),
    'M0.00,50.00 C16.67,33.33 33.33,0.00 50.00,0.00 C66.67,0.00 83.33,33.33 100.00,50.00');
  const flat = linePath([0, 0, 0], 100, 50, 100, true);
  assert.ok(!flat.includes('NaN'));
  assert.ok(flat.endsWith('100.00,50.00'));
});
