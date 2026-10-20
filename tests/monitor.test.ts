import test from 'node:test';
import assert from 'node:assert/strict';
import { bytes, duration, fresh, networkPath, coreReadings, type CoreMetrics } from '../src/monitor-model.ts';

test('核心指标区分真实零、不可用、停止和过期，不冒充设备数', () => {
  const now = Date.now();
  const m: CoreMetrics = { at: new Date(now).toISOString(), running: true, memory: '1024', threads: 7, tcp: 0, udp: 1, issues: [] };
  assert.deepEqual(coreReadings(m, now).values, ['1.00 KiB', '7', '0', '1']);
  assert.deepEqual(coreReadings(null, now).values, ['—', '—', '—', '—']);
  assert.deepEqual(coreReadings({ ...m, running: false }, now).values, ['—', '—', '—', '—']);
  assert.deepEqual(coreReadings(m, now + 11000).values, ['—', '—', '—', '—']);
  const invalid = coreReadings({ ...m, memory: null, threads: -1, tcp: NaN, udp: 0.5, issues: ['权限不足'] }, now);
  assert.deepEqual(invalid.values, ['—', '—', '—', '—']);
  assert.equal(invalid.note, '权限不足');
});

test('资源显示不把缺失当零，字节单位一致', () => {
  assert.equal(bytes(null), '—'); assert.equal(bytes('bad'), '—');
  assert.equal(bytes(Infinity), '—'); assert.equal(bytes(-1), '—');
  assert.equal(bytes('0'), '0 B'); assert.equal(bytes('1024'), '1.00 KiB');
  assert.equal(bytes('18446744073709551615'), '16.00 EiB');
  assert.equal(duration(null), '—'); assert.equal(duration(0), '0秒');
  assert.equal(duration(3660), '1小时1分钟'); assert.equal(duration(90000), '1天1小时');
});
test('时间戳区分新鲜、过期、无效及未来漂移', () => {
  const now = Date.now();
  assert.ok(fresh(new Date(now - 2000).toISOString(), now));
  assert.ok(!fresh(new Date(now - 11000).toISOString(), now));
  assert.ok(!fresh('0001-01-01T00:00:00Z', now));
  assert.ok(!fresh('invalid', now)); assert.ok(!fresh(new Date(now + 10000).toISOString(), now));
});
test('网速图按时间坐标、不复制点、缺样本和长间隔断线', () => {
  const end = Date.parse('2026-10-18T10:00:00Z');
  const point = (offset:number, value:number|null) => ({at:new Date(end+offset).toISOString(),upload:value,download:value});
  assert.equal(networkPath([], 'upload', 100, end), '');
  assert.equal(networkPath([point(0,50)], 'upload', 100, end), 'M900.00,85.00');
  assert.equal(networkPath([point(-4000,0),point(-2000,null),point(0,50)], 'upload', 100, end), 'M880.00,170.00 M900.00,85.00');
  assert.equal(networkPath([point(-2000,0),point(0,50)], 'upload', 100, end), 'M890.00,170.00 L900.00,85.00');
  assert.equal(networkPath([point(-8000,0),point(0,50)], 'upload', 100, end), 'M860.00,170.00 M900.00,85.00');
  assert.equal(networkPath([point(-181000,100)], 'upload', 100, end), '');
  assert.equal(networkPath([point(0,NaN)], 'upload', 100, end), '');
});
