import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile, readdir, realpath, symlink, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createHash, randomBytes } from 'node:crypto';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import { buildTarget, stageLocalFiles, writeLocalManifest } from '../scripts/build-local.mjs';
import { defaultSettings } from '../src/xray-settings-model.ts';

const execute = promisify(execFile);
// 启动脚本与程序按系统区域选择语言；既有断言按中文，英文另测。
process.env.LC_ALL = 'zh_CN.UTF-8';
const script = new URL('../scripts/start-local.sh', import.meta.url);
const hostTarget = buildTarget();

test('本机构建支持明确的Linux及macOS双架构目标，不猜测未知参数', async () => {
  const target = buildTarget;
  assert.deepEqual(target([], 'darwin', 'arm64'), { os: 'darwin', arch: 'arm64' });
  assert.deepEqual(target([], 'linux', 'x64'), { os: 'linux', arch: 'amd64' });
  for (const os of ['linux', 'darwin']) for (const arch of ['amd64', 'arm64']) {
    assert.deepEqual(target(['--target', `${os}-${arch}`], 'darwin', 'arm64'), { os, arch });
  }
  for (const args of [['--target'], ['linux-amd64'], ['--target', 'windows-amd64'], ['--target', 'linux-x64'], ['--target', 'linux-arm64', 'extra'], ['--target=linux-arm64'], ['--target', 'linux-arm64\n'], ['--target', ' linux-amd64']]) {
    assert.throws(() => target(args, 'darwin', 'arm64'), /用法/);
  }
  assert.throws(() => target([], 'win32', 'x64'), /宿主/);
  assert.throws(() => target([], 'linux', 'arm'), /宿主/);
  await assert.rejects(execute(process.execPath, [fileURLToPath(new URL('../scripts/build-local.mjs', import.meta.url)), '--target', 'linux-arm64', 'extra']), (reason: any) => reason.code === 1 && reason.stdout === '' && /用法/.test(reason.stderr));
});

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'ixp-local-build-')), source = join(root, 'source'), output = join(root, 'bundle with spaces');
  await mkdir(join(source, 'scripts'), { recursive: true, mode: 0o700 }); await mkdir(join(source, 'dist', 'assets'), { recursive: true, mode: 0o700 });
  await mkdir(join(source, '.runtime'), { mode: 0o700 }); await mkdir(output, { mode: 0o700 });
  await writeFile(join(source, 'scripts', 'start-local.sh'), await readFile(script));
  for (const name of ['本机运行说明.md', 'SOURCES.md', '安全边界.md']) await writeFile(join(source, name), 'test-only-document');
  await writeFile(join(source, 'dist', 'index.html'), '<html>test</html>'); await writeFile(join(source, 'dist', 'assets', 'test.js'), 'test-only');
  await writeFile(join(source, '.runtime', 'private-test-marker'), 'not-for-bundle');
  await mkdir(join(output, 'bin'), { mode: 0o700 }); await writeFile(join(output, 'bin', 'o-ui'), '#!/bin/sh\nprintf \'%s\\n\' "$@"\n', { mode: 0o700 });
  return { root, source, output };
}

test('本机构建只复制白名单程序资源，不带运行库，清单能逐文件复核', async () => {
  const { source, output } = await fixture();
  await stageLocalFiles(source, output); await writeLocalManifest(output, hostTarget);
  assert.deepEqual((await readdir(output)).sort(), ['PLATFORM', 'SHA256SUMS', 'SOURCES.md', 'bundle.json', 'dist', 'bin', 'o-ui', '安全边界.md', '本机运行说明.md'].sort());
  const manifest = JSON.parse(await readFile(join(output, 'bundle.json'), 'utf8'));
  assert.equal(manifest.localOnly, true); assert.equal(manifest.format, 1);
  assert.deepEqual(manifest.target, hostTarget);
  assert.equal(await readFile(join(output, 'PLATFORM'), 'utf8'), `${hostTarget.os}-${hostTarget.arch}\n`);
  assert.ok(manifest.files.every((file: { path: string }) => !file.path.includes('.runtime') && !file.path.includes('private-test-marker')));
  for (const file of manifest.files) {
    const data = await readFile(join(output, file.path));
    assert.equal(file.bytes, data.length); assert.equal(file.sha256, createHash('sha256').update(data).digest('hex'));
  }
  assert.equal((await stat(join(output, 'o-ui'))).mode & 0o777, 0o700);
  await assert.rejects(stageLocalFiles(source, output), { code: 'EEXIST' });
  await assert.rejects(writeLocalManifest(output, hostTarget), { code: 'EEXIST' });
});

