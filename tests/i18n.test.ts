import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { test } from 'node:test';
import { lang, languageButton, locale, t } from '../src/i18n.ts';

const cjk = /[　-〿一-鿿＀-￯]/;
// 后端逐字比对的确认口令，两种界面语言都原样提交。
const tokens = new Set(['确认开放代理监听', '确认启用HTTPS非本机访问', '确认恢复上次应用', '确认重启当前配置', '确认覆盖并恢复']);

// 极简扫描：返回 t(...) 调用之外、字符串或模板文本里的中文片段（跳过注释与正则）。
function untranslated(src: string): string[] {
  const found: string[] = [];
  const marks: number[] = [];
  let paren = 0, i = 0;
  const add = (text: string) => { if (!marks.length && cjk.test(text) && !tokens.has(text.trim())) found.push(text.trim()); };
  const previous = (at: number) => {
    let k = at - 1;
    while (k >= 0 && /\s/.test(src[k])) k--;
    if (k < 0 || /(return|typeof|case|in|of|void)$/.test(src.slice(0, k + 1))) return '';
    return src[k];
  };
  function code(untilBrace: boolean): void {
    let depth = 0;
    while (i < src.length) {
      const c = src[i];
      if (c === '/' && src[i + 1] === '/') { const j = src.indexOf('\n', i); i = j < 0 ? src.length : j; continue; }
      if (c === '/' && src[i + 1] === '*') { const j = src.indexOf('*/', i + 2); i = j < 0 ? src.length : j + 2; continue; }
      if (c === '\'' || c === '"') {
        let j = i + 1;
        while (j < src.length && src[j] !== c) j += src[j] === '\\' ? 2 : 1;
        add(src.slice(i + 1, j)); i = j + 1; continue;
      }
      if (c === '`') { i++; template(); continue; }
      if (c === '/' && '(,=:[!&|?{};+-*%<>~^'.includes(previous(i) || '(')) {
        let j = i + 1, inClass = false;
        while (j < src.length && (src[j] !== '/' || inClass) && src[j] !== '\n') {
          if (src[j] === '\\') { j += 2; continue; }
          if (src[j] === '[') inClass = true; else if (src[j] === ']') inClass = false;
          j++;
        }
        i = j + 1;
        while (/[a-z]/i.test(src[i] ?? '')) i++;
        continue;
      }
      if (c === '(') { paren++; if (src[i - 1] === 't' && !/[\w$.]/.test(src[i - 2] ?? '')) marks.push(paren); }
      else if (c === ')') { if (marks.at(-1) === paren) marks.pop(); paren--; }
      if (untilBrace) {
        if (c === '{') depth++;
        else if (c === '}') { if (depth === 0) { i++; return; } depth--; }
      }
      i++;
    }
  }
  function template(): void {
    let start = i;
    while (i < src.length) {
      const c = src[i];
      if (c === '\\') { i += 2; continue; }
      if (c === '`') { src.slice(start, i).split(/[<>"]/).forEach(add); i++; return; }
      if (c === '$' && src[i + 1] === '{') { src.slice(start, i).split(/[<>"]/).forEach(add); i += 2; code(true); start = i; continue; }
      i++;
    }
  }
  code(false);
  return found;
}

test('界面文案只经t()提供简体中文与英文', () => {
  const files = readdirSync(new URL('../src/', import.meta.url)).filter(name => name.endsWith('.ts') && name !== 'i18n.ts');
  assert.ok(files.length > 30);
  const left = files.flatMap(name => untranslated(readFileSync(new URL(`../src/${name}`, import.meta.url), 'utf8')).map(text => `${name}: ${text}`));
  assert.deepEqual(left, []);
});

test('扫描器能发现t()之外的中文并放过确认口令', () => {
  assert.deepEqual(untranslated("const a = '未翻译'; const b = t('已翻译', 'Done'); const c = `<p>模板${x}</p>`; // 注释中文"), ['未翻译', '模板']);
  assert.deepEqual(untranslated("api('x', { confirmation: '确认覆盖并恢复' }); const r = /[中文]/;"), []);
});

test('测试环境默认简体中文，语言按钮指向另一种语言', () => {
  assert.equal(lang, 'zh');
  assert.equal(locale, 'zh-CN');
  assert.equal(t('中文', 'English'), '中文');
  assert.match(languageButton(), /data-lang="en"[^>]*>EN<\/button>/);
});
