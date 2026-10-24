import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { seedWorkspace } from '../src/workspace.ts';

const hooks = registerHooks({ load(url, context, next) {
  return url.endsWith('.css') ? { format: 'module', source: '', shortCircuit: true } : next(url, context);
} });
const { initManagement } = await import('../src/management.ts');
hooks.deregister();

const settle = async () => { for (let i = 0; i < 5; i++) await new Promise(resolve => setImmediate(resolve)); };
function workspaceFixture() {
  const state = seedWorkspace();
  for (const inbound of state.inbounds) inbound.port = Math.max(1024, inbound.port);
  for (const client of state.clients) { client.ipLimit = 0; client.usedGB = 0; }
  return state;
}
class Control {
  value = ''; type = ''; name = ''; checked = false; disabled = false; hidden = false; textContent = '';
  isConnected = true; open = false; id = ''; className = ''; dataset: Record<string, string> = {};
  elements: Control[] = [];
  controls = new Map<string, Control>();
  groups = new Map<string, Control[]>();
  handlers = new Map<string, (event: any) => void>();
  html = '';
  get innerHTML() { return this.html; }
  set innerHTML(value: string) { this.html = value; this.controls.clear(); }
  addEventListener(name: string, handler: (event: any) => void) { this.handlers.set(name, handler); }
  setAttribute() {}
  removeAttribute() {}
  append(...items: Control[]) { this.elements.push(...items); }
  focus() {}
  getClientRects() { return [1]; }
  onConfirm?: () => boolean;
  showModal() { this.open = true; if (this.className === 'confirm-dialog') queueMicrotask(() => this.querySelector(this.onConfirm?.() ? '[data-confirm-accept]' : '[data-confirm-cancel]').fire()); }
  close() { this.open = false; this.handlers.get('close')?.({}); }
  remove() {}
  contains() { return false; }
  closest() { return this; }
  hasAttribute(name: string) { return name in this.dataset; }
  querySelector(selector: string): Control {
    if (!this.controls.has(selector)) {
      const item = new Control();
      if (selector === '#xray-settings-form' && this.html.includes('name="probeURL"')) item.controls.set('[name]', this.querySelector('[name="probeURL"]'));
      if (selector === '[name="probeURL"]') { item.name = 'probeURL'; item.value = this.html.match(/name="probeURL"[^>]*value="([^"]*)"/)?.[1] ?? ''; }
      this.controls.set(selector, item);
    }
    return this.controls.get(selector)!;
  }
  querySelectorAll(selector: string): Control[] {
    if (this.groups.has(selector)) return this.groups.get(selector)!;
    if (selector === '[name]' && this.controls.has('[name]')) return [this.controls.get('[name]')!];
    return [];
  }
  fire(name = 'click', event: any = {}) { this.handlers.get(name)?.({ preventDefault() {}, ...event }); }
}
async function withPage(fetcher: typeof fetch, run: (root: Control, dialogs: Control[], confirmation: { answer: boolean; count: number }, timers: { delay: number; callback: () => void }[]) => Promise<void>, firewallFetcher?: typeof fetch) {
  const root = new Control(), dialogs: Control[] = [], confirmation = { answer: true, count: 0 };
  const timers: { delay: number; callback: () => void }[] = [];
  root.hidden = true;
  const values = {
    fetch: ((url, options) => String(url).endsWith('/api/runtime/firewall') ? firewallFetcher ? firewallFetcher(url, options) : Promise.resolve(Response.json({ data: null })) : fetcher(url, options)) as typeof fetch, location: { protocol: 'http:' }, HTMLInputElement: Control, HTMLButtonElement: Control, setTimeout() { return 0; }, clearTimeout() {},
    window: { setInterval(callback: () => void, delay: number) { timers.push({ delay, callback }); }, addEventListener() {}, removeEventListener() {} },
    document: { createElement: (tag: string) => { const item = new Control(); item.onConfirm = () => { confirmation.count++; return confirmation.answer; }; if (tag === 'dialog') dialogs.push(item); return item; }, body: new Control(), activeElement: null, hidden: false, addEventListener() {}, querySelector() { return null; } },
  };
  const previous = Object.fromEntries(Object.keys(values).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(values)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  try { await run(root, dialogs, confirmation, timers); }
  finally { for (const [key, descriptor] of Object.entries(previous)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); } }
}

