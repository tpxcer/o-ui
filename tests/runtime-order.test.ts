import test from 'node:test';
import assert from 'node:assert/strict';
import { initRuntime } from '../src/runtime.ts';
import type { RuntimeStatus } from '../src/runtime.ts';

const running = { running: true, version: '26.7.28', workspaceRevision: 1, settingsRevision: 1, inbounds: 1 };
const stopped = { ...running, running: false };
const response = (data: unknown) => new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } });
const settle = () => new Promise(resolve => setImmediate(resolve));
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}
class Control {
  checked = false;
  disabled = false;
  handlers = new Map<string, (event?: { currentTarget: Control }) => void>();
  addEventListener(name: string, handler: (event?: { currentTarget: Control }) => void) { this.handlers.set(name, handler); }
  focus() {}
}
class Dialog extends Control {
  open = false;
  innerHTML = '';
  controls = new Map<string, Control>();
  setAttribute() {}
  showModal() { this.open = true; }
  querySelector(selector: string) {
    if (!this.controls.has(selector)) this.controls.set(selector, new Control());
    return this.controls.get(selector)!;
  }
}
async function withPage(fetcher: typeof fetch, run: (dialog: Dialog) => Promise<void>) {
  const dialog = new Dialog();
  const values = { document: { createElement: () => dialog, body: { append() {} }, activeElement: null }, location: { protocol: 'http:' }, fetch: fetcher };
  const before = Object.fromEntries(Object.keys(values).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(values)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  try { await run(dialog); }
  finally {
    for (const [key, descriptor] of Object.entries(before)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else Reflect.deleteProperty(globalThis, key);
    }
  }
}
test('晚到的旧状态查询不覆盖较新查询', async () => {
  const requests = [deferred<Response>(), deferred<Response>()];
  let index = 0;
  await withPage(() => requests[index++].promise, async () => {
    const seen: (RuntimeStatus | null)[] = [];
    const runtime = initRuntime(status => seen.push(status));
    const refresh = runtime.refresh();
    requests[1].resolve(response(stopped)); await refresh;
    requests[0].resolve(response(running)); await settle();
    assert.deepEqual(seen, [stopped]);
  });
});
test('状态新鲜度使用发起时刻，慢响应不会重新变新', async t => {
  let clock = 1000;
  t.mock.method(Date, 'now', () => clock);
  const pending = deferred<Response>();
  await withPage(() => pending.promise, async () => {
    const seen: number[] = [];
    initRuntime((_, at) => seen.push(at));
    clock = 21000;
    pending.resolve(response(running)); await settle();
    assert.deepEqual(seen, [1000]);
  });
});
test('查询耗时大于轮询间隔时仍可接收最新已完成结果，不永久饿死', async () => {
  const requests = [deferred<Response>(), deferred<Response>()];
  let index = 0;
  await withPage(() => requests[index++].promise, async () => {
    const seen: (RuntimeStatus | null)[] = [];
    const runtime = initRuntime(status => seen.push(status));
    const refresh = runtime.refresh();
    requests[0].resolve(response(running)); await settle();
    assert.deepEqual(seen, [running]);
    requests[1].resolve(response(stopped)); await refresh;
    assert.deepEqual(seen, [running, stopped]);
  });
});
test('应用或停止期间不轮询，旧查询的成功或失败不覆盖操作结果', async t => {
  for (const failure of [false, true]) await t.test(failure ? '旧查询失败' : '旧查询成功', async () => {
    const first = deferred<Response>(), mutation = deferred<Response>();
    let reads = 0;
    await withPage(async path => {
      if (path === '/api/runtime/status') return ++reads === 1 ? first.promise : response(running);
      if (path === '/api/runtime/stop') return mutation.promise;
      if (path === '/api/core/status') return response({ selected: '26.7.28' });
      return response({ revision: 1, available: false });
    }, async dialog => {
      const seen: (RuntimeStatus | null)[] = [];
      const runtime = initRuntime(status => seen.push(status));
      runtime.open('stop'); await settle();
      dialog.querySelector('#runtime-execute').handlers.get('click')!();
      await runtime.refresh();
      assert.equal(reads, 2, '操作进行中不能用轮询干扰新状态');
      mutation.resolve(response(stopped)); await settle();
      first.resolve(failure ? new Response('{}', { status: 503, headers: { 'Content-Type': 'application/json' } }) : response(running));
      await settle();
      assert.deepEqual(seen, [running, stopped]);
    });
  });
});
test('操作失败后仍重新读取真实运行状态并保留重试入口', async () => {
  let reads = 0;
  await withPage(async path => {
    if (path === '/api/runtime/status') { reads++; return response(running); }
    if (path === '/api/runtime/stop') return new Response('{"error":"停止失败"}', { status: 503, headers: { 'Content-Type': 'application/json' } });
    if (path === '/api/core/status') return response({ selected: '26.7.28' });
    return response({ revision: 1, available: false });
  }, async dialog => {
    const seen: (RuntimeStatus | null)[] = [];
    const runtime = initRuntime(status => seen.push(status));
    await settle(); runtime.open('stop'); await settle();
    dialog.querySelector('#runtime-execute').handlers.get('click')!(); await settle();
    assert.equal(reads, 3);
    assert.deepEqual(seen.at(-1), running);
    assert.match(dialog.innerHTML, /停止失败/);
    assert.match(dialog.innerHTML, /id="runtime-execute"\s*>确认停止/);
  });
});

test('重启确认只发送运行历史与当前实例，不应用较新草稿或另选核心', async () => {
  const current = { ...running, startedAt: '2026-10-19T00:00:00Z' };
  const mutations: { path: unknown; data: unknown }[] = [];
  await withPage(async (path, init) => {
    if (init?.method === 'POST') { mutations.push({ path, data: JSON.parse(String(init.body)) }); return response(current); }
    if (path === '/api/runtime/status') return response(current);
    if (path === '/api/core/status') return response({ selected: '26.3.27' });
    if (path === '/api/workspace' || path === '/api/settings') return response({ revision: 9 });
    return response({ revision: 4, available: true, version: current.version, workspaceRevision: 1, settingsRevision: 1, inbounds: 1 });
  }, async dialog => {
    const runtime = initRuntime(() => {});
    await settle(); runtime.open('restart'); await settle();
    assert.match(dialog.innerHTML, /重启当前已应用配置/);
    assert.doesNotMatch(dialog.innerHTML, /本次使用：v26\.3\.27/);
    dialog.querySelector('#runtime-execute').handlers.get('click')!(); await settle();
    assert.deepEqual(mutations, [], '未确认不能重启');
    const confirmation = dialog.querySelector('#runtime-confirm'); confirmation.checked = true;
    confirmation.handlers.get('change')!({ currentTarget: confirmation });
    dialog.querySelector('#runtime-execute').handlers.get('click')!(); await settle();
    assert.deepEqual(mutations, [{ path: '/api/runtime/restart', data: { revision: 4, startedAt: current.startedAt, confirmation: '确认重启当前配置' } }]);
    assert.match(dialog.innerHTML, /已重启当前已应用配置/);
  });
});

test('停止和重启不依赖草稿或当前选择可读取', async () => {
  for (const action of ['stop', 'restart'] as const) {
    const reads: unknown[] = [];
    await withPage(async path => {
      reads.push(path);
      if (path === '/api/runtime/status') return response({ ...running, startedAt: '2026-10-19T00:00:00Z' });
      if (path === '/api/runtime/history') return response({ revision: 4, available: true });
      return new Response('{"error":"草稿损坏"}', { status: 500, headers: { 'Content-Type': 'application/json' } });
    }, async dialog => {
      const runtime = initRuntime(() => {});
      await settle(); runtime.open(action); await settle();
      assert.equal(reads.some(path => ['/api/core/status', '/api/workspace', '/api/settings'].includes(String(path))), false);
      assert.doesNotMatch(dialog.innerHTML, /草稿损坏/);
    });
  }
});

test('重启未运行或确认后实例变化时不发请求，预检不需执行确认', async () => {
  for (const changed of [false, true]) {
    let current = { ...(changed ? running : stopped), startedAt: '2026-10-19T00:00:00Z' };
    const mutations: unknown[] = [];
    await withPage(async (path, init) => {
      if (init?.method === 'POST') { mutations.push(path); return response(current); }
      if (path === '/api/runtime/status') return response(current);
      return response({ revision: 4, available: true });
    }, async dialog => {
      const runtime = initRuntime(() => {});
      await settle(); runtime.open('restart'); await settle();
      if (changed) {
        dialog.querySelector('#runtime-preflight').handlers.get('click')!(); await settle();
        assert.deepEqual(mutations, ['/api/runtime/restart-preflight']);
        const confirmation = dialog.querySelector('#runtime-confirm'); confirmation.checked = true;
        confirmation.handlers.get('change')!({ currentTarget: confirmation });
        current = { ...current, startedAt: '2026-10-19T01:00:00Z' };
        await runtime.refresh();
      }
      dialog.querySelector('#runtime-execute').handlers.get('click')!(); await settle();
      assert.deepEqual(mutations, changed ? ['/api/runtime/restart-preflight'] : []);
      assert.match(dialog.innerHTML, /实例已改变时请关闭后重新读取/);
    });
  }
});

test('非回环应用、重启与恢复额外确认，预检不带授权且切换后不复用确认', async t => {
  for (const action of ['apply', 'restart', 'resume'] as const) await t.test(action, async () => {
    const current = { ...(action === 'restart' ? running : stopped), startedAt: '2026-10-19T00:00:00Z' };
    const writes: { path: unknown; data: Record<string, unknown> }[] = [];
    await withPage(async (path, init) => {
      if (init?.method === 'POST') { writes.push({ path, data: JSON.parse(String(init.body)) }); return response(current); }
      if (path === '/api/runtime/status') return response(current);
      if (path === '/api/core/status') return response({ selected: '26.7.28' });
      if (path === '/api/workspace') return response({ revision: 9, data: { inbounds: [{ enabled: true, listen: '0.0.0.0', shareHost: 'proxy.test' }], clients: [] } });
      if (path === '/api/settings') return response({ revision: 3 });
      return response({ revision: 4, available: true, nonLoopbackProxy: true });
    }, async dialog => {
      const runtime = initRuntime(() => {});
      await settle(); runtime.open(action === 'resume' ? 'apply' : action); await settle();
      if (action === 'resume') dialog.querySelector('#runtime-resume').handlers.get('click')!();
      assert.match(dialog.innerHTML, /id="runtime-proxy-confirm"/);
      dialog.querySelector('#runtime-preflight').handlers.get('click')!(); await settle();
      assert.equal(writes.length, 1);
      assert.equal(writes[0].data.proxyConfirmation, undefined, '预检不携带开放授权');
      if (action !== 'apply') {
        const regular = dialog.querySelector('#runtime-confirm'); regular.checked = true;
        regular.handlers.get('change')!({ currentTarget: regular });
      }
      dialog.querySelector('#runtime-execute').handlers.get('click')!(); await settle();
      assert.equal(writes.length, 1, '普通执行确认不能替代代理开放确认');
      assert.match(dialog.innerHTML, /请先明确确认开放非回环/);
      const proxy = dialog.querySelector('#runtime-proxy-confirm'); proxy.checked = true;
      proxy.handlers.get('change')!({ currentTarget: proxy });
      dialog.querySelector('#runtime-execute').handlers.get('click')!(); await settle();
      assert.equal(writes.length, 2);
      assert.equal(writes[1].path, `/api/runtime/${action}`);
      assert.equal(writes[1].data.proxyConfirmation, '确认开放代理监听');
      if (action === 'apply') assert.deepEqual(writes[1].data, { version: '26.7.28', workspaceRevision: 9, settingsRevision: 3, proxyConfirmation: '确认开放代理监听' });
      assert.doesNotMatch(dialog.innerHTML, /id="runtime-proxy-confirm" type="checkbox" checked/);
      dialog.querySelector('#runtime-execute').handlers.get('click')!(); await settle();
      assert.equal(writes.length, 2, '执行后不能复用旧确认');
    });
  });
});

test('初始空草稿可读取；回环应用不发送开放代理授权', async () => {
  for (const data of [null, { inbounds: [{ enabled: true, listen: '::1' }], clients: [] }]) {
    let write: unknown;
    await withPage(async (path, init) => {
      if (init?.method === 'POST') { write = JSON.parse(String(init.body)); return response(stopped); }
      if (path === '/api/runtime/status') return response(stopped);
      if (path === '/api/core/status') return response({ selected: '26.7.28' });
      if (path === '/api/workspace') return response({ revision: 1, data });
      return response({ revision: 1, available: false });
    }, async dialog => {
      const runtime = initRuntime(() => {}); await settle(); runtime.open(); await settle();
      assert.doesNotMatch(dialog.innerHTML, /id="runtime-proxy-confirm"/);
      dialog.querySelector('#runtime-execute').handlers.get('click')!(); await settle();
      assert.deepEqual(write, { version: '26.7.28', workspaceRevision: 1, settingsRevision: 1 });
    });
  }
});

test('应用、重启与恢复按本次实际版本提示兼容范围，不受另选核心或较新草稿误导', async t => {
  for (const action of ['apply', 'restart', 'resume'] as const) for (const candidate of [true, false]) {
    await t.test(`${action}-${candidate ? '候选' : '旧版'}`, async () => {
      const actual = candidate ? '26.10.17' : '26.7.28', other = candidate ? '26.7.28' : '26.10.17';
      const current = { ...(action === 'restart' ? running : stopped), version: action === 'apply' ? other : actual, startedAt: '2026-10-19T00:00:00Z' };
      const mutations: unknown[] = [];
      await withPage(async (path, init) => {
        if (init?.method === 'POST') { mutations.push(path); return response(current); }
        if (path === '/api/runtime/status') return response(current);
        if (path === '/api/core/status') return response({ selected: action === 'apply' ? actual : other });
        if (path === '/api/workspace') return response({ revision: 9, data: null });
        if (path === '/api/settings') return response({ revision: 3 });
        return response({ revision: 4, available: true, version: action === 'apply' ? other : actual, workspaceRevision: 1, settingsRevision: 1, inbounds: 1 });
      }, async dialog => {
        const runtime = initRuntime(() => {}); await settle(); runtime.open(action === 'resume' ? 'apply' : action); await settle();
        if (action === 'resume') dialog.querySelector('#runtime-resume').handlers.get('click')!();
        if (candidate) {
          assert.match(dialog.innerHTML, /Mihomo v1\.19\.32/);
          assert.match(dialog.innerHTML, /REALITY.*XHTTP-REALITY/);
          assert.match(dialog.innerHTML, /兼容模式.*未通过/);
          assert.match(dialog.innerHTML, /其他.*未验证/);
        } else assert.doesNotMatch(dialog.innerHTML, /Mihomo v1\.19\.32/);
        dialog.querySelector('#runtime-preflight').handlers.get('click')!(); await settle();
        assert.deepEqual(mutations, [`/api/runtime/${action === 'apply' ? 'preflight' : `${action}-preflight`}`]);
        if (candidate) assert.match(dialog.innerHTML, /Mihomo v1\.19\.32/, '预检成功不能撤下实际连接失败提示');
        if (action === 'resume') {
          dialog.querySelector('#runtime-back').handlers.get('click')!();
          if (candidate) assert.doesNotMatch(dialog.innerHTML, /Mihomo v1\.19\.32/, '返回旧版应用时清除候选历史的提示');
          else assert.match(dialog.innerHTML, /Mihomo v1\.19\.32/, '返回候选应用时恢复提示，不沿用旧版历史');
        }
      });
    });
  }
});

test('停止候选核心不加无关兼容警告、不增加草稿和版本查询', async () => {
  const reads: unknown[] = [];
  await withPage(async path => {
    reads.push(path); return response({ ...running, version: '26.10.17' });
  }, async dialog => {
    const runtime = initRuntime(() => {}); await settle(); runtime.open('stop'); await settle();
    assert.doesNotMatch(dialog.innerHTML, /Mihomo/);
    assert.deepEqual(reads, ['/api/runtime/status', '/api/runtime/status']);
  });
});
