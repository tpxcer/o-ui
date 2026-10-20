import test from 'node:test';
import assert from 'node:assert/strict';
import { settingsStatusText } from '../src/settings-status.ts';

const now = 100000;
const status = { running: true, version: '26.7.28', workspaceRevision: 10, settingsRevision: 2, inbounds: 1 };
test('设置状态仅比较设置修订，不把保存或入站修订当连接验收', () => {
  assert.match(settingsStatusText(true, 2, status, now, now), /修订2当前已生效.*v26.7.28/);
  assert.match(settingsStatusText(true, 3, status, now, now), /修订3待应用.*修订2/);
  assert.match(settingsStatusText(true, 1, status, now, now), /已落后.*刷新/);
  assert.equal(settingsStatusText(true, 2, { ...status, workspaceRevision: 99 }, now, now), settingsStatusText(true, 2, status, now, now));
  assert.doesNotMatch(settingsStatusText(true, 2, status, now, now), /连接.*通过|全部配置/);
});
test('设置状态区分停止、异常、失联与离线演示', () => {
  assert.match(settingsStatusText(true, 2, { ...status, running: false }, now, now), /已停止.*未在运行/);
  assert.match(settingsStatusText(true, 2, { ...status, running: false, unexpectedExit: true }, now, now), /异常退出.*重新应用/);
  assert.match(settingsStatusText(true, 2, null, now, now), /未连接.*无法确认/);
  assert.match(settingsStatusText(false, 2, status, now, now), /离线.*不应用/);
});
test('旧状态、非法时刻和超前时钟不能冒充当前生效', () => {
  for (const at of [now - 10001, now + 5001, NaN, -Infinity]) {
    assert.match(settingsStatusText(true, 2, status, at, now), /已过期.*无法确认/);
    assert.doesNotMatch(settingsStatusText(true, 2, status, at, now), /当前已生效/);
  }
  for (const at of [now - 10000, now + 5000]) assert.match(settingsStatusText(true, 2, status, at, now), /当前已生效/);
});
