import assert from 'node:assert/strict';
import { test } from 'node:test';
import { EventEmitter } from 'node:events';
import { subscriptionURL } from '../src/subscriptions.ts';
import config from '../vite.config.ts';

test('订阅链接使用当前HTTPS或本机HTTP来源且不接收任意跳转地址', () => {
  const path = `/sub/${'a'.repeat(43)}`;
  assert.equal(subscriptionURL(path, 'http://127.0.0.1:5178'), `http://127.0.0.1:5178${path}`);
  assert.equal(subscriptionURL(path, 'https://localhost:5180'), `https://localhost:5180${path}`);
  assert.equal(subscriptionURL(path, 'http://[::1]:5182'), `http://[::1]:5182${path}`);
  assert.equal(subscriptionURL(path, 'https://panel.example.com:5180'), `https://panel.example.com:5180${path}`);
  for (const bad of ['//evil.test/', 'https://evil.test/', '/sub/short', `${path}?token=other`, `${path}/`, '/api/auth/login']) assert.throws(() => subscriptionURL(bad, 'http://127.0.0.1:5178'));
  for (const origin of ['file:///preview.html', 'http://remote.test', 'http://user:secret@localhost', 'http://127.0.0.2:5182', 'https://panel.example.com/path', 'https://panel.example.com/', 'https://panel.example.com?other=1', 'https://panel.example.com#part', 'https://user:secret@panel.example.com']) assert.throws(() => subscriptionURL(path, origin));
});

test('开发订阅代理在默认错误日志读取地址前先移除凭据', () => {
  const options = config.server!.proxy!['/sub'];
  assert.ok(typeof options !== 'string' && options.configure);
  const proxy = new EventEmitter();
  options.configure(proxy as Parameters<NonNullable<typeof options.configure>>[0], options);
  const request = { url: `/sub/${'a'.repeat(43)}?private=fictional` }, response = { req: request };
  proxy.on('error', () => { assert.equal(response.req.url, '/sub/[redacted]'); });
  proxy.emit('error', new Error('fictional backend unavailable'), request, response);
  assert.equal(request.url, '/sub/[redacted]');
});
