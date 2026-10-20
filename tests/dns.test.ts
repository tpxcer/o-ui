import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildDNS, dnsFields } from '../src/dns-model.ts';
import { buildSettings, defaultSettings } from '../src/xray-settings-model.ts';

test('默认DNS及全部常用字段完整往返，区分省略与显式false', () => {
  const fields = dnsFields(defaultSettings().dns)!;
  assert.equal(fields.servers, 'localhost');
  assert.equal(fields.queryStrategy, 'UseIP');
  for (const queryStrategy of ['', 'UseIP', 'UseIPv4', 'UseIPv6']) {
    for (const disableCache of ['', 'true', 'false']) for (const disableFallback of ['', 'true', 'false']) {
      const input = { servers: 'localhost\n1.1.1.1\n2001:db8::1\ntcp://127.0.0.1:5353\ntcp+local://[::1]:5353\nhttps://dns.example.test/dns-query\nhttps+local://dns.example.test:8443/dns-query', queryStrategy, disableCache, disableFallback };
      const text = buildDNS(input);
      assert.deepEqual(dnsFields(text), input);
      assert.deepEqual(buildSettings({ ...defaultSettings(), dns: text }).dns, JSON.parse(text));
      assert.equal(Object.hasOwn(JSON.parse(text), 'disableCache'), disableCache !== '');
    }
  }
});

test('DNS表单拒绝空列表、错误地址/端口、凭据及参数，不隐式选公共服务商', () => {
  const fields = dnsFields(defaultSettings().dns)!;
  for (const servers of ['', '\n ', '999.1.1.1', '127.1', '01.2.3.4', '[::1]', '::gg', 'https://user:secret@dns.example/dns-query', 'https://dns.example/dns-query?key=secret', 'https://dns.example/dns-query#secret', 'tcp://127.0.0.1:0', 'tcp://127.0.0.1:65536', 'tcp://127.0.0.1/path', 'tcp://resolver.example:53', 'file:///etc/hosts', 'https://dns.exa mple/query', 'udp://127.0.0.1:5353', 'fakedns', Array(65).fill('localhost').join('\n'), 'x'.repeat(2049)]) assert.throws(() => buildDNS({ ...fields, servers }));
  assert.throws(() => buildDNS({ ...fields, queryStrategy: 'UseSystem' }));
  assert.throws(() => buildDNS({ ...fields, disableCache: 'yes' }));
  assert.deepEqual(JSON.parse(buildDNS(fields)), { servers: ['localhost'], queryStrategy: 'UseIP' });
  assert.deepEqual(JSON.parse(buildDNS({ ...fields, servers: '\r\n 1.1.1.1\r\n \r\n ::1 \n' })).servers, ['1.1.1.1', '::1']);
});

test('高级DNS/非法类型/重复键不开放表单，原文不被替代', () => {
  for (const text of ['{', '[]', 'null', '{"servers":[]}', '{"servers":[null]}', '{"servers":[" localhost"]}', '{"servers":["tcp://dns.example:53"]}', '{"servers":["localhost"],"queryStrategy":""}', '{"servers":["localhost"],"disableCache":"false"}', '{"servers":[{"address":"1.1.1.1","domains":["domain:example.test"]}]}', '{"servers":["localhost"],"hosts":{"example.test":"127.0.0.1"}}', '{"servers":["localhost"],"extra":{"large":9007199254740993}}', '{"servers":["localhost"],"servers":["1.1.1.1"]}', '{"servers":["localhost"],"disableCache":false,"disableCach\\u0065":true}']) assert.equal(dnsFields(text), null, text);
  assert.equal(dnsFields('{"servers":["https://dns.example/dns%22a%3Ab"]}')?.servers, 'https://dns.example/dns%22a%3Ab');
});
