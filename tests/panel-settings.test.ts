import test from 'node:test';
import assert from 'node:assert/strict';
import { initPanelSettings } from '../src/panel-settings.ts';
import { initPanelAccess } from '../src/panel-access.ts';

const settle = async () => { for (let i = 0; i < 5; i++) await new Promise(resolve => setImmediate(resolve)); };
const saved = (revision = 1, certificateFile = '/test/first.crt', keyFile = '/test/first.key') => ({
  active: false, serverNames: [], activeRevision: 0, revision, data: { certificateFile, keyFile },
});

// 只模拟本组件用到的表单控件，不将此测试作为真实DOM或浏览器验收。
class Control {
  value = ''; disabled = false; hidden = false; isConnected = true; textContent = ''; className = '';
  name = ''; tag = ''; html = ''; focused = false; checked = false;
  children: Control[] = []; fields: Control[] = [];
  controls = new Map<string, Control>();
  handlers = new Map<string, (event: any) => void>();
  get innerHTML() { return this.html; }
  set innerHTML(value: string) {
    this.html = value; this.controls.clear(); this.fields = [];
    for (const match of value.matchAll(/<(input|button|p|form)\b([^>]*)>/g)) {
      const item = new Control(), attributes = match[2]; item.tag = match[1];
      item.value = attributes.match(/\bvalue="([^"]*)"/)?.[1] ?? '';
      item.name = attributes.match(/\bname="([^"]*)"/)?.[1] ?? '';
      item.disabled = /\sdisabled(?:\s|$)/.test(attributes);
      item.hidden = /\shidden(?:\s|$)/.test(attributes);
      item.checked = /\schecked(?:\s|$)/.test(attributes);
      const id = attributes.match(/\bid="([^"]*)"/)?.[1], role = attributes.match(/\brole="([^"]*)"/)?.[1];
      if (id) this.controls.set(`#${id}`, item);
      if (role) this.controls.set(`[role="${role}"]`, item);
      if (item.name) this.controls.set(`[name="${item.name}"]`, item);
      for (const attribute of ['data-retry', 'data-reload', 'data-access-reload', 'data-plan-reload']) if (attributes.includes(attribute)) this.controls.set(`[${attribute}]`, item);
      if (item.tag === 'form') this.controls.set('form', item);
      if (item.tag === 'input' || item.tag === 'button') this.fields.push(item);
    }
    const form = this.controls.get('form');
    if (form) { form.controls = this.controls; form.fields = this.fields; }
  }
  append(child: Control) { this.children.push(child); }
  querySelector(selector: string): Control | null {
    return this.controls.get(selector) ?? this.children.map(child => child.querySelector(selector)).find(Boolean) ?? null;
  }
  querySelectorAll(selector: string): Control[] {
    return [...this.fields, ...this.children.flatMap(child => child.querySelectorAll(selector))].filter(item => selector.split(',').includes(item.tag));
  }
  addEventListener(name: string, handler: (event: any) => void) { this.handlers.set(name, handler); }
  focus() { this.focused = true; }
  fire(name = 'click') { this.handlers.get(name)?.({ preventDefault() {}, target: this }); }
}
function control(root: Control, selector: string): Control { const item = root.querySelector(selector); assert.ok(item, `缺少控件 ${selector}`); return item; }
// 页面结构：顶部操作区、设置卡片（标签栏+常规/账号安全/订阅三个面板）；证书区是“常规”的第一块。
const tlsBlock = (root: Control) => root.children[1].children[1].children[0];
async function withPage(fetcher: typeof fetch, run: (root: Control, confirmation: { answer: boolean; count: number }) => Promise<void>, stubAccess = true) {
  const root = new Control(), confirmation = { answer: true, count: 0 };
  const values = {
    fetch: ((url, options) => {
      if (stubAccess && String(url) === '/api/panel/access') return Promise.resolve(Response.json({ active: { public: false, listen: '127.0.0.1:5180', origin: '' }, activeRevision: 0, revision: 0, data: null }));
      // 服务器套餐是同页独立分区，这里只占位，不计入HTTPS请求断言。
      if (String(url) === '/api/system/plan') return Promise.resolve(Response.json({ revision: 0, data: null, usage: null, issue: '' }));
      if (String(url) === '/api/system/status') return Promise.resolve(Response.json({ network: null }));
      return fetcher(url, options);
    }) as typeof fetch, location: { protocol: 'http:' },
    window: { confirm() { confirmation.count++; return confirmation.answer; }, dispatchEvent() {} },
    document: { createElement() { return new Control(); } },
    FormData: class { form: Control; constructor(form: Control) { this.form = form; } get(name: string) { return this.form.querySelector(`[name="${name}"]`)?.value ?? null; } },
  };
  const previous = Object.fromEntries(Object.keys(values).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(values)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  try { await run(root, confirmation); }
  finally { for (const [key, descriptor] of Object.entries(previous)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); } }
}

