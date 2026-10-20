import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import type { CoreStatus } from '../src/core.ts';

const hooks = registerHooks({ load(url, context, next) {
  return url.endsWith('.css') ? { format: 'module', source: '', shortCircuit: true } : next(url, context);
} });
const { initCore } = await import('../src/core.ts');
hooks.deregister();

const settle = async () => { for (let i = 0; i < 5; i++) await new Promise(resolve => setImmediate(resolve)); };
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { resolve, promise }; }
const status = (selected = '26.7.28', busy = false): CoreStatus => ({
  selected, busy, defaultVersion: '26.7.28', previous: selected === '26.7.28' ? '26.3.27' : '26.7.28', installed: ['26.7.28', '26.3.27'], platform: 'test', runtime: 'not-started',
});
const releases = () => Response.json({ releases: [{ version: '26.7.28', prerelease: true, url: 'https://github.com/XTLS/Xray-core/releases/tag/v26.7.28' }] });
class Control {
  id = ''; value = ''; name = ''; disabled = false; checked = false; hidden = false; isConnected = true; textContent = ''; scrollTop = 0;
  handlers = new Map<string, (event: any) => void>();
  addEventListener(name: string, handler: (event: any) => void) { this.handlers.set(name, handler); }
  fire(name = 'click') { this.handlers.get(name)?.({ target: this }); }
  focus() {}
  getClientRects() { return [1]; }
}
// 仅用于操作顺序与最小控件状态，不模拟真实浏览器布局或原生对话框。
class Dialog extends Control {
  open = false; className = ''; html = '';
  controls = new Map<string, Control>();
  radios: Control[] = []; cancel: Control[] = [];
  get innerHTML() { return this.html; }
  set innerHTML(value: string) {
    this.html = value; this.controls.clear(); this.radios = []; this.cancel = [];
    for (const match of value.matchAll(/<(input|button|div|p)\b([^>]*)>/g)) {
      const attributes = match[2], item = new Control();
      item.id = attributes.match(/\bid="([^"]*)"/)?.[1] ?? '';
      item.value = attributes.match(/\bvalue="([^"]*)"/)?.[1] ?? '';
      item.name = attributes.match(/\bname="([^"]*)"/)?.[1] ?? '';
      item.disabled = /\sdisabled(?:\s|$)/.test(attributes); item.checked = /\schecked(?:\s|$)/.test(attributes);
      item.hidden = /\shidden(?:\s|$)/.test(attributes);
      if (item.id) this.controls.set(`#${item.id}`, item);
      if (item.name === 'core-picker-version') this.radios.push(item);
      if (attributes.includes('data-cancel')) this.cancel.push(item);
      if (/class="[^"]*\bcore-release-list\b/.test(attributes)) this.controls.set('.core-release-list', item);
    }
  }
  setAttribute() {}
  showModal() { this.open = true; }
  close() { this.open = false; this.fire('close'); }
  contains() { return false; }
  querySelector(selector: string) { return this.controls.get(selector) ?? null; }
  querySelectorAll(selector: string): Control[] {
    if (selector === '[name="core-picker-version"]') return this.radios;
    if (selector === '[data-cancel]') return this.cancel;
    return selector.split(',').map(item => this.querySelector(item.trim())).filter((item): item is Control => item !== null);
  }
}
function control(dialog: Dialog, selector: string) { const item = dialog.querySelector(selector); assert.ok(item, `缺少控件 ${selector}`); return item; }
function select(dialog: Dialog, version: string) { const input = control(dialog, '#core-exact-version'); input.value = version; input.fire('input'); }
async function withPage(fetcher: typeof fetch, run: (page: { core: ReturnType<typeof initCore>; picker: Dialog; confirm: Dialog; seen: (CoreStatus | null)[] }) => Promise<void>) {
  const dialogs: Dialog[] = [], seen: (CoreStatus | null)[] = [];
  const values = { fetch: fetcher, location: { protocol: 'http:' }, window: { dispatchEvent() {} }, document: {
    createElement() { const dialog = new Dialog(); dialogs.push(dialog); return dialog; }, body: { append() {} }, activeElement: null,
  } };
  const previous = Object.fromEntries(Object.keys(values).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(values)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  try { const core = initCore(() => {}, value => { seen.push(value); }); await run({ core, picker: dialogs[0], confirm: dialogs[1], seen }); }
  finally { for (const [key, descriptor] of Object.entries(previous)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); } }
}

test('核心版本读取到外部busy后仍可刷新，完成后恢复选择而不自动安装', async () => {
  let reads = 0, writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') { writes++; return Response.json(status()); }
    if (path === '/api/core/releases') return releases();
    return Response.json(status('26.7.28', ++reads < 3));
  }, async ({ core, picker }) => {
    await settle(); core.openPicker(); await settle();
    assert.equal(control(picker, '#core-picker-submit').disabled, true);
    assert.equal(control(picker, '#core-picker-refresh').disabled, false);
    control(picker, '#core-picker-refresh').fire(); await settle();
    assert.equal(reads, 3); assert.equal(control(picker, '#core-picker-submit').disabled, false); assert.equal(writes, 0);
  });
});

