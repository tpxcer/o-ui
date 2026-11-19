import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { defaultSettings } from '../src/xray-settings-model.ts';

const hooks = registerHooks({ load(url, context, next) {
  return url.endsWith('.css') ? { format: 'module', source: '', shortCircuit: true } : next(url, context);
} });
const { initXraySettings } = await import('../src/xray-settings.ts');
hooks.deregister();

// 只模拟本弹窗使用的表单、显隐与事件；真实筛选交互另由隔离浏览器核验。
class Control {
  innerHTML = ''; hidden = false; checked = false; disabled = false; open = false; className = '';
  name = ''; value = ''; type = ''; textContent = ''; parent: Control | null = null;
  controls = new Map<string, Control>(); fields: Control[] = [];
  handlers = new Map<string, (event: any) => void>();
  addEventListener(name: string, handler: (event: any) => void) { this.handlers.set(name, handler); }
  querySelector(selector: string): Control {
    if (!this.controls.has(selector)) {
      const node = new Control();
      if (selector === 'form') {
        for (const match of this.innerHTML.matchAll(/<input\b([^>]+)>|<select\b([^>]+)>([\s\S]*?)<\/select>/g)) {
          const attrs = match[1] ?? match[2];
          const field = new Control();
          field.name = attrs.match(/name="([^"]*)"/)?.[1] ?? '';
          field.type = attrs.match(/type="([^"]*)"/)?.[1] ?? 'text';
          field.checked = /\bchecked\b/.test(attrs); field.parent = new Control();
          field.value = match[1] ? attrs.match(/value="([^"]*)"/)?.[1] ?? '' : match[3].match(/<option value="([^"]*)" selected>/)?.[1] ?? '';
          node.fields.push(field);
        }
      }
      this.controls.set(selector, node);
    }
    return this.controls.get(selector)!;
  }
  querySelectorAll(selector: string): Control[] { return selector === '[name="userTag"]' ? this.fields.filter(item => item.name === 'userTag') : []; }
  closest() { return this.parent; }
  contains() { return false; }
  focus() {}
  showModal() { this.open = true; }
  close() { this.open = false; }
  fire(name: string, event: any = {}) { this.handlers.get(name)?.({ preventDefault() {}, ...event }); }
}
class FormDataFixture {
  form: Control;
  constructor(form: Control) { this.form = form; }
  getAll(name: string) { return this.form.fields.filter(field => field.name === name && !field.disabled && (field.type !== 'checkbox' || field.checked)).map(field => field.value); }
  get(name: string) { return this.getAll(name)[0] ?? null; }
}
const workspace = {
  inbounds: ['a', 'b', 'empty'].map(id => ({ id, name: id })),
  clients: [
    { id: 'a', name: '旧单入站客户端', inboundId: 'a' },
    { id: 'b', name: '另一入站客户端', inboundId: 'b' },
    { id: 'shared', name: '多入站客户端', inboundId: 'a', inboundIds: ['a', 'b'] },
  ],
};
const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(resolve => setImmediate(resolve)); };
async function fixture(rule: Record<string, unknown>, run: (view: {
  dialog: Control; form: Control; user: (id: string) => Control;
  inbound: (tag: string) => void; visible: () => string[]; save: () => Promise<any>;
}) => Promise<void>) {
  const root = new Control(), dialogs: Control[] = [], writes: any[] = [];
  const values = {
    FormData: FormDataFixture, location: { protocol: 'http:' },
    window: { setInterval() {} },
    document: { createElement() { const node = new Control(); dialogs.push(node); return node; }, body: { append() {} }, hidden: false, activeElement: null },
    fetch: async (url: string, options?: RequestInit) => {
      if (options?.method === 'PUT') { writes.push(JSON.parse(String(options.body))); return Response.json({ revision: 2, applied: true }); }
      return Response.json(url.endsWith('/workspace') ? { data: workspace } : { revision: 1, data: { ...defaultSettings(), rules: JSON.stringify([rule]) } });
    },
  };
  const previous = Object.fromEntries(Object.keys(values).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(values)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  try {
    initXraySettings(root as unknown as HTMLElement, () => {}, true).render(); await settle();
    const edit = { dataset: { op: 'rule:edit', index: '0' }, closest(selector: string) { return selector === 'button' ? this : null; } };
    root.fire('click', { target: edit }); await settle();
    const dialog = dialogs[0], form = dialog.querySelector('form');
    await run({
      dialog, form,
      user: id => form.fields.find(field => field.name === 'userTag' && field.value === `client-${id}`)!,
      inbound: tag => { form.fields.find(field => field.name === 'inboundTag')!.value = tag; form.fire('change'); },
      visible: () => form.querySelectorAll('[name="userTag"]').filter(field => !field.parent!.hidden).map(field => field.value),
      save: async () => {
        form.fire('submit');
        const button = { id: 'xray-save', dataset: {}, closest(selector: string) { return selector === 'button' ? this : null; } };
        root.fire('click', { target: button }); await settle();
        return writes.at(-1);
      },
    });
  } finally {
    for (const [key, descriptor] of Object.entries(previous)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); }
  }
}
const rule = (inbound = 'in-a', users = ['client-a']) => ({ type: 'field', inboundTag: [inbound], user: users, domain: ['example.test'], outboundTag: 'direct' });

