import assert from 'node:assert/strict';
import { test } from 'node:test';
import { api, apiURL, APIError, confirmSaved, jsonBody, uncertainSave } from '../src/api.ts';

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

test('面板部署在访问路径下时，接口地址随页面目录变化', () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'location');
  try {
    Object.defineProperty(globalThis, 'location', { configurable: true, value: { protocol: 'https:', pathname: '/secret-9/' } });
    assert.equal(apiURL('auth/status'), '/secret-9/api/auth/status');
    Object.defineProperty(globalThis, 'location', { configurable: true, value: { protocol: 'https:', pathname: '/' } });
    assert.equal(apiURL('auth/status'), '/api/auth/status');
  } finally { if (previous) Object.defineProperty(globalThis, 'location', previous); else Reflect.deleteProperty(globalThis, 'location'); }
});

test('保存结果不确定时：连接恢复后读回内容与修订都等于本次提交才算已保存', async () => {
  const previous = globalThis.fetch;
  Object.assign(globalThis, { location: { protocol: 'http:' } });
  const sent = { inbounds: [{ id: 'a', port: 57836 }], clients: [] };
  let calls = 0, reply: () => Response = () => Response.json({ revision: 12, data: sent });
  globalThis.fetch = async () => { calls++; if (calls <= 2) throw new TypeError('Failed to fetch'); return reply(); };
  try {
    assert.equal(uncertainSave(new Error('lost')), true);
    assert.equal(uncertainSave(new APIError('冲突', 409)), true);
    assert.equal(uncertainSave(new APIError('无效', 400)), false);
    assert.deepEqual(await confirmSaved('workspace', sent, 12, { interval: 1 }), { revision: 12, data: sent });
    assert.equal(calls, 3, '连接未恢复时重试');
    calls = 2; reply = () => Response.json({ revision: 13, data: sent });
    assert.equal(await confirmSaved('workspace', sent, 12, { interval: 1 }), null, '修订号不同：期间还有别的保存');
    calls = 2; reply = () => Response.json({ revision: 12, data: { ...sent, clients: [{ id: 'x' }] } });
    assert.equal(await confirmSaved('workspace', sent, 12, { interval: 1 }), null, '内容不同');
    calls = 2; reply = () => Response.json({ error: '服务异常' }, { status: 500 });
    assert.equal(await confirmSaved('workspace', sent, 12, { interval: 1 }), null);
    calls = -1000;
    assert.equal(await confirmSaved('workspace', sent, 12, { wait: 20, interval: 5 }), null, '一直连不上时按时放弃');
  } finally { globalThis.fetch = previous; }
});
