import test from 'node:test';
import assert from 'node:assert/strict';
import { defaultSettings } from '../src/xray-settings-model.ts';
import { emptyRoute, moveRoute, putRoute, removeRoute, routeFields, routeRule, routeSummary, routingRules, routingTargets } from '../src/routing-model.ts';

test('默认路由为空，目标显式选择，空表单不创建全流量规则', () => {
  const draft = defaultSettings();
  assert.deepEqual(routingRules(draft.rules), []);
  assert.deepEqual(routingTargets(draft).map(item => item.value), ['outboundTag:direct', 'outboundTag:block']);
  assert.throws(() => putRoute(draft, -1, { ...emptyRoute(), target: 'outboundTag:direct' }), /至少/);
  assert.throws(() => routeRule({ ...emptyRoute(), domain: 'example.test' }), /请选择/);
});
test('常用条件可完整往返并保持条目/数组顺序，互为AND不拆成多条', () => {
  const fields = { ...emptyRoute(), domain: 'domain:example.test\nfull:other.test', ip: '192.0.2.1\n2001:db8::/32', port: '443,1000-2000', network: 'tcp,udp', protocol: 'http\ntls', target: 'outboundTag:block' };
  const rule = routeRule(fields);
  assert.deepEqual(routeFields(rule), fields);
  assert.deepEqual(rule, { type: 'field', domain: ['domain:example.test', 'full:other.test'], ip: ['192.0.2.1', '2001:db8::/32'], protocol: ['http', 'tls'], port: '443,1000-2000', network: 'tcp,udp', outboundTag: 'block' });
  assert.match(routeSummary(rule), /example.test/);
});
test('添加/编辑只改变指定规则，高级字段和未编辑对象保持', () => {
  const draft = defaultSettings();
  const advanced = { type: 'field', domain: ['regexp:^a,b$'], attrs: "attrs[':method'] == 'GET'", outboundTag: 'direct', custom: { flag: true } };
  draft.rules = JSON.stringify([advanced]);
  const original = draft.rules;
  draft.rules = putRoute(draft, -1, { ...emptyRoute(), domain: 'example.test', target: 'outboundTag:block' });
  assert.deepEqual(routingRules(draft.rules)[0], advanced);
  draft.rules = putRoute(draft, 1, { ...emptyRoute(), ip: '192.0.2.0/24', target: 'outboundTag:direct' });
  assert.deepEqual(routingRules(draft.rules), [advanced, { type: 'field', ip: ['192.0.2.0/24'], outboundTag: 'direct' }]);
  assert.equal(original, JSON.stringify([advanced]));
  assert.throws(() => putRoute(draft, 0, { ...emptyRoute(), ip: '192.0.2.1', target: 'outboundTag:direct' }), /高级/);
});
test('高级/不可无损表达规则不降级；空数组、空字段和异常格式不能静默删去', () => {
  for (const extra of [{ domain: ['regexp:^a,b$'] }, { ip: ['geoip:private'] }, { source: ['192.0.2.1'] }, { domain: [] }, { domain: [' a.test'] }, { domain: ['a\nb.test'] }, { port: 'env:PORT' }, { network: 'tcp, udp' }, { protocol: ['custom'] }, { ruleTag: 'keep' }, { outboundTag: 1 }]) {
    assert.equal(routeFields({ type: 'field', domain: ['example.test'], outboundTag: 'direct', ...extra }), null);
  }
});
test('顺序移动/单项删除保留其它高级字段，不修改传入文本；越界拒绝', () => {
  const rules = [{ type: 'field', attrs: 'anything', outboundTag: 'direct' }, { type: 'field', protocol: ['bittorrent'], outboundTag: 'block' }, { type: 'field', ip: ['geoip:private'], outboundTag: 'block' }];
  const text = JSON.stringify(rules);
  assert.deepEqual(routingRules(moveRoute(text, 1, -1)), [rules[1], rules[0], rules[2]]);
  assert.deepEqual(routingRules(moveRoute(text, 1, 1)), [rules[0], rules[2], rules[1]]);
  assert.deepEqual(routingRules(removeRoute(text, 1)), [rules[0], rules[2]]);
  assert.equal(text, JSON.stringify(rules));
  for (const index of [-1, 3, 0.5, NaN]) assert.throws(() => removeRoute(text, index));
  assert.throws(() => moveRoute(text, 0, -1)); assert.throws(() => moveRoute(text, 2, 1));
});
test('出站/均衡引用区分，同名可存在，缺失重复/非法JSON拒绝不覆盖', () => {
  const draft = defaultSettings();
  draft.balancers = '[{"tag":"direct","selector":["direct"]}]';
  const text = putRoute(draft, -1, { ...emptyRoute(), domain: 'example.test', target: 'balancerTag:direct' });
  assert.equal(routingRules(text)[0].balancerTag, 'direct');
  assert.throws(() => putRoute(draft, -1, { ...emptyRoute(), domain: 'example.test', target: 'outboundTag:missing' }), /不存在/);
  assert.throws(() => routingTargets({ ...draft, outbounds: '[{"tag":"x"},{"tag":"x"}]' }), /重复/);
  for (const value of ['[', '{}', '[null]', '[1]']) assert.throws(() => routingRules(value));
  assert.throws(() => putRoute({ ...draft, rules: '[' }, -1, { ...emptyRoute(), domain: 'example.test', target: 'outboundTag:direct' }));
});
test('域名/IP/端口/协议合法边界与非法输入，不发起网络请求', () => {
  for (const value of ['0.0.0.0/0', '255.255.255.255/32', '::/0', '::1/128', '::ffff:192.0.2.1']) assert.ok(routeRule({ ...emptyRoute(), ip: value, target: 'outboundTag:block' }));
  for (const value of ['999.1.1.1', '01.2.3.4', '::/129', '192.0.2.1/33', '1.2.3.4/-1', 'fe80::1%en0', '::1/path', '1.2.3.4/32/1']) assert.throws(() => routeRule({ ...emptyRoute(), ip: value, target: 'outboundTag:block' }));
  for (const value of ['https://example.test', '-a.test', 'a..test', 'a.test/path', 'regexp:a', 'a.test,b.test']) assert.throws(() => routeRule({ ...emptyRoute(), domain: value, target: 'outboundTag:block' }));
  for (const value of ['0', '65536', '443-80', '1,,2', '3.5', '80x', '1-65536']) assert.throws(() => routeRule({ ...emptyRoute(), port: value, target: 'outboundTag:block' }));
  assert.equal(routeRule({ ...emptyRoute(), port: ' 1 , 100 - 65535 ', target: 'outboundTag:block' }).port, '1,100-65535');
  assert.throws(() => routeRule({ ...emptyRoute(), protocol: 'https', target: 'outboundTag:block' }));
  assert.throws(() => routeRule({ ...emptyRoute(), domain: Array(65).fill('example.test').join('\n'), target: 'outboundTag:block' }), /64项/);
});
test('排序/删除/追加不重新序列化未编辑高级条目，大整数/转义/重复字段原文保持', () => {
  const raw = '{"type":"field","domain":["regexp:^a,\\\"[{}]\\\\b$"],"outboundTag":"direct","extra":{"large":9007199254740993,"huge":1e400,"keep":1,"keep":2}}';
  const second = '{"type":"field","port":"443","outboundTag":"block"}';
  const text = `[${raw},${second}]`;
  assert.ok(moveRoute(text, 0, 1).includes(raw));
  assert.ok(removeRoute(text, 1).includes(raw));
  const draft = { ...defaultSettings(), rules: text };
  assert.ok(putRoute(draft, -1, { ...emptyRoute(), domain: 'example.test', target: 'outboundTag:block' }).includes(raw));
  assert.ok(putRoute(draft, 1, { ...emptyRoute(), port: '80', target: 'outboundTag:direct' }).includes(raw));
});