test('HTTPS返回页面不重绘未保存路径，也不影响独立密码表单', async () => {
  let reads = 0;
  await withPage(async () => { reads++; return Response.json(saved()); }, async root => {
    const panel = initPanelSettings(root as unknown as HTMLElement, true); panel.render(); await settle();
    const certificate = control(root, '#panel-cert'), key = control(root, '#panel-key'), password = control(root, '#password-current');
    certificate.value = '/test/unsaved.crt'; key.value = '/test/unsaved.key'; password.value = 'test-only-input';
    panel.render();
    assert.equal(control(root, '#panel-cert'), certificate); assert.equal(certificate.value, '/test/unsaved.crt');
    assert.equal(control(root, '#panel-key').value, '/test/unsaved.key');
    assert.equal(control(root, '#password-current'), password); assert.equal(password.value, 'test-only-input'); assert.equal(reads, 1);
    panel.clearPassword(); assert.equal(password.value, ''); assert.equal(certificate.value, '/test/unsaved.crt');
  });
});

test('HTTPS显式重读取消不请求，失败保留输入，成功换修订且不清密码区', async () => {
  let reads = 0; const writes: any[] = [];
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') { const value = JSON.parse(String(options.body)); writes.push(value); return Response.json(saved(10, value.data.certificateFile, value.data.keyFile)); }
    if (++reads === 2) throw new Error('offline');
    return Response.json(reads === 1 ? saved() : { ...saved(9, '/test/latest.crt', '/test/latest.key'), active: true, activeRevision: 8, serverNames: ['test.invalid'] });
  }, async (root, confirmation) => {
    initPanelSettings(root as unknown as HTMLElement, true).render(); await settle();
    const certificate = control(root, '#panel-cert'), password = control(root, '#password-current');
    certificate.value = '/test/keep.crt'; password.value = 'test-only-input';
    confirmation.answer = false; control(root, '[data-reload]').fire(); await settle();
    assert.equal(reads, 1); assert.equal(certificate.value, '/test/keep.crt');
    confirmation.answer = true; control(root, '[data-reload]').fire(); await settle();
    assert.equal(reads, 2); assert.equal(control(root, '#panel-cert'), certificate); assert.equal(certificate.value, '/test/keep.crt');
    assert.match(control(root, '[role="alert"]').textContent, /保留/);
    control(root, '[data-reload]').fire(); await settle();
    assert.equal(control(root, '#panel-cert').value, '/test/latest.crt');
    assert.match(tlsBlock(root).innerHTML, /待重启/); assert.equal(password.value, 'test-only-input');
    control(root, '#panel-tls-form').fire('submit'); await settle();
    assert.equal(writes.length, 1); assert.equal(writes[0].revision, 9); assert.equal(writes[0].data.certificateFile, '/test/latest.crt');
    assert.equal(confirmation.count, 3); assert.equal(password.value, 'test-only-input');
  });
});