function clickWorkspace(root: Control, attribute: string, operation?: string, id?: string) {
  const button = new Control(); button.dataset[attribute] = ''; if (operation) button.dataset.op = operation; if (id) button.dataset.id = id;
  root.fire('click', { target: button });
}

test('旧端口告警保留详情：不自动撤销，取消不请求，失败保留，单项成功后才撤下', async () => {
  const state = workspaceFixture();
  let cleanups = 0;
  const report = { workspaceRevision: 1, endpoints: {
    '51514/tcp': { state: 'failed', message: '<b>旧失败原因</b>', checkedAt: '2026-10-24T01:00:00Z', operation: 'delete' },
    '60890/udp': { state: 'failed', message: 'Port hopping update failed: legacy', checkedAt: '', operation: 'redirect-delete' },
  } as Record<string, any> };
  await withPage(async (url, options) => {
    if (String(url).endsWith('/api/workspace')) return Response.json({ revision: 1, data: state });
    if (String(url).endsWith('/api/runtime/firewall/cleanup')) {
      cleanups++;
      assert.deepEqual(JSON.parse(String(options?.body)), { endpoint: '51514/tcp' });
      if (cleanups === 1) return Response.json({ error: '仍然失败' }, { status: 500 });
      delete report.endpoints['51514/tcp'];
      return Response.json(report);
    }
    return Response.json({ totals: {}, clients: [], data: { enabled: false } });
  }, async (root, _dialogs, confirmation) => {
    initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true).render('clients'); await settle();
    const maintenance = root.querySelector('#firewall-maintenance');
    assert.equal(maintenance.hidden, false);
    assert.equal(root.querySelector('#firewall-read-error').hidden, true);
    assert.match(maintenance.innerHTML, /<details><summary>旧端口防火墙待处理/);
    assert.match(maintenance.innerHTML, /&lt;b&gt;旧失败原因&lt;\/b&gt;/);
    assert.match(maintenance.innerHTML, /data-firewall-cleanup="51514\/tcp"/);
    assert.doesNotMatch(maintenance.innerHTML, /data-firewall-cleanup="60890\/udp"/);
    assert.equal(cleanups, 0);
    const retry = () => { const button = new Control(); button.dataset.firewallCleanup = '51514/tcp'; root.fire('click', { target: button }); };
    confirmation.answer = false; retry(); await settle(); assert.equal(cleanups, 0);
    confirmation.answer = true; retry(); await settle(); assert.equal(cleanups, 1);
    assert.match(maintenance.innerHTML, /51514\/tcp/);
    retry(); await settle(); assert.equal(cleanups, 2);
    assert.doesNotMatch(maintenance.innerHTML, /51514\/tcp/);
    assert.match(maintenance.innerHTML, /60890\/udp/);
  }, async () => Response.json({ data: report }));
});

