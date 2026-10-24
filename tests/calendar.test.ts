import test from 'node:test';
import assert from 'node:assert/strict';
import { initCalendar } from '../src/calendar.ts';

test('永不过期日历定位今天，关闭/重新打开复位，今日标记不改变到期值', () => {
  const handlers = new Map<string, (event: any) => void>();
  const focused: string[] = [];
  const root = {
    innerHTML: '', addEventListener(name: string, fn: (event: any) => void) { handlers.set(name, fn); },
    querySelector(selector: string) { return { focus() { focused.push(selector); } }; },
  };
  const calendar = initCalendar(root as unknown as HTMLElement, null, () => {});
  function click(data: { date?: string; day?: string }, trigger = false) {
    const button = { dataset: data, classList: { contains: () => trigger } };
    handlers.get('click')!({ target: { closest: () => button } });
  }
  const today = new Date();
  assert.match(root.innerHTML, new RegExp(`data-day="${today.getDate()}" aria-current="date"`));
  assert.equal(calendar.value(), null);
  click({}, true); assert.equal(focused.at(-1), '[aria-current="date"]');
  click({ date: 'next' }); click({}, true); click({}, true);
  assert.match(root.innerHTML, new RegExp(`data-day="${today.getDate()}" aria-current="date"`));
  assert.equal(calendar.value(), null);
  click({ day: String(today.getDate()) });
  assert.equal(new Date(calendar.value()!).getDate(), today.getDate());
  click({ date: 'clear' }); assert.equal(calendar.value(), null);
});

test('编辑已有到期日期仍定位选中月份，今日标记和到期选中相互独立', () => {
  const initial = new Date(2030, 4, 12, 17, 20).getTime();
  const root = { innerHTML: '', addEventListener() {} };
  const calendar = initCalendar(root as unknown as HTMLElement, initial, () => {});
  assert.equal(calendar.value(), initial);
  assert.match(root.innerHTML, /data-day="12" aria-label="[^"]+" aria-pressed="true"/);
  assert.doesNotMatch(root.innerHTML, /aria-current="date"/);
});