test('HTTPS冲突或响应不确定后不得重复写旧修订，重读失败仍保留输入', async t => {
  for (const failure of ['conflict', 'network', 'server', 'invalid-json']) await t.test(failure, async () => {
    let writes = 0, reads = 0;
    await withPage(async (_, options) => {
      if (options?.method === 'PUT') {
        writes++;
        if (writes > 1) { assert.equal(JSON.parse(String(options.body)).revision, 9); return Response.json(saved(10)); }
        if (failure === 'network') throw new Error('lost');
        if (failure === 'invalid-json') return new Response('{invalid', { headers: { 'Content-Type': 'application/json' } });
        return Response.json({ error: 'test failure' }, { status: failure === 'conflict' ? 409 : 500 });
      }
      if (++reads === 2) throw new Error('offline');
      return Response.json(saved(reads === 1 ? 1 : 9));
    }, async root => {
      const panel = initPanelSettings(root as unknown as HTMLElement, true); panel.render(); await settle();
      const certificate = control(root, '#panel-cert'); certificate.value = '/test/keep.crt';
      control(root, '#panel-tls-form').fire('submit'); await settle();
      assert.equal(certificate.value, '/test/keep.crt'); assert.match(control(root, '[role="alert"]').textContent, /重新读取/);
      control(root, '#panel-tls-form').fire('submit'); await settle(); assert.equal(writes, 1);
      control(root, '[data-reload]').fire(); await settle();
      panel.render(); assert.equal(control(root, '#panel-cert'), certificate); assert.equal(certificate.value, '/test/keep.crt');
      control(root, '#panel-tls-form').fire('submit'); await settle(); assert.equal(writes, 1);
      control(root, '[data-reload]').fire(); await settle();
      control(root, '#panel-tls-form').fire('submit'); await settle(); assert.equal(writes, 2);
    });
  });
});

test('HTTPS明确证书校验失败可修正后重试，仍使用原修订', async () => {
  let writes = 0;
  await withPage(async (_, options) => {
    if (options?.method !== 'PUT') return Response.json(saved());
    const value = JSON.parse(String(options.body)); assert.equal(value.revision, 1);
    return ++writes === 1 ? Response.json({ error: '证书不匹配' }, { status: 400 }) : Response.json(saved(2, value.data.certificateFile, value.data.keyFile));
  }, async root => {
    const panel = initPanelSettings(root as unknown as HTMLElement, true); panel.render(); await settle();
    control(root, '#panel-cert').value = '/test/invalid.crt'; control(root, '#panel-tls-form').fire('submit'); await settle();
    panel.render(); assert.equal(control(root, '#panel-cert').value, '/test/invalid.crt');
    assert.equal(control(root, '#panel-cert').disabled, false); assert.equal(control(root, '[role="alert"]').focused, true);
    control(root, '#panel-cert').value = '/test/correct.crt'; control(root, '#panel-tls-form').fire('submit'); await settle();
    assert.equal(writes, 2); assert.equal(control(root, '#panel-cert').value, '/test/correct.crt');
    assert.match(tlsBlock(root).innerHTML, /重启面板/);
  });
});

test('HTTPS首次读取失败可重试，空设置不伪造证书且保存使用修订0', async () => {
  let reads = 0, writes = 0;
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') {
      writes++; const value = JSON.parse(String(options.body)); assert.equal(value.revision, 0);
      assert.deepEqual(value.data, { certificateFile: '/test/new.crt', keyFile: '/test/new.key' });
      return Response.json(saved());
    }
    if (++reads === 1) throw new Error('offline');
    return Response.json({ ...saved(0), data: null });
  }, async (root, confirmation) => {
    initPanelSettings(root as unknown as HTMLElement, true).render(); await settle();
    assert.equal(control(root, '#panel-cert').disabled, true); assert.equal(control(root, '#panel-tls-save').disabled, true);
    assert.equal(control(root, '[data-reload]').disabled, false);
    control(root, '#panel-tls-form').fire('submit'); await settle(); assert.equal(writes, 0);
    control(root, '[data-reload]').fire(); await settle();
    assert.equal(confirmation.count, 0); assert.equal(control(root, '#panel-cert').value, ''); assert.equal(control(root, '#panel-key').value, '');
    assert.equal(control(root, '#panel-cert').disabled, false);
    control(root, '#panel-cert').value = ' /test/new.crt '; control(root, '#panel-key').value = '/test/new.key';
    control(root, '#panel-tls-form').fire('submit'); await settle(); assert.equal(writes, 1);
  });
});