test('四预设一键创建直接保存并显示自动防火墙结果，无需另点重试', async t => {
  for (const preset of ['reality', 'xhttp-reality', 'xhttp-tls', 'hy2'] as const) {
    for (const applied of [true, false]) await t.test(`${preset} applied=${applied}`, async () => {
      const state = workspaceFixture(), requests: string[] = [];
      let writes = 0;
      await withPage(async (url, options) => {
        const path = String(url); requests.push(path);
        if (path.endsWith('/api/workspace')) {
          if (options?.method === 'PUT') {
            writes++;
            const sent = JSON.parse(String(options.body));
            assert.equal(sent.revision, 1);
            const inbound = sent.data.inbounds.at(-1);
            assert.equal(inbound.preset, preset);
            assert.equal(inbound.port, 58443);
            assert.equal(inbound.listen, '0.0.0.0');
            assert.equal(inbound.portHopping, preset === 'hy2' ? '48000-48199' : undefined);
            if (preset === 'hy2' || preset === 'xhttp-tls') assert.equal(inbound.connection.usePanelCertificate, true);
            return Response.json({ revision: 2, data: sent.data, applied, applyError: applied ? '' : '测试预检失败', firewall: '测试自动放行结果' });
          }
          return Response.json({ revision: 1, data: state });
        }
        if (path.includes('/api/system/free-port?')) return Response.json({ port: 58443, ...(preset === 'hy2' ? { portHopping: '48000-48199' } : {}) });
        if (path.endsWith('/api/panel/tls')) return Response.json({ active: true, serverNames: ['panel.example.com'] });
        if (path.endsWith('/api/system/public')) return Response.json({ ipv4: { ip: '203.0.113.10' } });
        return Response.json({ totals: {}, clients: [], data: { enabled: false } });
      }, async root => {
        initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true).render('inbounds');
        await settle();
        const button = new Control(); button.dataset.quickPreset = preset;
        root.fire('click', { target: button }); await settle();
        assert.equal(writes, 1);
        assert.ok(requests.some(path => path.endsWith(`/api/system/free-port?transport=${preset === 'hy2' ? 'udp&hop=1' : 'tcp'}`)));
        assert.ok(!requests.some(path => path.includes('/firewall/retry')));
        const notice = (document.body as unknown as Control).elements.find(item => item.className === 'save-notice')!;
        assert.match(notice.textContent, /防火墙：测试自动放行结果/);
      });
    });
  }
});

test('客户端启用与最近上线分列，速率持续显示且未知采样不假报零', async () => {
  const state = workspaceFixture();
  state.clients.forEach(client => { client.enabled = true; client.expiresAt = null; client.quotaGB = 0; });
  state.inbounds.forEach(inbound => { inbound.enabled = true; });
  const id = state.clients[0].id;
  let samples = 0, mode = 'online';
  const start = Date.now();
  await withPage(async url => {
    const path = String(url);
    if (path.endsWith('/api/workspace')) return Response.json({ revision: 1, data: state });
    if (path.endsWith('/api/runtime/traffic')) {
      samples++;
      const sampledAt = new Date(start + samples * 2000).toISOString();
      return Response.json({ totals: { [id]: { uplink: String(samples * 2000), downlink: String(samples * 4000) } }, lastOnline: { [id]: new Date(mode === 'online' ? start : start - 33 * 60000).toISOString() }, sampledAt, collecting: mode !== 'stopped', intervalSeconds: 2, issue: '', incomplete: false });
    }
    if (path.endsWith('/api/runtime/iplimit')) return Response.json({ clients: {} });
    if (path.endsWith('/api/panel/subscription')) return Response.json({ data: { enabled: false } });
    return Response.json({ clients: [], botEnabled: false });
  }, async (root, _dialogs, _confirmation, timers) => {
    root.hidden = false;
    initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true).render('clients');
    await settle();
    let html = root.querySelector('#list-content').innerHTML;
    assert.doesNotMatch(html, /<th scope="col">启用<\/th>/);
    assert.match(html, /class="client-identity"><label class="switch"[\s\S]*?data-toggle-client=[\s\S]*?class="client-identity-text"><strong class="record-name">/);
    assert.match(html, /class="speed-up"[^>]*>↑ —/);
    assert.match(html, /class="speed-down"[^>]*>↓ —/);
    assert.doesNotMatch(html.match(/<td data-label="启用">[\s\S]*?<\/td>/)?.[0] ?? '', /is-online/);
    assert.match(html, /data-client-last><span class="status-badge is-online"/);
    const poll = timers.find(timer => timer.delay === 5000)!;
    poll.callback(); await settle();
    html = root.querySelector('#list-content').innerHTML;
    assert.match(html, /class="speed-up"[^>]*>↑ 1 KB\/s/);
    assert.match(html, /class="speed-down"[^>]*>↓ 2 KB\/s/);
    mode = 'offline'; poll.callback(); await settle();
    html = root.querySelector('#list-content').innerHTML;
    assert.match(html, /33 分钟前上线过/);
    assert.doesNotMatch(html, /is-online|>离线</);
    mode = 'stopped'; poll.callback(); await settle();
    html = root.querySelector('#list-content').innerHTML;
    assert.match(html, /class="speed-up"[^>]*>↑ —/);
  });
});
test('入站客户端显式重读可取消，失败保留旧列表，再成功后使用新修订', async () => {
  const initial = workspaceFixture(), next = structuredClone(initial); next.clients[0].name = 'latest-client';
  let reads = 0; const writes: any[] = [];
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') { const value = JSON.parse(String(options.body)); writes.push(value); return Response.json({ revision: 10, data: value.data }); }
    reads++; if (reads === 2) throw new Error('offline');
    return Response.json({ revision: reads === 1 ? 1 : 9, data: reads === 1 ? initial : next });
  }, async (root, dialogs, confirmation) => {
    initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true).render('clients'); await settle();
    assert.match(root.innerHTML, /data-reload/);
    confirmation.answer = false; clickWorkspace(root, 'data-reload'); await settle(); assert.equal(reads, 1);
    confirmation.answer = true; clickWorkspace(root, 'data-reload'); await settle();
    assert.equal(reads, 2); assert.doesNotMatch(root.querySelector('#list-content').innerHTML, /latest-client/);
    clickWorkspace(root, 'data-reload'); await settle();
    assert.match(root.querySelector('#list-content').innerHTML, /latest-client/);
    clickWorkspace(root, 'data-op', 'toggle-client', next.clients[0].id);
    dialogs.find(item => item.id === 'edit-dialog')!.querySelector('form').fire('submit'); await settle();
    assert.equal(writes[0].revision, 9);
  });
});