test('本机构建拒绝网页内符号链接，不将外部文件带入包', async () => {
  const { root, source, output } = await fixture();
  const external = join(root, 'not-public'); await writeFile(external, 'test-only-private-marker');
  await symlink(external, join(source, 'dist', 'leak'));
  await assert.rejects(stageLocalFiles(source, output), /符号链接/);
  assert.equal((await readdir(output)).includes('SHA256SUMS'), false);
});

test('未知或缺失目标不能生成完成清单', async () => {
  const { output } = await fixture();
  const targets: Record<string, string>[] = [{}, { os: 'linux', arch: 'x64' }, { os: 'windows', arch: 'amd64' }];
  for (const target of targets) {
    await assert.rejects(writeLocalManifest(output, target), /用法/);
    assert.deepEqual(await readdir(output), ['bin']);
  }
});

test('本机启动与当前工作目录无关，含空格路径保留，重复启动指定同一数据根', async () => {
  const { root, source, output } = await fixture();
  await stageLocalFiles(source, output); await writeLocalManifest(output, hostTarget);
  const data = join(root, 'explicit data root');
  for (const port of ['23876', '23877']) {
    const result = await execute('sh', [join(output, 'o-ui'), data, port], { cwd: tmpdir() });
    assert.deepEqual(result.stdout.trim().split('\n'), ['-listen', `127.0.0.1:${port}`, '-data', join(data, 'core'), '-state', join(data, 'panel'), '-dist', join(await realpath(output), 'dist')]);
  }
});

test('本机启动拒绝缺数据根、相对目录、根目录及非法端口', async () => {
  const { root, source, output } = await fixture(); await stageLocalFiles(source, output); await writeLocalManifest(output, hostTarget);
  for (const args of [[], ['relative'], ['/'], [join(root, 'data'), '0'], [join(root, 'data'), '65536'], [join(root, 'data'), 'x'], [join(root, 'data'), '99999999999999999999']]) {
    await assert.rejects(execute('sh', [join(output, 'o-ui'), ...args]), (reason: any) => reason.code === 1 && reason.stdout === '');
  }
});

test('启动命令提示按系统区域显示简体中文或英文', async () => {
  const { root, source, output } = await fixture(); await stageLocalFiles(source, output); await writeLocalManifest(output, hostTarget);
  for (const [locale, pattern] of [['zh_CN.UTF-8', /用法：o-ui/], ['en_US.UTF-8', /Usage: o-ui/], ['C', /Usage: o-ui/]] as const) {
    await assert.rejects(execute('sh', [join(output, 'o-ui')], { env: { ...process.env, LC_ALL: locale } }), (reason: any) => reason.code === 1 && pattern.test(reason.stderr));
  }
  const data = join(root, 'data');
  const english = await execute('sh', [join(output, 'o-ui'), data], { env: { ...process.env, LC_ALL: 'en_US.UTF-8' } });
  assert.match(english.stderr, /Starting o-ui/);
});

test('本机包不完整或资源摘要改变时拒绝启动', async () => {
  const { root, source, output } = await fixture(); await stageLocalFiles(source, output);
  await assert.rejects(execute('sh', [join(output, 'o-ui'), join(root, 'data')]), (reason: any) => reason.code === 1 && reason.stdout === '');
  await writeLocalManifest(output, hostTarget); await writeFile(join(output, 'dist', 'assets', 'test.js'), 'tampered-test');
  await assert.rejects(execute('sh', [join(output, 'o-ui'), join(root, 'data')]), (reason: any) => reason.code === 1 && reason.stdout === '');
});

test('启动脚本核对构建目标与uname，匹配才执行；错包不创建数据目录', async () => {
  const { root, source, output } = await fixture();
  await stageLocalFiles(source, output); await writeLocalManifest(output, { os: 'linux', arch: 'arm64' });
  const bin = join(root, 'bin'); await mkdir(bin, { mode: 0o700 });
  const mock = join(bin, 'uname');
  await writeFile(mock, '#!/bin/sh\ncase "$1" in -s) printf "%s\\n" Linux;; -m) printf "%s\\n" x86_64;; *) exit 1;; esac\n', { mode: 0o700 });
  const data = join(root, 'must-not-be-created');
  await assert.rejects(execute('sh', [join(output, 'o-ui'), data], { env: { ...process.env, PATH: `${bin}:${process.env.PATH}` } }), (reason: any) => reason.code === 1 && reason.stdout === '' && /linux-arm64.*linux-amd64/.test(reason.stderr));
  await assert.rejects(stat(data), { code: 'ENOENT' });
  await writeFile(mock, '#!/bin/sh\ncase "$1" in -s) printf "%s\\n" Linux;; -m) printf "%s\\n" aarch64;; *) exit 1;; esac\n', { mode: 0o700 });
  const result = await execute('sh', [join(output, 'o-ui'), data], { env: { ...process.env, PATH: `${bin}:${process.env.PATH}` } });
  assert.match(result.stdout, /127\.0\.0\.1:5180/);
  assert.match(result.stderr, /正在启动 o-ui；使用已保存的访问设置/);
});