test('HTTPS慢读取期间不重复请求或保存，失败恢复原控件和路径', async () => {
  let complete!: (value: Response) => void;
  const pending = new Promise<Response>(resolve => { complete = resolve; });
  let reads = 0, writes = 0;
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') { writes++; return Response.json(saved(2)); }
    return ++reads === 1 ? Response.json(saved()) : pending;
  }, async root => {
    const panel = initPanelSettings(root as unknown as HTMLElement, true); panel.render(); await settle();
    const certificate = control(root, '#panel-cert'), password = control(root, '#password-current');
    certificate.value = '/test/keep.crt'; password.value = 'test-only-input';
    control(root, '[data-reload]').fire(); await settle();
    assert.equal(certificate.disabled, true); assert.equal(password.disabled, false);
    assert.equal(control(root, '#panel-tls-save').disabled, true); assert.equal(control(root, '[data-reload]').disabled, true);
    panel.render(); control(root, '[data-reload]').fire(); control(root, '#panel-tls-form').fire('submit'); await settle();
    assert.equal(reads, 2); assert.equal(writes, 0); assert.equal(control(root, '#panel-cert'), certificate);
    complete(Response.json({ error: '读取失败' }, { status: 500 })); await settle();
    assert.equal(certificate.disabled, false); assert.equal(certificate.value, '/test/keep.crt'); assert.equal(password.value, 'test-only-input');
    assert.match(control(root, '[role="alert"]').textContent, /原路径输入仍保留/);
  });
});

test('HTTPS慢保存期间不重读或重复写入，返回页面不重绘，成功后保持待重启语义', async () => {
  let complete!: (value: Response) => void;
  const pending = new Promise<Response>(resolve => { complete = resolve; });
  let reads = 0, writes = 0;
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') { writes++; return pending; }
    reads++; return Response.json(saved());
  }, async (root, confirmation) => {
    const panel = initPanelSettings(root as unknown as HTMLElement, true); panel.render(); await settle();
    const certificate = control(root, '#panel-cert'), password = control(root, '#password-current');
    certificate.value = '/test/new.crt'; password.value = 'test-only-input';
    control(root, '#panel-tls-form').fire('submit'); await settle();
    assert.equal(certificate.disabled, true); assert.equal(password.disabled, false);
    assert.match(control(tlsBlock(root), '[role="status"]').textContent, /校验证书/);
    panel.render(); control(root, '#panel-tls-form').fire('submit'); control(root, '[data-reload]').fire(); await settle();
    assert.equal(control(root, '#panel-cert'), certificate); assert.equal(writes, 1); assert.equal(reads, 1); assert.equal(confirmation.count, 0);
    complete(Response.json(saved(2, '/test/new.crt', '/test/first.key'))); await settle();
    assert.equal(control(root, '#panel-cert').value, '/test/new.crt'); assert.equal(control(root, '#panel-cert').disabled, false);
    assert.match(tlsBlock(root).innerHTML, /重启面板/); assert.match(tlsBlock(root).innerHTML, /待重启/);
    assert.equal(password.value, 'test-only-input');
  });
});

test('HTTPS离线演示不读取或保存真实设置，不初始化密码功能', async () => {
  let requests = 0;
  await withPage(async () => { requests++; throw new Error('unexpected'); }, async root => {
    const panel = initPanelSettings(root as unknown as HTMLElement, false); panel.render(); panel.render(); panel.clearPassword(); await settle();
    assert.equal(requests, 0); assert.equal(root.querySelector('#panel-tls-form'), null); assert.equal(root.querySelector('#password-current'), null);
    assert.match(tlsBlock(root).innerHTML, /离线演示不保存/);
  });
});

