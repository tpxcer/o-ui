import test from 'node:test';
import assert from 'node:assert/strict';
import { isLoopbackListen, readProxyEndpoint, validateProxyEndpoint } from '../src/proxy-endpoint.ts';

test('旧草稿默认回环；IPv4、IPv6及映射回环识别一致', () => {
  for (const listen of ['', '127.0.0.1', '127.4.3.2', '::1', '0:0:0:0:0:0:0:1', '::ffff:127.0.0.1']) {
    assert.equal(isLoopbackListen(listen), true, listen);
    assert.doesNotThrow(() => validateProxyEndpoint({ listen }));
  }
  for (const listen of ['0.0.0.0', '::', '192.0.2.1', '::ffff:192.0.2.1', 'localhost', '[::1]', 'bad']) assert.equal(isLoopbackListen(listen), false, listen);
  assert.deepEqual(readProxyEndpoint(new FormData()), { listen: '127.0.0.1', shareHost: '' });
});
test('非回环必须填分享主机；分享地址与SNI、监听地址独立', () => {
  for (const listen of ['0.0.0.0', '::', '192.0.2.1']) {
    assert.throws(() => validateProxyEndpoint({ listen }), /必须明确填写分享主机/);
    for (const shareHost of ['proxy.example.test', '192.0.2.10', '2001:db8::10']) assert.doesNotThrow(() => validateProxyEndpoint({ listen, shareHost }));
  }
  const form = new FormData(); form.set('listen', ' :: '); form.set('shareHost', ' 2001:db8::10 '); form.set('serverName', 'sni.test');
  assert.deepEqual(readProxyEndpoint(form), { listen: '::', shareHost: '2001:db8::10' });
});
test('监听拒绝非IP；分享主机拒绝URL、端口、凭据、通配地址及控制符', () => {
  assert.throws(() => validateProxyEndpoint({ shareHost: 'proxy.test\n' }), /分享主机/);
  for (const listen of ['localhost', '[::1]', '127.0.0.1:8000', 'fe80::1%en0', '127.00.0.1', '224.0.0.1', 'ff02::1', '::ffff:224.0.0.1', '255.255.255.255', 'bad\n']) assert.throws(() => validateProxyEndpoint({ listen, shareHost: 'proxy.test' }), /监听/);
  for (const shareHost of ['https://proxy.test', 'user@proxy.test', 'proxy.test:443', 'proxy.test/', 'proxy.test?x', 'proxy.test#x', '[2001:db8::1]', 'fe80::1%en0', '0.0.0.0', '::', '::ffff:0.0.0.0', '224.0.0.1', 'ff02::1', '::ffff:224.0.0.1', '255.255.255.255', 'bad name', 'bad\r\n', 'proxy..test', '-proxy.test', 'proxy-.test', 'x'.repeat(64) + '.test', '1.2.3.999', '2130706433']) assert.throws(() => validateProxyEndpoint({ shareHost }), /分享主机/, shareHost);
});
