import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile, readdir, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { stageLocalFiles, stageReleaseFiles, writeLocalManifest } from '../scripts/build-local.mjs';
import { packBundle, releaseArchitectures, releaseVersion, writeReleaseSums } from '../scripts/build-release.mjs';

process.env.LC_ALL = 'zh_CN.UTF-8';
const startScript = new URL('../scripts/start-local.sh', import.meta.url);

test('Release版本号必须显式且按日期（2026-10-21、同日第二个起-v1），平台只有linux-amd64与linux-arm64', () => {
  assert.deepEqual(releaseArchitectures, ['amd64', 'arm64']);
  for (const version of ['2026-10-21', '2026-10-21-v1', '2026-10-21-v12', '2028-02-29']) assert.equal(releaseVersion(['--version', version]), version);
  for (const args of [[], ['2026-10-21'], ['--version'], ['--version', 'v1.2.3'], ['--version', '2026-10-21-v0'], ['--version', '2026-10-21-1'], ['--version', '2026-02-30'], ['--version', '2026-1-21'], ['--version', '2026-10-21;rm'], ['--version', '2026-10-21', 'x']]) assert.throws(() => releaseVersion(args), /用法/);
});

async function fixture(installer = '#!/usr/bin/env bash\nREPO=${O_UI_REPO:-owner/o-ui}\nACME_SH_SHA256=' + 'a'.repeat(64) + '\n') {
  const root = await mkdtemp(join(tmpdir(), 'o-ui-release-')), source = join(root, 'source'), stage = join(root, 'stage'), bundle = join(stage, 'o-ui');
  await mkdir(join(source, 'scripts'), { recursive: true }); await mkdir(join(source, 'dist'), { recursive: true }); await mkdir(join(bundle, 'bin'), { recursive: true });
  await writeFile(join(source, 'scripts', 'start-local.sh'), await readFile(startScript));
  for (const name of ['本机运行说明.md', 'SOURCES.md', '安全边界.md']) await writeFile(join(source, name), 'test-only-document');
  await writeFile(join(source, 'dist', 'index.html'), '<html><head></head></html>');
  await writeFile(join(source, 'install.sh'), installer); await writeFile(join(source, 'LICENSE'), 'GPL-3.0 test text');
  await writeFile(join(bundle, 'bin', 'o-ui'), '#!/bin/sh\n', { mode: 0o700 });
  return { root, source, stage, bundle };
}

test('Release包附带安装脚本、许可证与版本号并计入清单；未填写仓库或acme.sh摘要时拒绝', async () => {
  const unconfigured = await fixture('REPO=${O_UI_REPO:-__O_UI_REPO__}\nACME_SH_SHA256=__ACME_SH_SHA256__\n');
  await stageLocalFiles(unconfigured.source, unconfigured.bundle);
  await assert.rejects(stageReleaseFiles(unconfigured.source, unconfigured.bundle, 'v0.1.0'), /尚未填写发布仓库或acme\.sh摘要/);
  const { source, bundle } = await fixture();
  await stageLocalFiles(source, bundle); await stageReleaseFiles(source, bundle, 'v0.1.0');
  await writeLocalManifest(bundle, { os: 'linux', arch: 'amd64' }, { version: 'v0.1.0' });
  assert.deepEqual((await readdir(bundle)).sort(), ['LICENSE', 'PLATFORM', 'SHA256SUMS', 'SOURCES.md', 'VERSION', 'bin', 'bundle.json', 'dist', 'install.sh', 'o-ui', '安全边界.md', '本机运行说明.md'].sort());
  assert.equal(await readFile(join(bundle, 'VERSION'), 'utf8'), 'v0.1.0\n');
  assert.equal((await stat(join(bundle, 'install.sh'))).mode & 0o777, 0o700);
  const manifest = JSON.parse(await readFile(join(bundle, 'bundle.json'), 'utf8'));
  assert.equal(manifest.localOnly, false); assert.equal(manifest.version, 'v0.1.0');
  const sums = await readFile(join(bundle, 'SHA256SUMS'), 'utf8');
  for (const name of ['install.sh', 'LICENSE', 'VERSION', 'PLATFORM', 'bin/o-ui', 'o-ui', 'bundle.json']) assert.match(sums, new RegExp(`^[0-9a-f]{64}  ${name.replace('.', '\\.')}$`, 'm'));
  await assert.rejects(stageReleaseFiles(source, bundle, 'v0.1.0'), { code: 'EEXIST' });
});

test('打包只含o-ui目录、属主为root、无macOS附加文件，解压后包内清单可逐项复核', async () => {
  const { root, source, stage, bundle } = await fixture();
  await stageLocalFiles(source, bundle); await stageReleaseFiles(source, bundle, 'v0.1.0');
  await writeLocalManifest(bundle, { os: 'linux', arch: 'arm64' }, { version: 'v0.1.0' });
  const out = join(root, 'o-ui-linux-arm64.tar.gz');
  packBundle(stage, out);
  const names = execFileSync('tar', ['-tzf', out], { encoding: 'utf8' }).trim().split('\n');
  assert.ok(names.every(name => name === 'o-ui/' || name.startsWith('o-ui/')), names.join(','));
  assert.ok(!names.some(name => /(^|\/)\._/.test(name)), 'AppleDouble entry packed');
  for (const line of execFileSync('tar', ['-tvzf', out], { encoding: 'utf8' }).trim().split('\n')) assert.match(line, /\broot[\s/]+root\b|\b0[\s/]+0\b/, line); // macOS bsdtar列为“0 0”，Linux GNU tar列为“0/0”
  const extracted = join(root, 'extract'); await mkdir(extracted);
  execFileSync('tar', ['-xzf', out, '-C', extracted]);
  execFileSync('shasum', ['-a', '256', '-c', '--quiet', 'SHA256SUMS'], { cwd: join(extracted, 'o-ui') });
  assert.equal(await readFile(join(extracted, 'o-ui', 'PLATFORM'), 'utf8'), 'linux-arm64\n');
  await writeFile(join(root, 'install.sh'), 'installer');
  await writeReleaseSums(root, ['o-ui-linux-arm64.tar.gz', 'install.sh']);
  const sha = (data: Buffer) => createHash('sha256').update(data).digest('hex');
  assert.equal(await readFile(join(root, 'SHA256SUMS'), 'utf8'), `${sha(await readFile(out))}  o-ui-linux-arm64.tar.gz\n${sha(await readFile(join(root, 'install.sh')))}  install.sh\n`);
  await assert.rejects(writeReleaseSums(root, ['install.sh']), { code: 'EEXIST' });
});