test('客户端保存冲突后不重复写，确认重读失败时保留弹窗，成功后方可保存新修订', async () => {
  const initial = workspaceFixture(), latest = structuredClone(initial); latest.clients[0].name = 'newer-client';
  let reads = 0, writes = 0;
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') {
      const value = JSON.parse(String(options.body));
      if (++writes === 1) return Response.json({ error: '修订冲突' }, { status: 409 });
      assert.equal(value.revision, 9); assert.equal(value.data.clients[0].name, 'newer-client');
      return Response.json({ revision: 10, data: value.data });
    }
    // 第2次读取是冲突后的确认读取（服务器上是别处改过的修订9，与本次提交不同）；第3次重读失败。
    if (++reads === 3) throw new Error('offline');
    return Response.json({ revision: reads === 1 ? 1 : 9, data: reads === 1 ? initial : latest });
  }, async (root, dialogs, confirmation) => {
    initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true).render('clients'); await settle();
    clickWorkspace(root, 'data-op', 'toggle-client', initial.clients[0].id);
    const dialog = dialogs.at(-1)!, form = dialog.querySelector('form');
    form.fire('submit'); await settle();
    assert.equal(reads, 2, '冲突后先向服务器确认是否其实已保存');
    assert.match(dialog.querySelector('.form-error').textContent, /输入已保留/);
    assert.equal(form.querySelector('[type="submit"]').disabled, true);
    form.fire('submit'); await settle(); assert.equal(writes, 1);
    confirmation.answer = false; dialog.querySelector('[data-reload]').fire(); await settle(); assert.equal(reads, 2); assert.equal(dialog.open, true);
    confirmation.answer = true; dialog.querySelector('[data-reload]').fire(); await settle();
    assert.equal(dialog.open, true); assert.match(dialog.querySelector('.form-error').textContent, /原输入仍保留/);
    assert.equal(form.querySelector('[type="submit"]').disabled, true);
    dialog.querySelector('[data-reload]').fire(); await settle();
    assert.equal(dialog.open, false); assert.match(root.querySelector('#list-content').innerHTML, /newer-client/);
    clickWorkspace(root, 'data-op', 'toggle-client', initial.clients[0].id);
    dialog.querySelector('form').fire('submit'); await settle(); assert.equal(writes, 2);
  });
});

