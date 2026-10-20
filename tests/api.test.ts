import assert from 'node:assert/strict';
import { test } from 'node:test';
import { api, APIError, jsonBody } from '../src/api.ts';

test('管理请求携带同源会话和界面语言，JSON写入格式明确', async () => {
  const previous = globalThis.fetch;
  Object.assign(globalThis, { location: { protocol: 'http:' } });
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/settings');
    assert.equal(options?.credentials, 'same-origin');
    assert.equal(options?.method, 'PUT');
    assert.equal(options?.body, '{"revision":1}');
    const headers = new Headers(options?.headers);
    assert.equal(headers.get('Content-Type'), 'application/json');
    assert.equal(headers.get('X-O-UI-Lang'), 'zh');
    return Response.json({ revision: 2 });
  };
  try { assert.deepEqual(await api('settings', jsonBody('PUT', { revision: 1 })), { revision: 2 }); }
  finally { globalThis.fetch = previous; }
});

test('401管理请求通知重新登录，登录错误与修订冲突不触发重载', async () => {
  const previous = globalThis.fetch;
  let events = 0;
  Object.assign(globalThis, { location: { protocol: 'http:' }, window: { dispatchEvent: () => { events++; } } });
  globalThis.fetch = async () => Response.json({ error: '请先登录' }, { status: 401 });
  try {
    await assert.rejects(api('core/status'), reason => reason instanceof APIError && reason.status === 401);
    assert.equal(events, 1);
    await assert.rejects(api('auth/login'), APIError);
    assert.equal(events, 1);
    globalThis.fetch = async () => Response.json({ error: '修订冲突' }, { status: 409 });
    await assert.rejects(api('settings'), reason => reason instanceof APIError && reason.status === 409);
    assert.equal(events, 1);
  } finally { globalThis.fetch = previous; }
});

test('断线、非JSON服务和离线文件不会误报保存成功', async () => {
  const previous = globalThis.fetch;
  Object.assign(globalThis, { location: { protocol: 'http:' } });
  try {
    globalThis.fetch = async () => { throw new Error('network failed'); };
    await assert.rejects(api('settings'), /未连接或请求超时/);
    globalThis.fetch = async () => new Response('<html>fallback</html>');
    await assert.rejects(api('settings'), /后端未连接/);
    Object.assign(globalThis, { location: { protocol: 'file:' } });
    await assert.rejects(api('settings'), /离线预览不能/);
  } finally { globalThis.fetch = previous; }
});
