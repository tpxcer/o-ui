import test from 'node:test';
import assert from 'node:assert/strict';
import { buildSettings, defaultSettings } from '../src/xray-settings-model.ts';

test('默认设置独立且没有可连接入站或控制接口', () => {
  const draft = defaultSettings(), before = JSON.stringify(draft);
  const config = buildSettings(draft);
  assert.equal(JSON.stringify(draft), before);
  assert.equal('inbounds' in config, false); assert.equal('api' in config, false);
  assert.equal((config.routing as { domainStrategy: string }).domainStrategy, 'AsIs');
});
test('非法JSON/结构、策略、带密码测试URL被拒绝', () => {
  for (const change of [{ rules: '[' }, { rules: '{}' }, { dns: '[]' }, { outbounds: '[]' }, { freedomStrategy: 'unknown' }, { probeURL: 'https://user:pass@example.test/' }]) {
    assert.throws(() => buildSettings({ ...defaultSettings(), ...change }));
  }
});
test('路由出站与均衡引用不能丢失、重复或同时指定', () => {
  assert.throws(() => buildSettings({ ...defaultSettings(), outbounds: '[{"tag":"x","protocol":"freedom"},{"tag":"x","protocol":"freedom"}]' }), /重复/);
  for (const rule of [
    { type: 'field', domain: ['example.test'], outboundTag: 'missing' },
    { type: 'field', domain: ['example.test'], outboundTag: 'direct', balancerTag: 'b' },
    { type: 'field', outboundTag: 'direct' },
  ]) assert.throws(() => buildSettings({ ...defaultSettings(), rules: JSON.stringify([rule]) }));
});
test('均衡前缀匹配与观测要求校验，路由顺序保持', () => {
  const draft = defaultSettings();
  draft.balancers = '[{"tag":"b","selector":["dir"],"strategy":{"type":"roundRobin"}}]';
  draft.rules = '[{"type":"field","domain":["example.test"],"balancerTag":"b"},{"type":"field","ip":["192.0.2.1"],"outboundTag":"block"}]';
  const config = buildSettings(draft);
  assert.deepEqual((config.routing as { rules: unknown }).rules, JSON.parse(draft.rules));
  assert.throws(() => buildSettings({ ...draft, balancers: '[{"tag":"b","selector":["missing"]}]' }), /未匹配/);
  assert.throws(() => buildSettings({ ...draft, balancers: '[{"tag":"b","selector":["dir"],"strategy":{"type":"leastPing"}}]' }), /观测/);
});
test('高级配置不得覆盖其他分类，统计常开', () => {
  for (const advanced of ['{"inbounds":[]}', '{"routing":{}}', '{"api":{}}', '{"log":{}}']) assert.throws(() => buildSettings({ ...defaultSettings(), advanced }), /暂不接受/);
  const config = buildSettings({ ...defaultSettings(), statistics: false });
  assert.equal('stats' in config, true); assert.equal('policy' in config, true);
});
test('Freedom策略统一使用官方targetStrategy，不被旧字段或出站草稿覆盖', () => {
  const draft = defaultSettings();
  draft.freedomStrategy = 'UseIPv4';
  draft.outbounds = '[{"tag":"direct","protocol":"freedom","settings":{"targetStrategy":"ForceIPv6","domainStrategy":"UseIPv6","userLevel":1}}]';
  const config = buildSettings(draft);
  const settings = (config.outbounds as { settings: Record<string, unknown> }[])[0].settings;
  assert.deepEqual(settings, { targetStrategy: 'UseIPv4', userLevel: 1 });
  assert.equal(JSON.parse(draft.outbounds)[0].settings.targetStrategy, 'ForceIPv6');
});
test('路由空条件、错误字段类型与DNS空地址不应被当作结构校验通过', () => {
  for (const condition of [{ domain: [] }, { domain: [''] }, { ip: '192.0.2.1' }, { port: 443 }, { network: '' }, { network: 'not-a-network' }]) {
    assert.throws(() => buildSettings({ ...defaultSettings(), rules: JSON.stringify([{ type: 'field', outboundTag: 'direct', ...condition }]) }));
  }
  assert.throws(() => buildSettings({ ...defaultSettings(), rules: '[{"type":"field","domain":["example.test"],"outboundTag":["direct"]}]' }));
  for (const dns of ['{"servers":[""]}', '{"servers":[{}]}', '{"servers":[{"address":""}]}']) assert.throws(() => buildSettings({ ...defaultSettings(), dns }));
});