test('入站重读在途阻止重复读取、修改和应用，失败保留弹窗内输入', async () => {
  let complete!: (value: Response) => void;
  const pending = new Promise<Response>(resolve => { complete = resolve; });
  let reads = 0, writes = 0, applied = 0;
  const initial = workspaceFixture();
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') { writes++; return Response.json({ revision: 2 }); }
    return ++reads === 1 ? Response.json({ revision: 1, data: initial }) : pending;
  }, async (root, dialogs) => {
    initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true, () => { applied++; }).render('inbounds'); await settle();
    clickWorkspace(root, 'data-op', 'toggle-inbound', initial.inbounds[0].id);
    const dialog = dialogs.at(-1)!, form = dialog.querySelector('form');
    const input = new Control(); input.value = '本地未提交内容'; form.controls.set('[name="local"]', input);
    dialog.querySelector('[data-reload]').fire(); await settle();
    dialog.querySelector('[data-reload]').fire(); form.fire('submit'); clickWorkspace(root, 'data-apply'); await settle();
    assert.equal(reads, 2); assert.equal(writes, 0); assert.equal(applied, 0);
    complete(Response.json({ error: '读取失败' }, { status: 500 })); await settle();
    assert.equal(dialog.open, true); assert.equal(form.querySelector('[name="local"]').value, '本地未提交内容');
    assert.match(dialog.querySelector('.form-error').textContent, /原输入仍保留/);
  });
});

test('入站客户端列表应显示应用状态入口，而非仅指向首页', async () => {
  await withPage(async () => Response.json({ revision: 3, data: workspaceFixture() }), async root => {
    const management = initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true);
    management.render('inbounds'); await settle();
    assert.match(root.innerHTML, /id="workspace-application-status"[^>]*role="status"/);
    assert.match(root.querySelector('#list-content').innerHTML, /已启用/);
    assert.doesNotMatch(root.querySelector('#list-content').innerHTML, /待应用/);
    management.render('clients');
    assert.match(root.innerHTML, /id="workspace-application-status"[^>]*role="status"/);
    assert.doesNotMatch(root.innerHTML, /生效状态见首页/);
  });
});

test('保存入站客户端后核对生效状态，状态更新或切页不重绘表单、不追加请求', async () => {
  const initial = workspaceFixture(); let reads = 0, writes = 0;
  let complete!: (value: Response) => void;
  const pending = new Promise<Response>(resolve => { complete = resolve; });
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') { writes++; return pending; }
    reads++; return Response.json({ revision: 3, data: initial });
  }, async (root, dialogs) => {
    const management = initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true);
    management.render('clients'); await settle();
    assert.match(root.querySelector('#workspace-application-status').textContent, /未连接.*无法确认/);
    const current = { running: true, version: '26.7.28', workspaceRevision: 3, settingsRevision: 1, inbounds: 1 };
    management.setRuntime(current, Date.now());
    assert.match(root.querySelector('#workspace-application-status').textContent, /修订3当前已应用/);
    clickWorkspace(root, 'data-op', 'toggle-client', initial.clients[0].id);
    const dialog = dialogs.at(-1)!, form = dialog.querySelector('form');
    const input = new Control(); input.value = '尚未提交的本地输入'; form.controls.set('[name="local"]', input);
    const pageHTML = root.innerHTML, modalHTML = dialog.innerHTML;
    management.setRuntime({ ...current, running: false }, Date.now());
    assert.match(root.querySelector('#workspace-application-status').textContent, /核心已停止/);
    assert.equal(root.innerHTML, pageHTML); assert.equal(dialog.innerHTML, modalHTML); assert.equal(form.querySelector('[name="local"]'), input);
    management.setRuntime(current, Date.now());
    form.fire('submit'); await settle();
    assert.match(root.querySelector('#workspace-application-status').textContent, /正在保存并应用/);
    const next = structuredClone(initial); next.clients[0].enabled = false;
    complete(Response.json({ revision: 4, data: next })); await settle();
    assert.equal(dialog.open, false); assert.equal(writes, 1);
    assert.match(root.querySelector('#workspace-application-status').textContent, /修订4待应用.*修订3/);
    management.setRuntime({ ...current, workspaceRevision: 4 }, Date.now());
    assert.match(root.querySelector('#workspace-application-status').textContent, /修订4当前已应用/);
    management.render('inbounds');
    assert.match(root.querySelector('#workspace-application-status').textContent, /修订4当前已应用/);
    assert.equal(reads, 1, '状态更新和已加载的切页不重读或保存草稿');
  });
});

