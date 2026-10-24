import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { registerHooks } from 'node:module';

const hooks = registerHooks({ load(url, context, next) {
  return url.endsWith('.css') ? { format: 'module', source: '', shortCircuit: true } : next(url, context);
} });
const { confirmAction } = await import('../src/confirm.ts');
hooks.deregister();

class Element {
  open = false; isConnected = true; textContent = ''; className = ''; innerHTML = ''; focused = false;
  attributes = new Map<string, string>();
  nodes = new Map<string, Element>();
  handlers = new Map<string, (event: any) => void>();
  setAttribute(key: string, value: string) { this.attributes.set(key, value); }
  querySelector(selector: string) { if (!this.nodes.has(selector)) this.nodes.set(selector, new Element()); return this.nodes.get(selector)!; }
  querySelectorAll() { return []; }
  addEventListener(name: string, handler: (event: any) => void) { this.handlers.set(name, handler); }
  showModal() { this.open = true; }
  close() { this.open = false; this.fire('close'); }
  remove() { this.isConnected = false; }
  focus() { this.focused = true; }
  getClientRects() { return [1]; }
  fire(name: string) { this.handlers.get(name)?.({ preventDefault() {} }); }
}

test('统一确认弹窗：安全文本、默认取消、确认/取消/Escape/会话失效、去重及焦点返回', async () => {
  const origin = new Element(), dialogs: Element[] = [], events = new Map<string, () => void>();
  const values = {
    window: { addEventListener(name: string, fn: () => void) { events.set(name, fn); }, removeEventListener(name: string) { events.delete(name); } },
    document: { createElement() { const node = new Element(); dialogs.push(node); return node; }, activeElement: origin, body: { append() {} } },
  };
  const previous = Object.fromEntries(Object.keys(values).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(values)) Object.defineProperty(globalThis, key, { configurable: true, value });
  try {
    const message = '<img src=x onerror=alert(1)>', pending = confirmAction(message, { title: '重新读取', danger: true });
    let dialog = dialogs.at(-1)!;
    assert.equal(dialog.open, true);
    assert.equal(dialog.querySelector('#confirm-action-message').textContent, message);
    assert.ok(!dialog.innerHTML.includes(message));
    assert.equal(dialog.querySelector('[data-confirm-cancel]').focused, true);
    assert.match(dialog.innerHTML, /danger-button is-solid/);
    assert.equal(await confirmAction('不得重复弹窗'), false); assert.equal(dialogs.length, 1);
    dialog.querySelector('[data-confirm-accept]').fire('click');
    assert.equal(await pending, true); assert.equal(origin.focused, true); assert.equal(dialog.isConnected, false);
    for (const action of ['cancel', 'close', 'escape', 'session', 'external-close']) {
      const result = confirmAction('确认操作'); dialog = dialogs.at(-1)!;
      if (action === 'escape') dialog.fire('cancel');
      else if (action === 'session') events.get('panel-session-expired')!();
      else if (action === 'external-close') dialog.close();
      else dialog.querySelector(action === 'cancel' ? '[data-confirm-cancel]' : '[data-confirm-close]').fire('click');
      assert.equal(await result, false, action);
      assert.equal(events.size, 0); assert.equal(dialog.isConnected, false);
    }
  } finally { for (const [key, descriptor] of Object.entries(previous)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); } }
});

test('业务代码不再调用浏览器原生alert/confirm/prompt', () => {
  for (const file of readdirSync(new URL('../src/', import.meta.url)).filter(file => file.endsWith('.ts'))) {
    assert.doesNotMatch(readFileSync(new URL(`../src/${file}`, import.meta.url), 'utf8'), /\bwindow\.(?:alert|confirm|prompt)\s*\(/, file);
  }
});
