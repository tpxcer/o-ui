import test from 'node:test';
import assert from 'node:assert/strict';
import { runtimeHistoryText, runtimeStatusText } from '../src/runtime.ts';

test('核心异常退出不冒充正常停止或自动恢复', () => {
  const status = { running: false, workspaceRevision: 1, settingsRevision: 1, inbounds: 1 };
  assert.match(runtimeStatusText(null), /未连接/);
  assert.match(runtimeStatusText(status), /已停止/);
  assert.match(runtimeStatusText({ ...status, unexpectedExit: true }), /异常退出.*未自动重启/);
  assert.match(runtimeStatusText({ ...status, running: true, version: '26.7.28' }), /运行中 v26.7.28/);
});

test('历史只显示已应用摘要，区分缺失、损坏与空有效配置', () => {
  assert.match(runtimeHistoryText(null), /未读取.*不能恢复/);
  const empty = { revision: 0, available: false, workspaceRevision: 0, settingsRevision: 0, inbounds: 0 };
  assert.match(runtimeHistoryText(empty), /尚无成功应用/);
  assert.equal(runtimeHistoryText({ ...empty, issue: '历史文件已改变，拒绝恢复' }), '历史文件已改变，拒绝恢复');
  const last = { ...empty, version: '26.7.28', workspaceRevision: 3, settingsRevision: 2, appliedAt: '2026-10-18T01:00:00Z' };
  assert.match(runtimeHistoryText(last), /v26.7.28.*入站修订 3／设置修订 2.*无可恢复入站/);
  assert.doesNotMatch(runtimeHistoryText({ ...last, available: true, inbounds: 1 }), /无可恢复/);
  assert.match(runtimeHistoryText({ ...last, appliedAt: 'invalid' }), /时间未知/);
});