test('状态定时撤下过期结论、失联撤值；隐藏页不轮询且不修改表单', async t => {
  let clock = 1800000000000; t.mock.method(Date, 'now', () => clock);
  let reads = 0;
  await withPage(async () => { reads++; return Response.json({ revision: 3, data: workspaceFixture() }); }, async (root, _, __, timers) => {
    const management = initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true);
    management.render('inbounds'); await settle(); root.hidden = false;
    const current = { running: true, workspaceRevision: 3, settingsRevision: 1, inbounds: 1 };
    management.setRuntime(current, clock);
    const html = root.innerHTML, status = root.querySelector('#workspace-application-status');
    clock += 10001;
    timers.find(timer => timer.delay === 1000)!.callback();
    assert.match(status.textContent, /状态已过期.*无法确认/); assert.equal(root.innerHTML, html); assert.equal(reads, 1);
    management.setRuntime(null, clock); assert.match(status.textContent, /未连接.*无法确认/);
    management.setRuntime(current, clock); assert.match(status.textContent, /当前已应用/);
    root.hidden = true; clock += 20000;
    timers.find(timer => timer.delay === 1000)!.callback(); assert.match(status.textContent, /当前已应用/);
    root.hidden = false; timers.find(timer => timer.delay === 1000)!.callback(); assert.match(status.textContent, /已过期/);
    assert.equal(reads, 1);
  });
});

test('保存结果不确定时不再肯定旧修订已应用，确认重读后才能重新核对', async () => {
  const initial = workspaceFixture(); let reads = 0;
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') throw new Error('response lost');
    return Response.json({ revision: ++reads === 1 ? 3 : 4, data: initial });
  }, async (root, dialogs) => {
    const management = initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true);
    management.render('clients'); await settle();
    const current = { running: true, workspaceRevision: 3, settingsRevision: 1, inbounds: 1 };
    management.setRuntime(current, Date.now());
    clickWorkspace(root, 'data-op', 'toggle-client', initial.clients[0].id);
    const dialog = dialogs.at(-1)!;
    dialog.querySelector('form').fire('submit'); await settle();
    assert.match(root.querySelector('#workspace-application-status').textContent, /结果未确认.*重新读取/);
    management.setRuntime(current, Date.now());
    assert.doesNotMatch(root.querySelector('#workspace-application-status').textContent, /当前已应用/);
    dialog.querySelector('[data-reload]').fire(); await settle();
    assert.match(root.querySelector('#workspace-application-status').textContent, /修订4待应用/);
    management.setRuntime({ ...current, workspaceRevision: 5 }, Date.now());
    assert.match(root.querySelector('#workspace-application-status').textContent, /页面.*已落后.*重新读取/);
  });
});

