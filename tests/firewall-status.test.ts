import test from 'node:test';
import assert from 'node:assert/strict';
import { firewallStatusText, oldFirewallCleanupAllowed } from '../src/firewall-status.ts';

test('防火墙区分未确认、失败和上次成功，不称公网连接已通过', () => {
  assert.match(firewallStatusText(), /未确认/);
  assert.match(firewallStatusText({ state: 'unverified', message: '', checkedAt: '' }), /未确认/);
  assert.match(firewallStatusText({ state: 'failed', message: '', checkedAt: '' }), /失败/);
  const allowed = firewallStatusText({ state: 'allowed', message: '', checkedAt: '' });
  assert.match(allowed, /上次放行成功/);
  assert.doesNotMatch(allowed, /连通|可用|公网.*成功/);
});
test('旧端口只允许重试放行规则撤销，不把转发或其他操作当作已清理', () => {
  const status = { state: 'failed' as const, message: '', checkedAt: '' };
  assert.equal(oldFirewallCleanupAllowed(status), true);
  assert.equal(oldFirewallCleanupAllowed({ ...status, operation: 'delete' }), true);
  for (const operation of ['allow', 'redirect-add', 'redirect-delete'] as const) assert.equal(oldFirewallCleanupAllowed({ ...status, operation }), false);
  for (const message of ['端口跳跃转发操作失败：旧记录', 'Port hopping update failed: legacy']) assert.equal(oldFirewallCleanupAllowed({ ...status, message }), false);
});
