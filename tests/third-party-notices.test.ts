import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile, readdir, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import * as localBuild from '../scripts/build-local.mjs';

const upstream = 'test-only upstream copyright\nPermission notice.\n';
const info = { GoVersion: 'go1.26.9', Deps: [{ Path: 'golang.org/x/crypto', Version: 'v0.58.0' }] };

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'ixp-licenses-'));
  const source = join(root, 'source'), output = join(root, 'bundle'), goRoot = join(root, 'toolchain'), module = join(root, 'crypto');
  for (const dir of [join(source, 'dist'), join(source, 'node_modules', 'lucide'), output, module, goRoot]) await mkdir(dir, { recursive: true, mode: 0o700 });
  await writeFile(join(source, 'dist', 'THIRD-PARTY-LICENSES.json'), JSON.stringify([{ name: 'lucide', version: '1.59.0', identifier: 'ISC', text: upstream.trim() }]));
  await writeFile(join(source, 'node_modules', 'lucide', 'package.json'), JSON.stringify({ name: 'lucide', version: '1.59.0' }));
  await writeFile(join(source, 'node_modules', 'lucide', 'LICENSE'), upstream);
  await writeFile(join(module, 'LICENSE'), upstream);
  await writeFile(join(goRoot, 'LICENSE'), upstream);
  await writeFile(join(goRoot, 'VERSION'), 'go1.26.9\ntime test-only\n');
  for (const [name, version, files] of [['vite', '8.3.4', ['LICENSE.md']], ['rolldown', '1.2.14', ['LICENSE', 'THIRD-PARTY-LICENSE']]] as const) {
    const directory = join(source, 'node_modules', name); await mkdir(directory);
    await writeFile(join(directory, 'package.json'), JSON.stringify({ name, version }));
    for (const file of files) await writeFile(join(directory, file), upstream);
  }
  for (const name of ['crypto', 'net', 'sys', 'text']) {
    const directory = join(goRoot, 'src', 'vendor', 'golang.org', 'x', name);
    await mkdir(directory, { recursive: true }); await writeFile(join(directory, 'LICENSE'), upstream);
  }
  return { source, output, goRoot, module, modules: { 'golang.org/x/crypto': { Dir: module, Version: 'v0.58.0' } } };
}

test('内部包附实际网页/Go依赖原文，覆盖清单可复核且不带缓存路径', async () => {
  const { source, output, goRoot, modules } = await fixture();
  await localBuild.stageThirdPartyNotices(source, output, info, goRoot, modules);
  const summary = await readFile(join(output, 'THIRD-PARTY-NOTICES.md'), 'utf8');
  assert.match(summary, /lucide.*1\.59\.0/); assert.match(summary, /golang.org\/x\/crypto.*v0\.58\.0/);
  assert.match(summary, /面板源码以GPL-3\.0授权/); assert.match(summary, /不包含Xray/);
  assert.equal(summary.includes(source), false); assert.equal(summary.includes(goRoot), false);
  assert.equal(await readFile(join(output, 'licenses', 'npm', 'lucide', 'LICENSE'), 'utf8'), upstream);
  assert.equal(await readFile(join(output, 'licenses', 'go', 'golang.org', 'x', 'crypto', 'LICENSE'), 'utf8'), upstream);
  const manifest = JSON.parse(await readFile(join(output, 'licenses', 'components.json'), 'utf8'));
  assert.deepEqual(manifest.components.map((item: { name: string }) => item.name).sort(), ['Go', 'golang.org/x/crypto', 'lucide', 'rolldown', 'vite']);
  assert.ok(manifest.components.every((item: { files: string[] }) => item.files.length > 0 && item.files.every(file => !file.startsWith('/'))));
  await assert.rejects(localBuild.stageThirdPartyNotices(source, output, info, goRoot, modules), { code: 'EEXIST' });
});

test('缺失网页清单、未知或重复依赖、版本漂移及Go替换模块均拒绝完成', async t => {
  for (const scenario of ['missing', 'unknown', 'duplicate', 'npm-version', 'go-version', 'go-unknown', 'replace', 'root-version', 'duplicate-go', 'toolchain', 'build-runtime']) {
    await t.test(scenario, async () => {
      const { source, output, goRoot, modules } = await fixture();
      const buildInfo: any = structuredClone(info);
      const entry = { name: 'lucide', version: '1.59.0', identifier: 'ISC', text: upstream.trim() };
      if (scenario !== 'missing') await writeFile(join(source, 'dist', 'THIRD-PARTY-LICENSES.json'), JSON.stringify(scenario === 'duplicate' ? [entry, entry] : [{ ...entry, ...(scenario === 'unknown' ? { name: 'not-reviewed' } : {}), ...(scenario === 'npm-version' ? { version: '2.0.0' } : {}) }]));
      else await writeFile(join(source, 'dist', 'THIRD-PARTY-LICENSES.json'), 'null');
      if (scenario === 'go-version') buildInfo.Deps[0].Version = 'v0.59.0';
      if (scenario === 'go-unknown') buildInfo.Deps[0].Path = 'unknown.invalid/module';
      if (scenario === 'replace') buildInfo.Deps[0].Replace = { Path: '/not-reviewed' };
      if (scenario === 'root-version') modules['golang.org/x/crypto'].Version = 'v0.59.0';
      if (scenario === 'duplicate-go') buildInfo.Deps.push({ ...buildInfo.Deps[0] });
      if (scenario === 'toolchain') buildInfo.GoVersion = 'go1.26.8';
      if (scenario === 'build-runtime') await writeFile(join(source, 'node_modules', 'vite', 'package.json'), JSON.stringify({ name: 'vite', version: '0.0.0' }));
      await assert.rejects(localBuild.stageThirdPartyNotices(source, output, buildInfo, goRoot, modules));
      assert.equal((await readdir(output)).includes('THIRD-PARTY-NOTICES.md'), false);
    });
  }
});

