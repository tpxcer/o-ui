import test from 'node:test';
import assert from 'node:assert/strict';
import { allowance, decimalBytes, parsePlanForm, planTags, regionLabel } from '../src/server-plan-model.ts';

const form = { node: ' LA ', name: 'KVM', resources: 'RAM 2 GB, Disk 40 GB', quotaGB: '2000', resetDay: '31', billing: 'both', interfaces: 'eth0, ens3', usedGB: '' };

test('套餐用量按十进制GB/TB显示，不限额不显示百分比', () => {
  assert.equal(decimalBytes('208600000000'), '208.60 GB');
  assert.equal(decimalBytes('2000000000000'), '2.00 TB');
  assert.equal(decimalBytes('bad'), '—'); assert.equal(decimalBytes(null), '—');
  const usage = { periodStart: '2026-10-01T00:00:00+08:00', nextReset: '2026-11-01T00:00:00+08:00', sent: '1', received: '1', used: '208600000000', quota: '2000000000000', inexact: false, at: '2026-10-19T00:00:00Z' };
  const limited = allowance(usage);
  assert.equal(limited.used, '208.60 GB'); assert.equal(limited.total, '2.00 TB');
  assert.ok(Math.abs(limited.percent! - 10.43) < 1e-9);
  assert.notEqual(limited.reset, '—');
  const unlimited = allowance({ ...usage, quota: '0' });
  assert.equal(unlimited.total, '不限'); assert.equal(unlimited.percent, null);
  assert.equal(allowance({ ...usage, used: '5000000000000' }).percent, 100);
  assert.equal(allowance({ ...usage, nextReset: 'invalid' }).reset, '—');
});

test('套餐资源按常用分隔符拆成标签并限量', () => {
  assert.deepEqual(planTags('RAM 2 GB， Disk 40 GB、 ,IPv4 1;'), ['RAM 2 GB', 'Disk 40 GB', 'IPv4 1']);
  assert.equal(planTags(Array.from({ length: 12 }, (_, i) => `t${i}`).join(',')).length, 8);
});

test('套餐表单与后端规则一致，校准值可选', () => {
  const parsed = parsePlanForm(form);
  assert.deepEqual(parsed.plan, { node: 'LA', name: 'KVM', resources: 'RAM 2 GB, Disk 40 GB', quotaGB: 2000, resetDay: 31, billing: 'both', interfaces: ['eth0', 'ens3'] });
  assert.equal(parsed.usedGB, null);
  assert.equal(parsePlanForm({ ...form, usedGB: '208.6' }).usedGB, 208.6);
  assert.deepEqual(parsePlanForm({ ...form, interfaces: '' }).plan.interfaces, []);
  for (const bad of [
    { quotaGB: '-1' }, { quotaGB: '' }, { quotaGB: '1000001' }, { resetDay: '0' }, { resetDay: '1.5' }, { resetDay: '32' },
    { billing: 'down' }, { interfaces: 'eth0;rm' }, { interfaces: 'eth0 eth0' }, { interfaces: 'a b c d e f g h i' },
    { node: '长'.repeat(121) }, { name: 'a\u0007' }, { usedGB: '-2' }, { usedGB: 'x' },
  ]) assert.throws(() => parsePlanForm({ ...form, ...bad }), Error, JSON.stringify(bad));
});

test('入站名称国家前缀来自公网信息，查询不到不冒充', () => {
  const address = { ip: '192.0.2.10', hostname: '', location: '', country: 'US', org: '' };
  assert.equal(regionLabel({ checkedAt: '', ipv4: address, ipv6: null, issues: [] }), '🇺🇸 美国');
  assert.equal(regionLabel({ checkedAt: '', ipv4: null, ipv6: { ...address, country: 'JP' }, issues: [] }), '🇯🇵 日本');
  assert.equal(regionLabel({ checkedAt: '', ipv4: { ...address, country: 'u1' }, ipv6: null, issues: [] }), '未知地区');
  assert.equal(regionLabel(null), '未知地区');
});