test('启动平台识别覆盖两种系统的常用架构名，未知系统或架构拒绝', async () => {
  for (const [os, machine, targetOS, arch, allowed] of [
    ['Darwin', 'arm64', 'darwin', 'arm64', true], ['Darwin', 'x86_64', 'darwin', 'amd64', true],
    ['Linux', 'arm64', 'linux', 'arm64', true], ['Linux', 'amd64', 'linux', 'amd64', true],
    ['Darwin', 'arm64', 'linux', 'arm64', false], ['FreeBSD', 'amd64', 'linux', 'amd64', false],
    ['Linux', 'riscv64', 'linux', 'arm64', false],
  ] as const) {
    const { root, source, output } = await fixture();
    await stageLocalFiles(source, output); await writeLocalManifest(output, { os: targetOS, arch });
    const bin = join(root, 'bin'); await mkdir(bin, { mode: 0o700 });
    await writeFile(join(bin, 'uname'), `#!/bin/sh\ncase "$1" in -s) printf '%s\\n' '${os}';; -m) printf '%s\\n' '${machine}';; *) exit 1;; esac\n`, { mode: 0o700 });
    const data = join(root, 'data'), options = { env: { ...process.env, PATH: `${bin}:${process.env.PATH}` } };
    if (allowed) assert.match((await execute('sh', [join(output, 'o-ui'), data], options)).stdout, /127\.0\.0\.1:5180/);
    else await assert.rejects(execute('sh', [join(output, 'o-ui'), data], options), (reason: any) => reason.code === 1 && reason.stdout === '' && /尚未启动面板/.test(reason.stderr));
    await assert.rejects(stat(data), { code: 'ENOENT' });
  }
});

test('Linux交叉包实物架构与清单一致，错平台启动不写数据', { skip: !process.env.PANEL_LOCAL_CROSS_BUNDLE_QA, timeout: 15000 }, async () => {
  const bundle = resolve(process.env.PANEL_LOCAL_CROSS_BUNDLE_QA!);
  const manifest = JSON.parse(await readFile(join(bundle, 'bundle.json'), 'utf8'));
  assert.equal(manifest.format, 1); assert.equal(manifest.localOnly, true); assert.equal(manifest.target.os, 'linux');
  assert.ok(['amd64', 'arm64'].includes(manifest.target.arch));
  assert.notDeepEqual({ os: manifest.target.os, arch: manifest.target.arch }, hostTarget, '此专项只验证错平台包，不在匹配系统上启动');
  assert.equal(await readFile(join(bundle, 'PLATFORM'), 'utf8'), `linux-${manifest.target.arch}\n`);
  assert.ok(manifest.files.some((file: { path: string }) => file.path === 'PLATFORM'));
  for (const file of manifest.files) {
    const data = await readFile(join(bundle, file.path));
    assert.equal(data.length, file.bytes); assert.equal(createHash('sha256').update(data).digest('hex'), file.sha256);
  }
  const binary = await readFile(join(bundle, 'bin', 'o-ui'));
  assert.equal(binary.subarray(0, 4).toString('hex'), '7f454c46'); assert.equal(binary[4], 2); assert.equal(binary[5], 1);
  assert.equal(binary.readUInt16LE(18), manifest.target.arch === 'amd64' ? 62 : 183);
  const metadata = (await execute('go', ['version', '-m', join(bundle, 'bin', 'o-ui')])).stdout;
  assert.match(metadata, /GOOS=linux/); assert.ok(metadata.includes(`GOARCH=${manifest.target.arch}`)); assert.match(metadata, /CGO_ENABLED=0/);
  const temporary = await mkdtemp(join(tmpdir(), 'ixp-cross-bundle-')), data = join(temporary, 'must-not-be-created');
  await assert.rejects(execute(join(bundle, 'o-ui'), [data], { cwd: tmpdir() }), (reason: any) => reason.code === 1 && reason.stdout === '' && /平台不匹配/.test(reason.stderr));
  await assert.rejects(stat(data), { code: 'ENOENT' });
});