test('许可原文缺失、内容不一致或符号链接不产生完成说明', async t => {
  for (const scenario of ['empty', 'mismatch', 'symlink', 'absent']) await t.test(scenario, async () => {
    const { source, output, goRoot, modules } = await fixture();
    if (scenario === 'empty') await writeFile(join(goRoot, 'LICENSE'), '');
    if (scenario === 'mismatch') await writeFile(join(source, 'node_modules', 'lucide', 'LICENSE'), 'changed-after-build');
    if (scenario === 'symlink') {
      const other = join(source, 'not-a-module'); await mkdir(other);
      await symlink(other, join(other, 'LICENSE')); modules['golang.org/x/crypto'].Dir = other;
    }
    if (scenario === 'absent') {
      const other = join(source, 'module-without-license'); await mkdir(other); modules['golang.org/x/crypto'].Dir = other;
    }
    await assert.rejects(localBuild.stageThirdPartyNotices(source, output, info, goRoot, modules));
    assert.equal((await readdir(output)).includes('THIRD-PARTY-NOTICES.md'), false);
  });
});

test('实际内部包许可覆盖程序/网页版本，继承原文及声明均参与摘要', { skip: !process.env.PANEL_LOCAL_NOTICE_BUNDLE_QA }, async () => {
  const bundle = process.env.PANEL_LOCAL_NOTICE_BUNDLE_QA!;
  const manifest = JSON.parse(await readFile(join(bundle, 'bundle.json'), 'utf8'));
  const covered = JSON.parse(await readFile(join(bundle, 'licenses', 'components.json'), 'utf8')).components;
  const binary = JSON.parse(execFileSync('go', ['version', '-m', '-json', join(bundle, 'bin', 'o-ui')], { encoding: 'utf8' }));
  const web = JSON.parse(await readFile(join(bundle, 'dist', 'THIRD-PARTY-LICENSES.json'), 'utf8'));
  const versions = (items: { name: string; version: string }[]) => items.map(item => `${item.name}@${item.version}`).sort();
  assert.deepEqual(versions(covered.filter((item: { kind: string }) => item.kind === 'go')), versions(binary.Deps.map((item: { Path: string; Version: string }) => ({ name: item.Path, version: item.Version }))));
  assert.deepEqual(versions(covered.filter((item: { kind: string }) => item.kind === 'npm')), versions(web));
  assert.equal(covered.find((item: { name: string }) => item.name === 'Go').version, binary.GoVersion);
  const paths = new Set(manifest.files.map((item: { path: string }) => item.path));
  for (const component of covered) for (const path of component.files) {
    assert.ok(paths.has(path), `许可文件未纳入清单：${path}`);
    assert.ok((await readFile(join(bundle, path), 'utf8')).trim());
  }
  assert.ok(paths.has('THIRD-PARTY-NOTICES.md')); assert.ok(paths.has('licenses/components.json'));
  assert.match(await readFile(join(bundle, 'licenses/npm/lucide/LICENSE'), 'utf8'), /Cole Bemis/);
  assert.match(await readFile(join(bundle, 'licenses/npm/qrcode/UPSTREAM-COPYRIGHT.txt'), 'utf8'), /Kazuhiko Arase/);
  assert.match(await readFile(join(bundle, 'licenses/npm/dijkstrajs/MIT-FULL-TEXT.txt'), 'utf8'), /Permission is hereby granted/);
  assert.match(await readFile(join(bundle, 'licenses/go/modernc.org/libc/LICENSE-3RD-PARTY.md'), 'utf8'), /musl libc/);
  assert.match(await readFile(join(bundle, 'licenses/go/modernc.org/sqlite/LICENSE-3RD-PARTY.md'), 'utf8'), /SQLite/);
  for (const name of ['vite', 'rolldown']) assert.ok(covered.some((item: { kind: string; name: string }) => item.kind === 'build-runtime' && item.name === name));
});
