import test from 'node:test';
import assert from 'node:assert/strict';
import { workspaceStatusChip, workspaceStatusText } from '../src/workspace-status.ts';
import { trafficChip } from '../src/traffic.ts';

const now = 100000;
const status = { running: true, version: '26.7.28', workspaceRevision: 3, settingsRevision: 9, inbounds: 1 };

test('入站客户端修订区分已应用、待应用与页面落后，不冒充节点可用', () => {
  assert.match(workspaceStatusText(true, 3, status, now, now), /修订3当前已应用.*v26.7.28/);
  assert.match(workspaceStatusText(true, 4, status, now, now), /修订4待应用.*修订3/);
  assert.match(workspaceStatusText(true, 2, status, now, now), /已落后.*重新读取/);
  assert.equal(workspaceStatusText(true, 3, status, now, now), workspaceStatusText(true, 3, { ...status, settingsRevision: 99 }, now, now));
  assert.doesNotMatch(workspaceStatusText(true, 3, status, now, now), /连接.*通过|全部配置|所有客户端.*可用/);
});
test('入站状态区分停止、异常、失联和离线，停止不冒充仍在应用', () => {
  assert.match(workspaceStatusText(true, 3, { ...status, running: false }, now, now), /已停止.*未在运行/);
  assert.match(workspaceStatusText(true, 3, { ...status, running: false, unexpectedExit: true }, now, now), /异常退出.*重新应用/);
  assert.match(workspaceStatusText(true, 3, null, now, now), /未连接.*无法确认/);
  assert.match(workspaceStatusText(false, 3, status, now, now), /离线.*不应用/);
});
test('入站状态超过10秒撤下已应用结论，非法时刻与时钟异常同样不肯定', () => {
  for (const at of [now - 10001, now + 5001, NaN, -Infinity]) {
    const text = workspaceStatusText(true, 3, status, at, now);
    assert.match(text, /已过期.*无法确认/);
    assert.doesNotMatch(text, /当前已应用/);
  }
  for (const at of [now - 10000, now + 5000]) assert.match(workspaceStatusText(true, 3, status, at, now), /当前已应用/);
});

test('列表顶部短标签与完整状态一致，颜色区分正常、等待与故障', () => {
  const now = Date.now(), status = { running: true, version: '26.7.28', workspaceRevision: 3, settingsRevision: 1, inbounds: 2 } as never;
  assert.deepEqual(workspaceStatusChip(true, 3, status, now, now), ['已生效', 'good']);
  assert.deepEqual(workspaceStatusChip(true, 4, status, now, now), ['待应用', 'warn']);
  assert.deepEqual(workspaceStatusChip(true, 2, status, now, now), ['页面已落后', 'warn']);
  assert.deepEqual(workspaceStatusChip(true, 3, null, now, now), ['状态未连接', 'warn']);
  assert.deepEqual(workspaceStatusChip(true, 3, status, now - 60_000, now), ['状态已过期', 'warn']);
  assert.deepEqual(workspaceStatusChip(true, 3, { ...(status as object), running: false } as never, now, now), ['核心未运行', 'muted']);
  assert.deepEqual(workspaceStatusChip(true, 3, { ...(status as object), running: false, unexpectedExit: true } as never, now, now), ['核心异常退出', 'bad']);
  const snapshot = { totals: {}, sampledAt: new Date(now).toISOString(), collecting: true, intervalSeconds: 2, issue: '', incomplete: false };
  assert.deepEqual(trafficChip(snapshot, '', now), ['流量采集中', 'good']);
  assert.deepEqual(trafficChip(snapshot, '超时', now), ['流量读取失败', 'bad']);
  assert.deepEqual(trafficChip({ ...snapshot, collecting: false }, '', now), ['流量未采集', 'muted']);
  assert.deepEqual(trafficChip(snapshot, '', now + 11_000), ['流量采样过旧', 'warn']);
  assert.deepEqual(trafficChip({ ...snapshot, incomplete: true }, '', now), ['流量采集中', 'warn']);
  assert.deepEqual(trafficChip(null, '', now), ['正在读取流量', 'muted']);
});
