import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { defaultSettings } from '../src/xray-settings-model.ts';

const hooks = registerHooks({ load(url, context, next) {
  return url.endsWith('.css') ? { format: 'module', source: '', shortCircuit: true } : next(url, context);
} });
const { initXraySettings } = await import('../src/xray-settings.ts');
hooks.deregister();

const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(resolve => setImmediate(resolve)); };
// 只模拟页面用到的根节点事件与查询，不作为真实DOM或浏览器验收。
class Node {
  innerHTML = ''; hidden = false; textContent = ''; disabled = false; open = false;
  handlers = new Map<string, (event: any) => void>();
  addEventListener(name: string, handler: (event: any) => void) { this.handlers.set(name, handler); }
  querySelector() { return null; }
  querySelectorAll() { return []; }
  contains() { return false; }
  focus() {}
  showModal() { this.open = true; }
  close() { this.open = false; }
}
function page(fetcher: (url: string, options?: RequestInit) => Promise<Response>, confirmation = { answer: true, count: 0 }) {
  const root = new Node();
  const values = {
    fetch: fetcher, location: { protocol: 'http:' }, CSS: { escape: (value: string) => value },
    window: { setInterval() {}, confirm() { confirmation.count++; return confirmation.answer; }, addEventListener() {} },
    document: { createElement: () => new Node(), body: { append() {} }, hidden: false, activeElement: null },
  };
  const previous = Object.fromEntries(Object.keys(values).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(values)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  const restore = () => { for (const [key, descriptor] of Object.entries(previous)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); } };
  const settings = initXraySettings(root as unknown as HTMLElement, () => {}, true);
  const click = (id: string) => { const button = { id, dataset: {}, closest: (selector: string) => selector === 'button' ? button : null }; root.handlers.get('click')!({ target: button }); };
  const change = (name: string, value: string) => root.handlers.get('change')!({ target: { name, value, type: 'text', dataset: {}, hasAttribute: () => false, closest: () => null } });
  return { root, settings, click, change, restore, confirmation };
}

test('Xray设置：刷新前确认，取消不丢未保存修改，确认后换成最新修订', async () => {
  let reads = 0; const writes: any[] = [];
  const view = page(async (url, options) => {
    if (options?.method === 'PUT') { writes.push(JSON.parse(String(options.body))); return Response.json({ revision: 10, applied: true }); }
    if (url.endsWith('/workspace')) return Response.json({ revision: 1, data: null });
    reads++; return Response.json({ revision: reads === 1 ? 1 : 9, data: { ...defaultSettings(), probeURL: reads === 1 ? 'https://first.test/' : 'https://latest.test/' } });
  });
  try {
    view.settings.render(); await settle();
    assert.match(view.root.innerHTML, /id="xray-reload"/); assert.match(view.root.innerHTML, /重启 Xray/);
    view.change('probeURL', 'https://unsaved.test/');
    assert.match(view.root.innerHTML, /unsaved\.test/);
    view.confirmation.answer = false; view.click('xray-reload'); await settle();
    assert.equal(reads, 1); assert.match(view.root.innerHTML, /unsaved\.test/);
    view.confirmation.answer = true; view.click('xray-reload'); await settle();
    assert.equal(reads, 2); assert.match(view.root.innerHTML, /latest\.test/);
    view.change('logLevel', 'info'); view.click('xray-save'); await settle();
    assert.equal(writes.length, 1); assert.equal(writes[0].revision, 9); assert.equal(writes[0].data.logLevel, 'info'); assert.equal(writes[0].data.probeURL, 'https://latest.test/');
    assert.match(view.root.innerHTML, /已保存并生效/);
  } finally { view.restore(); }
});

test('Xray设置：保存冲突或响应丢失后保留修改，不重复写旧修订', async t => {
  for (const network of [false, true]) await t.test(network ? '网络响应丢失' : '修订冲突', async () => {
    let writes = 0;
    const view = page(async (url, options) => {
      if (options?.method === 'PUT') { writes++; if (network) throw new Error('lost'); return Response.json({ error: '修订冲突' }, { status: 409 }); }
      if (url.endsWith('/workspace')) return Response.json({ revision: 1, data: null });
      return Response.json({ revision: 1, data: defaultSettings() });
    });
    try {
      view.settings.render(); await settle();
      view.change('probeURL', 'https://keep.test/'); view.click('xray-save'); await settle();
      assert.equal(writes, 1); assert.match(view.root.innerHTML, /keep\.test/); assert.match(view.root.innerHTML, /请先刷新|refresh before/);
      view.click('xray-save'); await settle(); assert.equal(writes, 1);
    } finally { view.restore(); }
  });
});

test('Xray设置：读取在途不保存、不并发读取；校验失败可修正后重试', async () => {
  let reads = 0, writes = 0, release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  const view = page(async (url, options) => {
    if (options?.method === 'PUT') { writes++; return writes === 1 ? Response.json({ error: '测试URL无效' }, { status: 400 }) : Response.json({ revision: 2, applied: true }); }
    if (url.endsWith('/workspace')) return Response.json({ revision: 1, data: null });
    reads++; await pending; return Response.json({ revision: 1, data: defaultSettings() });
  });
  try {
    view.settings.render(); view.click('xray-reload'); view.click('xray-save'); await settle();
    assert.equal(reads, 1); assert.equal(writes, 0);
    release(); await settle();
    view.change('probeURL', 'https://retry.test/'); view.click('xray-save'); await settle();
    assert.equal(writes, 1); assert.match(view.root.innerHTML, /测试URL无效/);
    view.click('xray-save'); await settle();
    assert.equal(writes, 2, '明确校验失败不锁成需刷新');
  } finally { view.restore(); }
});