test('核心状态慢读取期间不能确认或安装，完成后保留手填候选再显式安装', async () => {
  const pending = deferred<Response>(); let reads = 0, writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') { writes++; assert.deepEqual(JSON.parse(String(options.body)), { version: '26.3.27' }); return Response.json(status('26.3.27')); }
    if (path === '/api/core/releases') return releases();
    return ++reads === 2 ? pending.promise : Response.json(status());
  }, async ({ core, picker, confirm, seen }) => {
    await settle(); core.openPicker(); await settle(); select(picker, '26.3.27');
    assert.equal(control(picker, '#core-picker-submit').disabled, true);
    assert.equal(control(picker, '#core-picker-previous').disabled, true);
    control(picker, '#core-picker-submit').fire(); await settle(); assert.equal(confirm.open, false); assert.equal(writes, 0);
    pending.resolve(Response.json(status())); await settle();
    assert.equal(control(picker, '#core-exact-version').value, '26.3.27');
    control(picker, '#core-picker-submit').fire(); assert.equal(confirm.open, true);
    control(confirm, '#core-install-confirm').fire(); await settle();
    assert.equal(writes, 1); assert.equal(seen.at(-1)?.selected, '26.3.27');
  });
});

test('核心安装中关闭再打开不启动旧状态查询、不丢进度且不重复安装', async () => {
  const pending = deferred<Response>(); let reads = 0, writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') { writes++; return pending.promise; }
    if (path === '/api/core/releases') return releases();
    reads++; return Response.json(status());
  }, async ({ core, picker, confirm, seen }) => {
    await settle(); core.openPicker(); await settle(); select(picker, '26.3.27');
    control(picker, '#core-picker-submit').fire(); control(confirm, '#core-install-confirm').fire(); await settle();
    assert.equal(writes, 1); control(picker, '#core-picker-cancel').fire(); core.openPicker(); await settle();
    assert.equal(reads, 2); assert.equal(control(picker, '#core-exact-version').value, '26.3.27');
    assert.match(picker.innerHTML, /正在下载官方包/);
    control(picker, '#core-picker-submit').fire(); control(confirm, '#core-install-confirm').fire(); await settle(); assert.equal(writes, 1);
    pending.resolve(Response.json(status('26.3.27'))); await settle();
    assert.equal(seen.at(-1)?.selected, '26.3.27'); assert.match(picker.innerHTML, /未改变当前运行的核心/);
  });
});

test('核心安装结果不确定且服务仍busy时先刷新确认，不能重复提交', async () => {
  let reads = 0, writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') {
      return ++writes === 1 ? Response.json({ error: '未确认结果' }, { status: 500 }) : Response.json(status('26.3.27'));
    }
    if (path === '/api/core/releases') return releases();
    return Response.json(status('26.7.28', ++reads === 3));
  }, async ({ core, picker, confirm, seen }) => {
    await settle(); core.openPicker(); await settle(); select(picker, '26.3.27');
    control(picker, '#core-picker-submit').fire(); control(confirm, '#core-install-confirm').fire(); await settle();
    assert.match(picker.innerHTML, /未确认切换成功/); assert.equal(control(picker, '#core-picker-refresh').disabled, false);
    control(picker, '#core-picker-submit').fire(); control(confirm, '#core-install-confirm').fire(); await settle(); assert.equal(writes, 1);
    control(picker, '#core-picker-refresh').fire(); await settle(); assert.equal(reads, 4);
    control(picker, '#core-picker-submit').fire(); control(confirm, '#core-install-confirm').fire(); await settle();
    assert.equal(writes, 2); assert.equal(seen.at(-1)?.selected, '26.3.27');
  });
});

test('核心状态读取失败后保留手填候选，禁止安装，显式刷新成功后恢复', async () => {
  const pending = deferred<Response>(); let reads = 0, writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') { writes++; return Response.json(status('26.3.27')); }
    if (path === '/api/core/releases') return releases();
    return ++reads === 2 ? pending.promise : Response.json(status());
  }, async ({ core, picker, confirm, seen }) => {
    await settle(); core.openPicker(); select(picker, '26.3.27');
    pending.resolve(Response.json({ error: '读取失败' }, { status: 500 })); await settle();
    assert.equal(seen.at(-1), null); assert.equal(control(picker, '#core-exact-version').value, '26.3.27');
    assert.equal(control(picker, '#core-picker-submit').disabled, true); assert.equal(control(picker, '#core-picker-refresh').disabled, false);
    control(picker, '#core-picker-submit').fire(); await settle(); assert.equal(confirm.open, false); assert.equal(writes, 0);
    control(picker, '#core-picker-refresh').fire(); await settle();
    assert.equal(control(picker, '#core-exact-version').value, '26.3.27'); assert.equal(control(picker, '#core-picker-submit').disabled, false);
    control(picker, '#core-picker-submit').fire(); control(confirm, '#core-install-confirm').fire(); await settle();
    assert.equal(writes, 1); assert.equal(seen.at(-1)?.selected, '26.3.27');
  });
});

