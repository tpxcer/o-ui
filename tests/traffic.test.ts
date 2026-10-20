import test from 'node:test';
import assert from 'node:assert/strict';
import { formatTrafficGB, trafficStatus, usedBytes } from '../src/traffic.ts';
import type { TrafficSnapshot } from '../src/traffic.ts';
const snapshot: TrafficSnapshot = { totals: { one: { uplink: '9007199254740993', downlink: '1' } }, sampledAt: new Date(1000).toISOString(), collecting: true, intervalSeconds: 2, issue: '', incomplete: false };
test('真实累计用十进制字符串和BigInt避免精度丢失，未知不当作0', () => {
  assert.equal(usedBytes(snapshot, 'one'), 9007199254740994n);
  assert.equal(usedBytes(snapshot, 'new'), 0n);
  assert.equal(usedBytes(null, 'one'), null);
  assert.equal(formatTrafficGB(9007199254740994n), '9007199.254740994');
  assert.equal(formatTrafficGB(1n), '0.000000001');
  assert.equal(formatTrafficGB(null), '未采集');
});
test('采集、停止、过旧、错误及历史缺口均明确显示', () => {
  assert.match(trafficStatus(snapshot, '', 2000), /正在采集/);
  assert.match(trafficStatus(snapshot, '', 20000), /过旧/);
  assert.match(trafficStatus({ ...snapshot, collecting: false }), /停止或统计关闭/);
  assert.match(trafficStatus({ ...snapshot, incomplete: true }), /可能不完整/);
  assert.match(trafficStatus(snapshot, '断线'), /不视为零/);
});