test('本机实际包从外部工作目录启动、静态资源完整、临时管理员与草稿重启保持', { skip: !process.env.PANEL_LOCAL_BUNDLE_QA, timeout: 45000 }, async () => {
  const bundle = resolve(process.env.PANEL_LOCAL_BUNDLE_QA!), root = await mkdtemp(join(tmpdir(), 'ixp-bundle-process-'));
  const data = join(root, 'independent data'), username = 'bundle-test', password = randomBytes(24).toString('base64url');
  const marker = { ...defaultSettings(), probeURL: 'https://bundle-test.invalid/' };
  const portServer = createServer(); await new Promise<void>(done => portServer.listen(0, '127.0.0.1', done));
  const address = portServer.address(); assert.ok(address && typeof address !== 'string'); const port = address.port;
  await new Promise<void>((done, reject) => portServer.close(error => error ? reject(error) : done()));
  const base = `http://127.0.0.1:${port}`;
  let cookie = '', child: ReturnType<typeof spawn> | null = null, closed: Promise<{ code: number | null; signal: string | null }> | null = null;
  async function request(path: string, method = 'GET', body?: unknown) {
    return fetch(`${base}${path}`, { method, signal: AbortSignal.timeout(2000), headers: { ...(cookie ? { Cookie: cookie } : {}), ...(method !== 'GET' ? { Origin: base, 'Content-Type': 'application/json' } : {}) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
  }
  async function start() {
    child = spawn(join(bundle, 'o-ui'), [data, String(port)], { cwd: tmpdir(), stdio: 'ignore' });
    closed = new Promise((done, reject) => { child!.once('error', reject); child!.once('close', (code, signal) => done({ code, signal })); });
    for (let i = 0; i < 80; i++) {
      if (child.exitCode !== null) throw new Error('本机包提前退出，未通过启动验收');
      try { const response = await request('/api/auth/status'); if (response.ok) return await response.json(); } catch {}
      await new Promise(done => setTimeout(done, 50));
    }
    throw new Error('本机包未在期限内就绪');
  }
  async function stop() {
    if (!child) return;
    const process = child, completion = closed!; child = null;
    if (process.exitCode === null && process.signalCode === null) process.kill('SIGTERM');
    const watchdog = setTimeout(() => process.kill('SIGKILL'), 7000);
    try { assert.deepEqual(await completion, { code: 0, signal: null }); }
    finally { clearTimeout(watchdog); }
  }
  try {
    const first = await start(); assert.equal(first.initialized, false); assert.equal(first.authenticated, false);
    const page = await request('/'); assert.equal(page.status, 200); const html = await page.text();
    const assets = [...html.matchAll(/(?:src|href)="([^"]+)"/g)].map(match => new URL(match[1]!, base)).filter(url => url.origin === base && url.pathname.startsWith('/assets/')).map(url => url.pathname); assert.ok(assets.length >= 2);
    for (const asset of assets) { const response = await request(asset); assert.equal(response.status, 200); assert.ok((await response.arrayBuffer()).byteLength > 0); }
    const setup = await request('/api/auth/setup', 'POST', { username, password }); assert.equal(setup.status, 200);
    cookie = setup.headers.get('set-cookie')!.split(';')[0]; assert.ok(cookie);
    const order = await request('/api/panel/list-order/clients', 'PUT', { revision: 0, data: ['display-b', 'display-a'] });
    assert.equal(order.status, 200); assert.equal((await order.json()).revision, 1);
    const saved = await request('/api/settings', 'PUT', { revision: 0, data: marker }); assert.equal(saved.status, 200); assert.equal((await saved.json()).revision, 1);
    const core = await (await request('/api/core/status')).json(); assert.equal(core.selected, '26.7.28'); assert.deepEqual(core.installed, []);
    await stop();
    assert.equal((await stat(join(data, 'panel', 'panel.sqlite'))).mode & 0o777, 0o600);
    const next = await start(); assert.equal(next.initialized, true); assert.equal(next.authenticated, true);
    const settings = await (await request('/api/settings')).json(); assert.equal(settings.revision, 1); assert.deepEqual(settings.data, marker);
    assert.deepEqual(await (await request('/api/panel/list-order/clients')).json(), { revision: 1, data: ['display-b', 'display-a'] });
    assert.deepEqual(await (await request('/api/workspace')).json(), { revision: 0, data: null });
    const runtime = await (await request('/api/runtime/status')).json(); assert.equal(runtime.running, false);
    await stop();
  } finally { await stop(); }
});
