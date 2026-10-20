import test from 'node:test';
import assert from 'node:assert/strict';
import { workspaceStatusText } from '../src/workspace-status.ts';

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