test('草稿校验失败仍可修正，重读失败则撤下旧修订已应用结论', async () => {
  const initial = workspaceFixture(); let reads = 0, writes = 0;
  await withPage(async (_, options) => {
    if (options?.method === 'PUT') { writes++; return Response.json({ error: '草稿校验失败' }, { status: 400 }); }
    if (++reads > 1) throw new Error('offline');
    return Response.json({ revision: 3, data: initial });
  }, async (root, dialogs) => {
    const management = initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true);
    const current = { running: true, workspaceRevision: 3, settingsRevision: 1, inbounds: 1 };
    management.setRuntime(current, Date.now());
    management.render('clients'); await settle();
    clickWorkspace(root, 'data-op', 'toggle-client', initial.clients[0].id);
    const dialog = dialogs.at(-1)!, form = dialog.querySelector('form');
    form.fire('submit'); await settle();
    assert.match(root.querySelector('#workspace-application-status').textContent, /修订3当前已应用/);
    assert.equal(form.querySelector('[type="submit"]').disabled, false);
    form.fire('submit'); await settle(); assert.equal(writes, 2);
    dialog.querySelector('[data-reload]').fire(); await settle();
    management.setRuntime(current, Date.now());
    assert.match(root.querySelector('#workspace-application-status').textContent, /重新读取.*暂不判断/);
    assert.doesNotMatch(root.querySelector('#workspace-application-status').textContent, /当前已应用/);
    assert.equal(dialog.querySelector('[type="submit"]').disabled, true);
    assert.equal(dialog.open, true);
  });
});

test('入站客户端慢保存锁定所有编辑控件，失败恢复原禁用状态且不丢输入', async t => {
  for (const outcome of ['success', 'validation', 'uncertain']) await t.test(outcome, async () => {
    const initial = workspaceFixture(); let writes = 0, reads = 0;
    let finish!: (value: Response) => void, fail!: (reason: Error) => void;
    const pending = new Promise<Response>((resolve, reject) => { finish = resolve; fail = reject; });
    await withPage(async (_, options) => {
      if (options?.method === 'PUT') { writes++; return pending; }
      reads++; return Response.json({ revision: 3, data: initial });
    }, async (root, dialogs) => {
      initManagement(root as unknown as HTMLElement, () => {}, () => {}, () => {}, true).render('clients'); await settle();
      clickWorkspace(root, 'data-op', 'toggle-client', initial.clients[0].id);
      const dialog = dialogs.at(-1)!, form = dialog.querySelector('form');
      const name = new Control(), disabledInput = new Control(), select = new Control(), textarea = new Control();
      const save = form.querySelector('[type="submit"]'), cancel = new Control(), disabledButton = new Control();
      name.value = '原输入'; textarea.value = '原排除规则'; disabledInput.disabled = disabledButton.disabled = true;
      const controls = [name, disabledInput, select, textarea, save, cancel, disabledButton];
      const disabled = controls.map(control => control.disabled);
      form.groups.set('input,button,select,textarea', controls);
      form.groups.set('button', [save, cancel, disabledButton]);
      dialog.controls.set('[type="submit"]', save);
      form.fire('submit'); await settle();
      try {
        assert.ok(controls.every(control => control.disabled), '提交期间输入、选择、文本域和按钮全部锁定');
        assert.equal(save.textContent, '正在保存并生效…');
        assert.equal(dialog.open, true); assert.equal(name.value, '原输入'); assert.equal(textarea.value, '原排除规则');
        let prevented = false; dialog.fire('cancel', { preventDefault() { prevented = true; } }); assert.equal(prevented, true);
        form.fire('submit'); dialog.querySelector('[data-reload]').fire(); await settle();
        assert.equal(writes, 1); assert.equal(reads, 1);
      } finally {
        if (outcome === 'uncertain') fail(new Error('response lost'));
        else finish(outcome === 'validation' ? Response.json({ error: '输入校验失败' }, { status: 400 }) : Response.json({ revision: 4, data: initial }));
        await settle();
      }
      if (outcome === 'success') assert.equal(dialog.open, false);
      else {
        assert.equal(dialog.open, true); assert.equal(name.value, '原输入'); assert.equal(textarea.value, '原排除规则');
        controls.forEach((control, index) => assert.equal(control.disabled, control === save && outcome === 'uncertain' || disabled[index]));
        assert.equal(save.textContent, initial.clients[0].enabled ? '确认停用' : '确认启用');
        assert.match(dialog.querySelector('.form-error').textContent, outcome === 'validation' ? /输入校验失败/ : /输入已保留.*重新读取/);
      }
    });
  });
});
