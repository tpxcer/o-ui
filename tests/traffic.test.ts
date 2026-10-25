import test from 'node:test';
import assert from 'node:assert/strict';
import { formatTrafficGB, inboundTrafficPeriod, trafficPeriodHint, trafficSpeedVisible, trafficStatus, usedBytes } from '../src/traffic.ts';
import type { TrafficSnapshot } from '../src/traffic.ts';
const snapshot: TrafficSnapshot = { totals: { one: { uplink: '9007199254740993', downlink: '1' } }, sampledAt: new Date(1000).toISOString(), collecting: true, intervalSeconds: 2, issue: '', incomplete: false };
test('每个节点独立选择统计周期，旧节点缺省不重置，不改累计限额来源', () => {
  const daily = { trafficReset: 'daily' as const }, monthly = { trafficReset: 'monthly' as const };
  assert.equal(inboundTrafficPeriod(daily), 'day');
  assert.equal(inboundTrafficPeriod(monthly), 'month');
  assert.equal(inboundTrafficPeriod({ trafficReset: 'weekly' }), 'week');
  assert.equal(inboundTrafficPeriod({ trafficReset: 'never' }), 'total');
  assert.equal(inboundTrafficPeriod({}), 'total');
  assert.equal(inboundTrafficPeriod(), 'total');
  assert.equal(usedBytes(snapshot, 'one'), 9007199254740994n);
});
test('周期统计与累计独立，缺失周期不冒充零用量', () => {
  const periodic: TrafficSnapshot = { ...snapshot, periods: { day: { start: '2026-10-25T00:00:00+08:00', end: '2026-10-26T00:00:00+08:00', since: '2026-10-25T12:00:00+08:00', partial: true, totals: { one: { uplink: '2', downlink: '3' } } } } };
  assert.equal(usedBytes(periodic, 'one', 'day'), 5n);
  assert.equal(usedBytes(periodic, 'new', 'day'), 0n);
  assert.equal(usedBytes(periodic, 'one'), 9007199254740994n);
  assert.equal(usedBytes(periodic, 'one', 'week'), null);
  assert.equal(usedBytes(snapshot, 'one', 'day'), null);
  assert.match(trafficPeriodHint(periodic, 'day'), /部分统计.*不影响累计限额/);
  assert.ok(trafficPeriodHint(periodic, 'day').includes('2026-10-25 00:00:00+08:00'));
  assert.match(trafficPeriodHint(snapshot, 'day'), /尚未开始/);
});
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

test('离线不显示上传下载速度，在线仅显示有效速率，在线空闲仍允许零值', () => {
  assert.equal(trafficSpeedVisible(false, { up: 0, down: 0 }), false);
  assert.equal(trafficSpeedVisible(false, { up: 100, down: 200 }), false);
  assert.equal(trafficSpeedVisible(true), false);
  assert.equal(trafficSpeedVisible(true, { up: NaN, down: 0 }), false);
  assert.equal(trafficSpeedVisible(true, { up: 0, down: -1 }), false);
  assert.equal(trafficSpeedVisible(true, { up: 0, down: 0 }), true);
  assert.equal(trafficSpeedVisible(true, { up: 100, down: 200 }), true);
});
