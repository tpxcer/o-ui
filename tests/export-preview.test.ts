import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, copyFile, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';

test('离线预览原样内嵌含替换符的脚本，并嵌入标志与浏览器图标', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'o-ui-preview-'));
  try {
    await mkdir(join(directory, 'scripts')); await mkdir(join(directory, 'dist', 'assets'), { recursive: true });
    await copyFile(new URL('../scripts/export-preview.mjs', import.meta.url), join(directory, 'scripts', 'export-preview.mjs'));
    const value = "$` / $' / $& / $$";
    const script = `return ${JSON.stringify(value)};`;
    const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jN1sAAAAASUVORK5CYII=', 'base64');
    await writeFile(join(directory, 'dist', 'assets', 'logo.png'), png);
    await writeFile(join(directory, 'dist', 'assets', 'main.js'), script);
    await writeFile(join(directory, 'dist', 'assets', 'main.css'), '.brand{background:url(./logo.png)}');
    await writeFile(join(directory, 'dist', 'index.html'), '<link rel="icon" href="./assets/logo.png"><link rel="stylesheet" href="./assets/main.css"><script type="module" src="./assets/main.js"></script>');
    execFileSync(process.execPath, [join(directory, 'scripts', 'export-preview.mjs')]);
    const html = await readFile(join(directory, 'dist', '首页预览.html'), 'utf8');
    const code = html.match(/<script type="module">([\s\S]*?)<\/script>/)![1];
    assert.equal(new Function(code)(), value);
    assert.doesNotMatch(html, /\.\/assets\//);
    assert.equal((html.match(/data:image\/png;base64,/g) ?? []).length, 2);
  } finally { await rm(directory, { recursive: true, force: true }); }
});