test('官方版本列表失败不自动改默认，仍可精确输入，格式错误或取消均不安装', async () => {
  let writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') { writes++; assert.deepEqual(JSON.parse(String(options.body)), { version: '26.3.27' }); return Response.json(status('26.3.27')); }
    return path === '/api/core/releases' ? Response.json({ error: '官方列表暂不可用' }, { status: 503 }) : Response.json(status());
  }, async ({ core, picker, confirm }) => {
    await settle(); core.openPicker(); await settle();
    assert.equal(control(picker, '#core-exact-version').value, '26.7.28'); assert.match(picker.innerHTML, /仍可输入精确版本/); assert.equal(writes, 0);
    select(picker, 'latest'); control(picker, '#core-picker-submit').fire();
    assert.equal(confirm.open, false); assert.match(picker.innerHTML, /版本格式/);
    select(picker, 'v26.3.27'); control(picker, '#core-picker-submit').fire();
    assert.equal(confirm.open, true); confirm.cancel[0].fire(); await settle(); assert.equal(writes, 0);
    control(picker, '#core-picker-submit').fire(); control(confirm, '#core-install-confirm').fire(); await settle(); assert.equal(writes, 1);
  });
});

test('初次慢读取期间重复打开不重复查询，也不能未读状态就安装', async () => {
  const pending = deferred<Response>(); let reads = 0, writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') { writes++; return Response.json(status()); }
    if (path === '/api/core/releases') return releases();
    reads++; return pending.promise;
  }, async ({ core, picker, confirm }) => {
    core.openPicker(); core.openPicker(); await settle();
    assert.equal(reads, 1); assert.equal(control(picker, '#core-picker-submit').disabled, true);
    control(picker, '#core-picker-submit').fire(); await settle(); assert.equal(confirm.open, false); assert.equal(writes, 0);
    pending.resolve(Response.json(status('26.3.27'))); await settle();
    assert.equal(control(picker, '#core-exact-version').value, '26.3.27'); assert.equal(control(picker, '#core-picker-submit').disabled, false);
  });
});

test('精确输入候选时立即说明已验客户端限制，不重绘输入、不自动安装或泛化其它版本', async () => {
  let writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') { writes++; return Response.json(status()); }
    return path === '/api/core/releases' ? releases() : Response.json(status());
  }, async ({ core, picker, confirm }) => {
    await settle(); core.openPicker(); await settle();
    assert.equal(control(picker, '#core-compatibility-warning').hidden, true);
    const input = control(picker, '#core-exact-version'); select(picker, ' v26.10.17 ');
    const warning = control(picker, '#core-compatibility-warning');
    assert.equal(warning.hidden, false); assert.match(warning.textContent, /Mihomo v1\.19\.32/);
    assert.match(warning.textContent, /REALITY.*XHTTP-REALITY/); assert.match(warning.textContent, /兼容模式.*未通过/);
    assert.match(warning.textContent, /本机/); assert.match(warning.textContent, /其他.*未验证/);
    assert.equal(control(picker, '#core-exact-version'), input); assert.equal(input.value, ' v26.10.17 ');
    control(picker, '#core-picker-submit').fire(); assert.equal(confirm.open, true);
    assert.match(confirm.innerHTML, /Mihomo v1\.19\.32/); assert.match(confirm.innerHTML, /26\.10\.17/);
    confirm.close();
    for (const version of ['26.7.28', '26.10.18', '26.10.17-invalid', '26.10.1']) {
      select(picker, version);
      assert.equal(control(picker, '#core-compatibility-warning').hidden, true);
      assert.equal(control(picker, '#core-compatibility-warning').textContent, '');
    }
    assert.equal(writes, 0);
  });
});

test('列表选择和切回确认均按各自版本提示，已安装候选也不跳过限制', async () => {
  let writes = 0;
  await withPage(async (path, options) => {
    if (options?.method === 'POST') { writes++; return Response.json(status()); }
    if (path === '/api/core/releases') return Response.json({ releases: [{ version: '26.10.17', prerelease: true, url: '' }] });
    return Response.json({ ...status('26.10.17'), installed: ['26.7.28', '26.10.17'] });
  }, async ({ core, picker, confirm }) => {
    await settle(); core.openPicker(); await settle();
    assert.equal(control(picker, '#core-compatibility-warning').hidden, false);
    control(picker, '#core-picker-submit').fire(); assert.match(confirm.innerHTML, /Mihomo v1\.19\.32/);
    confirm.close(); control(picker, '#core-picker-previous').fire();
    assert.match(confirm.innerHTML, /确认选择 v26\.7\.28/); assert.doesNotMatch(confirm.innerHTML, /Mihomo/);
    confirm.close();
    const old = control(picker, '#core-picker-26-7-28'); old.fire('change');
    assert.equal(control(picker, '#core-compatibility-warning').hidden, true);
    control(picker, '#core-picker-26-10-17').fire('change');
    assert.equal(control(picker, '#core-compatibility-warning').hidden, false);
    assert.equal(writes, 0);
  });
});