const accessStatus = (revision = 0) => ({ active: { public: false, listen: '127.0.0.1:5180', origin: '' }, activeRevision: 0, revision, data: null });
test('访问设置默认本机、返回页不丢输入、非本机保存须逐次确认且不自动切换', async () => {
  let reads = 0, writes = 0;
  await withPage(async (_, options) => {
    if (options?.method !== 'PUT') { reads++; return Response.json(accessStatus()); }
    writes++; const value = JSON.parse(String(options.body));
    assert.equal(value.confirmation, '确认启用HTTPS非本机访问'); assert.equal(value.revision, 0);
    return Response.json({ ...accessStatus(1), data: value.data });
  }, async root => {
    const panel = initPanelAccess(root as unknown as HTMLElement); panel.render(); await settle();
    assert.equal(control(root, '#access-public').checked, false);
    const listen = control(root, '#access-listen'); listen.value = '127.0.0.1:5190';
    control(root, '#access-public').checked = true; control(root, '#access-origin').value = 'https://panel.test:5190';
    panel.render(); assert.equal(control(root, '#access-listen'), listen); assert.equal(reads, 1);
    control(root, '#access-form').fire('submit'); await settle(); assert.equal(writes, 0);
    control(root, '#access-confirm').checked = true; control(root, '#access-form').fire('submit'); await settle();
    assert.equal(writes, 1); assert.equal(control(root, '#access-confirm').checked, false);
    assert.equal(control(root, '#access-listen').value, '127.0.0.1:5190'); assert.match(root.innerHTML, /有待重启/);
    assert.match(control(root, '[role="status"]').textContent, /重启面板/);
  }, false);
});

test('访问设置慢保存锁控件、冲突保留输入并禁止重写、显式重读取消与失败不丢内容', async () => {
  let reads = 0, writes = 0, complete!: (value: Response) => void;
  const pending = new Promise<Response>(resolve => { complete = resolve; });
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') { writes++; return pending; }
    if (++reads === 2) throw new Error('offline');
    return Response.json(accessStatus(reads === 1 ? 0 : 4));
  }, async (root, confirmation) => {
    const panel = initPanelAccess(root as unknown as HTMLElement); panel.render(); await settle();
    const listen = control(root, '#access-listen'); listen.value = '127.0.0.1:5191';
    control(root, '#access-form').fire('submit'); await settle();
    assert.equal(listen.disabled, true); panel.render(); control(root, '#access-form').fire('submit'); control(root, '[data-access-reload]').fire();
    assert.equal(writes, 1); assert.equal(reads, 1); assert.equal(control(root, '#access-listen'), listen);
    complete(Response.json({ error: '修订冲突' }, { status: 409 })); await settle();
    assert.equal(listen.disabled, false); assert.equal(listen.value, '127.0.0.1:5191'); assert.equal(control(root, '#access-save').disabled, true);
    control(root, '#access-form').fire('submit'); await settle(); assert.equal(writes, 1);
    confirmation.answer = false; control(root, '[data-access-reload]').fire(); await settle(); assert.equal(reads, 1);
    confirmation.answer = true; control(root, '[data-access-reload]').fire(); await settle();
    assert.equal(control(root, '#access-listen'), listen); assert.equal(listen.value, '127.0.0.1:5191'); assert.equal(control(root, '#access-save').disabled, true);
    control(root, '[data-access-reload]').fire(); await settle(); assert.equal(control(root, '#access-save').disabled, false);
    assert.equal(control(root, '#access-listen').value, '127.0.0.1:5180');
  }, false);
});

test('访问设置首次读取失败可重试，校验失败可修正，网络不确定后须重读', async () => {
  let reads = 0, writes = 0;
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') {
      if (++writes === 1) return Response.json({ error: '地址无效' }, { status: 400 });
      throw new Error('offline');
    }
    if (++reads === 1) throw new Error('offline'); return Response.json(accessStatus());
  }, async root => {
    initPanelAccess(root as unknown as HTMLElement).render(); await settle(); assert.equal(control(root, '#access-save').disabled, true);
    control(root, '[data-access-reload]').fire(); await settle();
    control(root, '#access-listen').value = 'invalid'; control(root, '#access-form').fire('submit'); await settle();
    assert.equal(control(root, '#access-save').disabled, false); assert.equal(control(root, '#access-listen').value, 'invalid');
    control(root, '#access-listen').value = '127.0.0.1:5190'; control(root, '#access-form').fire('submit'); await settle();
    assert.equal(control(root, '#access-save').disabled, true); assert.equal(control(root, '#access-listen').value, '127.0.0.1:5190');
    control(root, '#access-form').fire('submit'); await settle(); assert.equal(writes, 2);
  }, false);
});