test('路由用户随入站筛选，兼容旧单入站、多入站、空入站和不限制入站', async () => {
  await fixture(rule(), async view => {
    assert.deepEqual(view.visible(), ['client-a', 'client-shared']);
    view.inbound('in-b'); assert.deepEqual(view.visible(), ['client-b', 'client-shared']);
    assert.equal(view.user('a').checked, true, '保留被隐藏的选择');
    assert.equal(view.user('a').disabled, true);
    view.inbound('in-empty'); assert.deepEqual(view.visible(), []);
    assert.equal(view.form.querySelector('[data-rule-no-users]').hidden, false);
    view.inbound(''); assert.deepEqual(view.visible(), ['client-a', 'client-b', 'client-shared']);
    assert.equal(view.user('a').checked, true);
    assert.equal(view.form.querySelector('[data-rule-user-mismatch]').hidden, true);
  });
});

test('切换入站不静默丢掉用户限制；错配不能保存，切回恢复原选择', async () => {
  await fixture(rule(), async view => {
    view.inbound('in-b');
    assert.equal(await view.save(), undefined); assert.equal(view.dialog.open, true);
    assert.match(view.form.querySelector('.form-error').textContent, /所选用户与入站不匹配/);
    view.inbound('in-a');
    const saved = await view.save(); assert.equal(view.dialog.open, false);
    assert.deepEqual(JSON.parse(saved.data.rules), [rule()]);
  });
});

test('显式清除只移除不匹配选择，保留跨入站用户及其它条件', async () => {
  await fixture(rule('in-a', ['client-a', 'client-shared']), async view => {
    view.inbound('in-b'); view.form.querySelector('[data-clear-rule-users]').fire('click');
    assert.equal(view.user('a').checked, false); assert.equal(view.user('shared').checked, true);
    assert.equal(view.form.querySelector('[data-rule-user-mismatch]').hidden, true);
    const saved = await view.save();
    assert.deepEqual(JSON.parse(saved.data.rules), [rule('in-b', ['client-shared'])]);
  });
});

test('旧错配与未知用户不能在编辑时被静默删掉；无入站时保留自定义用户', async () => {
  for (const id of ['b', 'unknown']) await fixture(rule('in-a', [`client-${id}`]), async view => {
    assert.deepEqual(view.visible(), ['client-a', 'client-shared']);
    assert.equal(view.user(id).checked, true); assert.equal(await view.save(), undefined);
    view.inbound(''); const saved = await view.save();
    const expected: Record<string, unknown> = { ...rule('in-a', [`client-${id}`]) }; delete expected.inboundTag;
    assert.deepEqual(JSON.parse(saved.data.rules), [expected]);
  });
});

test('没有用户限制的入站规则保持不限制；未知旧入站条件不会在编辑时丢失', async () => {
  await fixture({ type: 'field', inboundTag: ['in-empty'], outboundTag: 'direct' }, async view => {
    assert.deepEqual(view.visible(), []);
    assert.deepEqual(JSON.parse((await view.save()).data.rules), [{ type: 'field', inboundTag: ['in-empty'], outboundTag: 'direct' }]);
    assert.equal(view.dialog.open, false);
  });
  await fixture(rule('in-missing'), async view => {
    assert.equal(view.form.fields.find(field => field.name === 'inboundTag')!.value, 'in-missing');
    assert.deepEqual(view.visible(), []); assert.equal(await view.save(), undefined);
  });
});
