import test from 'node:test';
import assert from 'node:assert/strict';
import { newRealityConnection, nextRealityTarget, realityTargets, realityServerName, readSniffing, sniffingRules } from '../src/inbound-options.ts';
import { createQuick, defaultSniffing, editInbound } from '../src/workspace.ts';

test('REALITY新建有成对候选，刷新循环目标和SNI，无私钥字段', () => {
  const first = newRealityConnection();
  let target = first.target;
  const names = new Set<string>();
  for (let i = 0; i < realityTargets.length; i++) {
    const next = nextRealityTarget(target);
    assert.equal(next.target, `${next.serverName}:443`);
    names.add(next.serverName); target = next.target;
  }
  assert.equal(names.size, realityTargets.length);
  assert.equal(target, first.target);
  assert.equal(nextRealityTarget('custom.example:443').target, first.target);
  assert.equal('privateKey' in first, false);
  assert.equal(first.mihomoCompatibility ?? false, false);
});
test('REALITY候选包含特斯拉，刷新同步SNI并保持默认目标', () => {
  assert.ok(realityTargets.includes('www.tesla.com'));
  assert.deepEqual(nextRealityTarget('www.amazon.com:443'), { target: 'www.tesla.com:443', serverName: 'www.tesla.com' });
  assert.deepEqual(nextRealityTarget('www.tesla.com:443'), { target: 'www.amd.com:443', serverName: 'www.amd.com' });
  assert.equal(newRealityConnection().target, 'www.amd.com:443');
});
test('SNI从域名目标提取，不从IPv6或不完整目标猜测域名', () => {
  assert.equal(realityServerName(' www.amd.com:443 '), 'www.amd.com');
  for (const value of ['[::1]:443', 'missing-port', 'host:bad']) assert.equal(realityServerName(value), '');
});
test('嗅探表单读取全部开关和排除列表，关闭总开关不丢失子选项', () => {
  const data = new FormData();
  for (const field of ['http', 'tls', 'routeOnly']) data.set(`sniff-${field}`, 'on');
  data.set('sniff-ips', '192.0.2.1\n2001:db8::/32'); data.set('sniff-domains', 'domain:example.com,full:example.org');
  const value = readSniffing(data);
  assert.equal(value.enabled, false); assert.equal(value.http, true); assert.equal(value.tls, true); assert.equal(value.metadataOnly, false);
  assert.deepEqual(value.ipsExcluded, ['192.0.2.1', '2001:db8::/32']);
  assert.deepEqual(value.domainsExcluded, ['domain:example.com', 'full:example.org']);
});
test('嗅探排除列表有界，去重，不默默截断', () => {
  assert.deepEqual(sniffingRules(' x\n x,， y \n'), ['x', 'y']);
  assert.throws(() => sniffingRules('a'.repeat(257)), /256/);
  assert.throws(() => sniffingRules(Array.from({ length: 65 }, (_, i) => String(i)).join('\n')), /64/);
  assert.throws(() => sniffingRules('a\x00b'));
});
test('创建可保存自选嗅探，编辑保留排除列表且不共享数组', () => {
  const sniffing = { ...defaultSniffing(), routeOnly: false, ipsExcluded: ['192.0.2.1'], domainsExcluded: ['domain:example.com'] };
  const state = createQuick({ inbounds: [], clients: [] }, { preset: 'reality', port: 8443, name: 'one', quotaGB: 0, ipLimit: 0, subscription: false, expiresAt: null, sniffing }, 'one');
  sniffing.ipsExcluded.push('192.0.2.2');
  assert.deepEqual(state.inbounds[0].sniffing.ipsExcluded, ['192.0.2.1']);
  const result = editInbound(state, 'one', { ...state.inbounds[0], name: 'renamed' });
  result.inbounds[0].sniffing.domainsExcluded!.push('full:example.org');
  assert.equal(state.inbounds[0].sniffing.domainsExcluded!.length, 1);
});
