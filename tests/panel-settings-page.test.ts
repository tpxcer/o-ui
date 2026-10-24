import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const hooks = registerHooks({ load(url, context, next) {
  return url.endsWith('.css') ? { format: 'module', source: '', shortCircuit: true } : next(url, context);
} });
const { initPanelSettings } = await import('../src/panel-settings.ts');
hooks.deregister();

const settle = async () => { for (let i = 0; i < 8; i++) await new Promise(resolve => setImmediate(resolve)); };
// 只模拟页面用到的根节点事件与查询：querySelector按选择器返回稳定的桩元素，不作为真实DOM或浏览器验收。
class Node {
  innerHTML = ''; hidden = false; textContent = ''; disabled = false; open = false; value = ''; checked = false; className = '';
  dataset: Record<string, string> = {};
  handlers = new Map<string, (event: any) => void>();
  elements = new Map<string, Node>();
  addEventListener(name: string, handler: (event: any) => void) { this.handlers.set(name, handler); }
  querySelector(selector: string) { if (!this.elements.has(selector)) this.elements.set(selector, new Node()); return this.elements.get(selector)!; }
  querySelectorAll() { return []; }
  setAttribute() {}
  remove() {}
  onConfirm?: () => boolean;
  focus() {}
  showModal() { this.open = true; if (this.className === 'confirm-dialog') queueMicrotask(() => this.querySelector(this.onConfirm?.() ? '[data-confirm-accept]' : '[data-confirm-cancel]').handlers.get('click')!({})); }
  close() { this.open = false; }
}
type Fetcher = (path: string, method: string, body: any) => Promise<Response>;
const general = (minutes = 720, revision = 0) => ({
  access: { revision: 0, data: null, active: { public: false, listen: '127.0.0.1:5180', origin: '' }, pending: false },
  tls: { revision: 0, data: null, active: false, serverNames: [], pending: false }, session: { revision, minutes },
});
// 默认读取：第n次读取会话时长为 720+n，修订为 n-1，便于看出是否换成了最新设置
function page(write: Fetcher, options: { read?: () => Promise<void>; twoFactor?: Fetcher } = {}) {
  const root = new Node(), dialog = new Node(), confirmation = { answer: true, count: 0 }, calls: { path: string; method: string; body: any }[] = [];
  let created = false;
  let reads = 0;
  const fetcher = async (url: string, init?: RequestInit) => {
    const path = String(url).replace(/^\/api\//, ''), method = init?.method ?? 'GET', body = init?.body ? JSON.parse(String(init.body)) : null;
    calls.push({ path, method, body });
    if (path.startsWith('auth/2fa/')) return options.twoFactor!(path, method, body);
    if (method !== 'GET') return write(path, method, body);
    if (path === 'panel/general') { reads++; await options.read?.(); return Response.json(general(720 + reads, reads - 1)); }
    if (path === 'system/plan') return Response.json({ revision: 0, data: null, usage: null, issue: '' });
    if (path === 'panel/subscription') return Response.json({ revision: 0, data: { enabled: true, updateHours: 12, title: 'o-ui', showInfo: true, remarkClientName: true, clashEnabled: true, jsonEnabled: true, jsonFragment: false, fragmentPackets: 'tlshello', fragmentLength: '100-200', fragmentInterval: '10-20', jsonDirectIP: [], jsonDirectDomain: [] } });
    if (path === 'auth/status') return Response.json({ initialized: true, authenticated: true, username: 'pageqa', twoFactor: false });
    if (path === 'auth/2fa') return Response.json({ enabled: false });
    if (path === 'panel/telegram') return Response.json({ revision: 0, data: { enabled: false, chatIds: '', reportTime: '', notifyLogin: false, cpuThreshold: 0, backupEnabled: false, tokenSet: false, backupPasswordSet: false } });
    return Response.json({ interfaces: ['eth0'] });
  };
  const values = {
    fetch: fetcher,
    location: { protocol: 'http:', origin: 'http://127.0.0.1:5180', pathname: '/', host: '127.0.0.1:5180', reload() {} },
    window: { addEventListener() {}, removeEventListener() {}, dispatchEvent() { return true; } },
    document: { createElement: () => { const node = created ? new Node() : dialog; created = true; node.onConfirm = () => { confirmation.count++; return confirmation.answer; }; return node; }, body: { append() {} } },
  };
  const previous = Object.fromEntries(Object.keys(values).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(values)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  const restore = () => { for (const [key, descriptor] of Object.entries(previous)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); } };
  const settings = initPanelSettings(root as unknown as HTMLElement, true);
  const click = (selector: string) => { const target: any = { dataset: {}, closest: (s: string) => s === selector ? target : null }; root.handlers.get('click')!({ target }); };
  const input = (name: string, value: string) => root.handlers.get('input')!({ target: { name, value, type: 'text', closest: () => ({}) } });
  const writes = (path = 'panel/general') => calls.filter(call => call.path === path && call.method !== 'GET');
  return { root, dialog, settings, click, input, restore, confirmation, calls, writes, reads: () => reads };
}

test('面板设置：保存冲突或响应丢失后保留输入，不再写旧修订，须重新读取', async t => {
  for (const network of [false, true]) await t.test(network ? '网络响应丢失' : '修订冲突', async () => {
    const view = page(async () => { if (network) throw new Error('lost'); return Response.json({ error: '数据已被其他页面修改' }, { status: 409 }); });
    try {
      view.settings.render(); await settle();
      view.input('sessionMinutes', '60');
      assert.equal(view.root.querySelector('#settings-save').disabled, false);
      view.click('#settings-save'); await settle();
      assert.equal(view.writes().length, 1);
      assert.equal(view.writes()[0].body.sessionRevision, 0);
      assert.match(view.root.innerHTML, /name="sessionMinutes" value="60"/, '重绘后保留未保存的输入');
      assert.match(view.root.innerHTML, /重新读取/);
      assert.equal(view.root.querySelector('#settings-save').disabled, true);
      view.click('#settings-save'); await settle();
      assert.equal(view.writes().length, 1, '不得再用旧修订写入');
      // 重新读取后换成最新修订，才能再次保存
      view.click('#settings-reload'); await settle();
      assert.equal(view.reads(), 2); assert.match(view.root.innerHTML, /name="sessionMinutes" value="722"/);
      assert.equal(view.root.querySelector('#settings-save').disabled, true, '刚读取的设置没有改动');
    } finally { view.restore(); }
  });
});

test('面板设置：保存进行中不重复保存，也不重新读取或重启', async () => {
  let release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  const view = page(async (_path, _method, body) => { await pending; return Response.json(general(body.sessionMinutes, 1)); });
  try {
    view.settings.render(); await settle();
    view.input('sessionMinutes', '61');
    view.click('#settings-save'); view.click('#settings-save'); view.click('#settings-reload'); view.click('#panel-restart'); await settle();
    assert.equal(view.writes().length, 1);
    assert.equal(view.reads(), 1);
    assert.equal(view.confirmation.count, 0, '保存中点重新读取或重启不弹确认也不执行');
    assert.equal(view.calls.some(call => call.path === 'panel/restart'), false);
    assert.equal(view.root.querySelector('#settings-save').disabled, true);
    assert.equal(view.root.querySelector('#panel-restart').disabled, true);
    release(); await settle();
    assert.match(view.root.innerHTML, /已保存/);
    assert.match(view.root.innerHTML, /name="sessionMinutes" value="61"/);
    assert.equal(view.root.querySelector('#settings-save').disabled, true);
  } finally { view.restore(); }
});

test('面板设置：重新读取前确认，取消不丢未保存修改，确认后换成最新设置', async () => {
  const view = page(async (_path, _method, body) => Response.json(general(body.sessionMinutes, body.sessionRevision + 1)));
  try {
    view.settings.render(); await settle();
    view.input('sessionMinutes', '45');
    view.confirmation.answer = false; view.click('#settings-reload'); await settle();
    assert.equal(view.confirmation.count, 1); assert.equal(view.reads(), 1);
    view.click('#settings-save'); await settle();
    assert.equal(view.writes().length, 1); assert.equal(view.writes()[0].body.sessionMinutes, 45, '取消重新读取后未保存的修改仍在');
    view.input('sessionMinutes', '50');
    view.confirmation.answer = true; view.click('#settings-reload'); await settle();
    assert.equal(view.reads(), 2); assert.match(view.root.innerHTML, /name="sessionMinutes" value="722"/);
    // 没有改动时重新读取不需确认
    const asked = view.confirmation.count;
    view.click('#settings-reload'); await settle();
    assert.equal(view.confirmation.count, asked); assert.equal(view.reads(), 3);
  } finally { view.restore(); }
});

test('面板设置：读取在途不保存；服务器校验失败（400）修正后可用原修订重试', async () => {
  let release!: () => void, attempts = 0;
  const pending = new Promise<void>(resolve => { release = resolve; });
  const view = page(async (_path, _method, body) => {
    attempts++;
    return attempts === 1 ? Response.json({ error: '会话时长须为5至43200分钟' }, { status: 400 }) : Response.json(general(body.sessionMinutes, 1));
  }, { read: () => pending });
  try {
    view.settings.render(); view.click('#settings-save'); view.click('#settings-reload'); await settle();
    assert.equal(view.reads(), 1); assert.equal(view.writes().length, 0);
    release(); await settle();
    view.input('sessionMinutes', '60'); view.click('#settings-save'); await settle();
    assert.equal(view.writes().length, 1); assert.match(view.root.innerHTML, /会话时长须为5至43200分钟/);
    assert.doesNotMatch(view.root.innerHTML, /结果无法确认/, '明确校验失败不锁成需重新读取');
    assert.equal(view.root.querySelector('#settings-save').disabled, false);
    view.input('sessionMinutes', '61'); view.click('#settings-save'); await settle();
    assert.equal(view.writes().length, 2);
    assert.deepEqual([view.writes()[1].body.sessionMinutes, view.writes()[1].body.sessionRevision], [61, 0]);
    // 前端校验不通过时不发请求
    view.input('sessionMinutes', '4'); view.click('#settings-save'); await settle();
    assert.equal(view.writes().length, 2);
  } finally { view.restore(); }
});

test('面板设置：开启两步验证时验证码错误不会启用，改正后才启用', async () => {
  let enables = 0;
  const view = page(async () => Response.json({}), { twoFactor: async (path, _method, body) => {
    if (path === 'auth/2fa/setup') return Response.json({ secret: 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP', uri: 'otpauth://totp/o-ui:pageqa?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=o-ui' });
    enables++;
    return body.code === '123456' ? Response.json({ enabled: true }) : Response.json({ error: '验证码错误' }, { status: 403 });
  } });
  try {
    view.settings.render(); await settle();
    const toggle: any = { name: 'twoFactor', checked: true, type: 'checkbox', closest: (s: string) => s === '#settings-twofactor' ? {} : null };
    view.root.handlers.get('change')!({ target: toggle }); await settle();
    assert.equal(toggle.checked, false, '确认前开关不显示为已启用');
    assert.equal(view.dialog.open, true); assert.match(view.dialog.innerHTML, /JBSWY3DPEHPK3PXP/); assert.match(view.dialog.innerHTML, /<img src="data:image\/svg\+xml/);
    const submit = () => view.dialog.querySelector('form').handlers.get('submit')!({ preventDefault() {} });
    view.dialog.querySelector('[name="code"]').value = '12345'; submit(); await settle();
    assert.equal(enables, 0); assert.match(view.dialog.querySelector('[role="alert"]').textContent, /6位验证码/);
    view.dialog.querySelector('[name="code"]').value = '000000'; submit(); await settle();
    assert.equal(enables, 1); assert.equal(view.dialog.open, true);
    assert.match(view.dialog.querySelector('[role="alert"]').textContent, /验证码错误/);
    assert.match(view.root.innerHTML, /name="twoFactor" +aria-label/, '错误验证码后页面仍为未启用');
    assert.doesNotMatch(view.root.innerHTML, /name="code"/, '未启用时改账号不要求验证码');
    view.dialog.querySelector('[name="code"]').value = '123456'; submit(); await settle();
    assert.equal(enables, 2); assert.equal(view.dialog.open, false);
    assert.match(view.root.innerHTML, /name="twoFactor" checked/);
    assert.match(view.root.innerHTML, /双因素认证已成功建立/);
    assert.match(view.root.innerHTML, /name="code"/, '启用后改账号需要验证码');
  } finally { view.restore(); }
});
