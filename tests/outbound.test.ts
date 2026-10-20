import test from 'node:test';
import assert from 'node:assert/strict';
import { emptyOutbound, moveOutbound, outbound, outboundFields, outboundTexts, putOutbound, removeOutbound } from '../src/outbound-model.ts';
import { defaultSettings } from '../src/xray-settings-model.ts';

const proxy = () => ({ ...emptyOutbound(), tag: 'proxy-a', protocol: 'http', address: '127.0.0.1', port: '8080', user: 'test-user', pass: 'test-only-pass' });
test('四种常用出站/可选认证完整往返，新增不改默认或自动应用', () => {
  for (const protocol of ['freedom', 'blackhole', 'http', 'socks']) {
    for (const auth of [false, true]) {
      const fields = ['http', 'socks'].includes(protocol) ? { ...proxy(), protocol, user: auth ? 'test-user' : '', pass: auth ? 'test-only-pass' : '' } : { ...emptyOutbound(), tag: 'example', protocol };
      assert.deepEqual(outboundFields(JSON.stringify(outbound(fields))), fields);
    }
  }
  const draft = defaultSettings();
  const text = putOutbound(draft, -1, proxy());
  assert.equal(JSON.parse(text)[0].tag, 'direct');
  assert.equal(JSON.parse(text).length, 3);
  assert.equal(JSON.parse(draft.outbounds).length, 2);
  assert.ok(outboundFields('{"tag":"direct","protocol":"freedom"}'));
  assert.ok(outboundFields('{"settings":{"servers":[{"port":8080,"address":"localhost","users":[{"pass":"test-only-pass","user":"test-user"}]}]},"protocol":"http","tag":"proxy-a"}'));
});
test('常用代理严格拒绝非法地址端口与不完整/超长认证，不回退直连', () => {
  for (const address of ['', 'https://example.test', 'a:80', '[::1]', 'x/path', 'x@host', 'a b', '256.2.3.4', '1.2.3', '-host', '::gg']) assert.throws(() => outbound({ ...proxy(), address }));
  for (const address of ['localhost', 'example.test', '127.0.0.1', '::1', '2001:db8::1']) assert.equal((outbound({ ...proxy(), address }).settings as { servers: { address: string }[] }).servers[0].address, address);
  for (const port of ['0', '65536', '-1', '1.5', '1e3', '']) assert.throws(() => outbound({ ...proxy(), port }));
  for (const user of ['', 'x\ny', 'x:y', 'a'.repeat(1025)]) assert.throws(() => outbound({ ...proxy(), user }));
  for (const pass of ['', 'x\ry', 'a'.repeat(1025)]) assert.throws(() => outbound({ ...proxy(), pass }));
  assert.throws(() => outbound({ ...proxy(), protocol: 'socks', user: '汉'.repeat(86) }), /255字节/);
  assert.throws(() => outbound({ ...proxy(), tag: 'two words' }));
  assert.throws(() => outbound({ ...proxy(), protocol: 'vless' }));
  assert.throws(() => putOutbound(defaultSettings(), -1, { ...proxy(), tag: 'direct' }), /重复/);
});
test('路由/均衡前缀/回退/链式/观测引用保护及默认出站不直接删除', () => {
  const draft = { ...defaultSettings(), outbounds: putOutbound(defaultSettings(), -1, proxy()) };
  for (const reference of [
    { rules: '[{"type":"field","network":"tcp","outboundTag":"proxy-a"}]' },
    { balancers: '[{"tag":"pool","selector":["proxy-"]}]' },
    { balancers: '[{"tag":"pool","selector":["direct"],"fallbackTag":"proxy-a"}]' },
    { advanced: '{"observatory":{"subjectSelector":["proxy-"]}}' },
    { outbounds: draft.outbounds.slice(0, -1) + ',{"tag":"chain","protocol":"freedom","proxySettings":{"tag":"proxy-a"}}]' },
    { outbounds: draft.outbounds.slice(0, -1) + ',{"tag":"chain","protocol":"freedom","streamSettings":{"sockopt":{"dialerProxy":"proxy-a"}}}]' },
  ]) {
    assert.throws(() => putOutbound({ ...draft, ...reference }, 2, { ...proxy(), tag: 'renamed' }), /引用/);
    assert.throws(() => removeOutbound({ ...draft, ...reference }, 2), /引用/);
    assert.equal(JSON.parse(putOutbound({ ...draft, ...reference }, 2, { ...proxy(), port: '8081' }))[2].tag, 'proxy-a');
  }
  assert.throws(() => removeOutbound(draft, 0), /默认出站/);
  assert.throws(() => removeOutbound({ ...draft, outbounds: '[{"tag":"last","protocol":"freedom"}]' }, 0), /至少保留/);
  assert.throws(() => removeOutbound({ ...draft, rules: '[' }, 2), /无法确认/);
  assert.deepEqual(JSON.parse(removeOutbound(draft, 2)).map((item: { tag: string }) => item.tag), ['direct', 'block']);
});
test('高级/重复与Unicode键/超大数原文保留，排序仅改变所选顺序', () => {
  const advanced = '{"tag":"advanced","protocol":"vless","settings":{"number":9007199254740993,"x":1,"x":2}}';
  const draft = { ...defaultSettings(), outbounds: `[${outboundTexts(defaultSettings().outbounds).join(',')},${advanced}]` };
  const added = putOutbound(draft, -1, proxy());
  assert.equal(outboundTexts(added)[2], advanced);
  assert.equal(outboundTexts(moveOutbound(added, 2, -1))[1], advanced);
  assert.equal(outboundTexts(removeOutbound({ ...draft, outbounds: added }, 3))[2], advanced);
  for (const text of [advanced, '{"tag":"direct","protocol":"freedom","settings":{"targetStrategy":"AsIs"}}', '{"tag":"x","tag":"y","protocol":"freedom"}', '{"tag":"x","\\u0074ag":"y","protocol":"freedom"}', '{"tag":"x","protocol":"http","settings":{"servers":[{"address":"localhost","port":80,"users":[{"user":"x","user":"y","pass":"p"}]}]}}', '{"tag":"x","protocol":"http","settings":{"servers":[{"address":"localhost","port":80,"users":[]}]}}']) assert.equal(outboundFields(text), null);
  assert.throws(() => putOutbound(draft, 2, proxy()), /高级出站/);
  assert.throws(() => removeOutbound(draft, 2), /高级出站/);
  assert.equal(outboundFields('{"tag":"deep","protocol":"freedom","settings":' + '{"nested":'.repeat(10000) + '0' + '}'.repeat(10000) + '}'), null);
  for (const text of ['[', '{}', '[1]', '[null]']) assert.throws(() => outboundTexts(text));
  for (const index of [-2, 99, 1.5, NaN]) assert.throws(() => putOutbound(draft, index, proxy()));
  for (const index of [-1, 99, 1.5, NaN]) assert.throws(() => removeOutbound(draft, index));
  for (const [index, offset] of [[0, -1], [99, 1], [1.5, 1], [0, 0]]) assert.throws(() => moveOutbound(draft.outbounds, index, offset as -1 | 1));
});
