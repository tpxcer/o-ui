import test from 'node:test';
import assert from 'node:assert/strict';
import { firewallStatusText } from '../src/firewall-status.ts';

test('防火墙区分未确认、失败和上次成功，不称公网连接已通过', () => {
  assert.match(firewallStatusText(), /未确认/);
  assert.match(firewallStatusText({ state: 'unverified', message: '', checkedAt: '' }), /未确认/);
  assert.match(firewallStatusText({ state: 'failed', message: '', checkedAt: '' }), /失败/);
  const allowed = firewallStatusText({ state: 'allowed', message: '', checkedAt: '' });
  assert.match(allowed, /上次放行成功/);
  assert.doesNotMatch(allowed, /连通|可用|公网.*成功/);
});
