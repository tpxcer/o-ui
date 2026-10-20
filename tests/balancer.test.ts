import test from 'node:test';
import assert from 'node:assert/strict';
import { balancerFields, balancerTexts, emptyBalancer, matchedOutbounds, putBalancer, removeBalancer } from '../src/balancer-model.ts';
import { defaultSettings } from '../src/xray-settings-model.ts';

const fields = () => ({ ...emptyBalancer(), tag: 'pool', selector: 'direct' });
test('均衡默认不添加，默认/随机/轮询完整往返', () => {
  const draft = defaultSettings();
  assert.deepEqual(balancerTexts(draft.balancers), []);
  for (const strategy of ['', 'random', 'roundRobin']) {
    const value = { ...fields(), strategy };
    const text = putBalancer(draft, -1, value);
    assert.deepEqual(balancerFields(balancerTexts(text)[0]), value);
    if (!strategy) assert.equal('strategy' in JSON.parse(text)[0], false);
    assert.equal('fallbackTag' in JSON.parse(text)[0], false);
  }
  assert.equal(draft.balancers, '[]');
});
test('selector按前缀展示实际匹配，失效前缀拒绝，不改成直连', () => {
  const draft = { ...defaultSettings(), outbounds: '[{"tag":"proxy-a","protocol":"blackhole"},{"tag":"proxy-ab","protocol":"blackhole"},{"tag":"block","protocol":"blackhole"}]' };
  assert.deepEqual(matchedOutbounds(draft, 'proxy-a'), ['proxy-a', 'proxy-ab']);
  assert.deepEqual(matchedOutbounds({ ...draft, outbounds: '[{"tag":"proxy-z"},{"tag":"proxy-a"}]' }, 'proxy-'), ['proxy-a', 'proxy-z']);
  assert.deepEqual(matchedOutbounds(draft, ''), []);
  assert.throws(() => putBalancer(draft, -1, { ...fields(), selector: 'missing' }), /未匹配/);
  assert.throws(() => putBalancer(draft, -1, { ...fields(), selector: 'proxy-\nmissing' }), /未匹配/);
  assert.equal(balancerFields('{"tag":"x","selector":["proxy-"],"fallbackTag":"block"}'), null);
  assert.throws(() => matchedOutbounds({ ...draft, outbounds: '[{"tag":"x"},{"tag":"x"}]' }, 'x'), /重复/);
});
test('均衡引用保护：已用不可删除或改名，可改策略，未用只删一项', () => {
  let draft = defaultSettings(); draft.balancers = putBalancer(draft, -1, fields());
  draft.rules = '[{"type":"field","network":"tcp","balancerTag":"pool"}]';
  assert.throws(() => removeBalancer(draft, 0), /引用/);
  assert.throws(() => putBalancer(draft, 0, { ...fields(), tag: 'renamed' }), /引用/);
  assert.equal(JSON.parse(putBalancer(draft, 0, { ...fields(), strategy: 'roundRobin' }))[0].strategy.type, 'roundRobin');
  draft.rules = '[]'; draft.balancers = putBalancer(draft, -1, { ...fields(), tag: 'second' });
  assert.deepEqual(JSON.parse(removeBalancer(draft, 0)).map((x: { tag: string }) => x.tag), ['second']);
  assert.throws(() => putBalancer(draft, -1, fields()), /重复/);
  assert.throws(() => removeBalancer({ ...draft, rules: '[' }, 0), /路由JSON/);
  for (const index of [-1, 2, 0.5, NaN]) assert.throws(() => removeBalancer(draft, index));
});
test('高级/未知/重复键均不可表单改写，混合列表保留原文字节', () => {
  const advanced = '{"tag":"advanced","selector":["direct"],"strategy":{"type":"leastLoad","settings":{"large":9007199254740993,"x":1,"x":2}}}';
  const draft = { ...defaultSettings(), balancers: `[${advanced}]`, advanced: '{"observatory":{}}' };
  for (const raw of [advanced, '{"tag":"x","tag":"y","selector":["direct"]}', '{"t\\u0061g":"x","tag":"y","selector":["direct"]}', '{"tag":"x","selector":["direct"],"strategy":{"type":"random","type":"roundRobin"}}', '{"tag":"x","selector":["direct"],"extra":1}', '{"tag":"x","selector":["direct"],"fallbackTag":""}', '{"tag":" x","selector":["direct"]}', '{"tag":"x","selector":["direct\nblock"]}']) assert.equal(balancerFields(raw), null);
  const text = putBalancer(draft, -1, fields());
  assert.ok(text.includes(advanced));
  assert.ok(removeBalancer({ ...draft, balancers: text }, 1).includes(advanced));
  assert.throws(() => putBalancer(draft, 0, fields()), /高级/);
  assert.throws(() => removeBalancer(draft, 0), /高级/);
});
test('均衡非法输入/上限/索引拒绝，原草稿保持', () => {
  const draft = defaultSettings();
  for (const change of [{ tag: '' }, { tag: 'a b' }, { tag: 'a'.repeat(129) }, { selector: '' }, { selector: 'a b' }, { selector: Array(65).fill('direct').join('\n') }, { strategy: 'leastPing' }]) assert.throws(() => putBalancer(draft, -1, { ...fields(), ...change }));
  for (const index of [-2, 0, 0.5, NaN]) assert.throws(() => putBalancer(draft, index, fields()));
  for (const raw of ['[', '{}', '[null]', '[1]']) assert.throws(() => balancerTexts(raw));
  assert.equal(draft.balancers, '[]');
});
