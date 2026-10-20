import { constants } from 'node:fs';
import { chmod, copyFile, lstat, mkdir, mkdtemp, readFile, readdir, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { stageThirdPartyNotices } from './third-party-notices.mjs';

export { stageThirdPartyNotices };

const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const inputs = [['dist', 'dist'], ['scripts/start-local.sh', 'o-ui'], ['本机运行说明.md', '本机运行说明.md'], ['SOURCES.md', 'SOURCES.md'], ['安全边界.md', '安全边界.md']];

export function buildTarget(args = [], platform = process.platform, architecture = process.arch) {
  if (!args.length) {
    const arch = { arm64: 'arm64', x64: 'amd64' }[architecture];
    if (!['darwin', 'linux'].includes(platform) || !arch) throw new Error('本机构建仅支持macOS/Linux的ARM64或x86_64宿主；请明确选择受支持的目标');
    return { os: platform, arch };
  }
  if (args.length !== 2 || args[0] !== '--target' || !['darwin-amd64', 'darwin-arm64', 'linux-amd64', 'linux-arm64'].includes(args[1])) {
    throw new Error('用法：npm run build:local -- [--target darwin-amd64|darwin-arm64|linux-amd64|linux-arm64]');
  }
  const [os, arch] = args[1].split('-');
  return { os, arch };
}

async function filesAt(root, prefix = '') {
  const files = [];
  for (const item of await readdir(join(root, prefix), { withFileTypes: true })) {
    const name = prefix ? `${prefix}/${item.name}` : item.name;
    if (/[\r\n\\]/.test(name)) throw new Error('构建文件名不能包含换行或反斜杠');
    if (item.isDirectory()) files.push(...await filesAt(root, name));
    else if (item.isFile()) files.push(name);
    else throw new Error('本机构建只接受普通文件和目录，不复制符号链接或设备');
  }
  return files.sort();
}

export async function stageLocalFiles(source, destination) {
  for (const [from, to] of inputs) {
    const input = join(source, from), output = join(destination, to), info = await lstat(input);
    const files = info.isDirectory() ? await filesAt(input) : info.isFile() ? [''] : null;
    if (!files) throw new Error('构建输入不能是符号链接或设备');
    for (const file of files) {
      const target = file ? join(output, file) : output;
      await mkdir(dirname(target), { recursive: true, mode: 0o700 });
      await copyFile(file ? join(input, file) : input, target, constants.COPYFILE_EXCL);
      await chmod(target, to === 'o-ui' ? 0o700 : 0o600);
    }
  }
}

export async function writeLocalManifest(destination, target, release) {
  const { os, arch } = buildTarget(['--target', `${target.os}-${target.arch}`]);
  await writeFile(join(destination, 'PLATFORM'), `${os}-${arch}\n`, { flag: 'wx', mode: 0o600 });
  const files = [];
  for (const file of await filesAt(destination)) {
    const data = await readFile(join(destination, file));
    files.push({ path: file, bytes: data.length, sha256: createHash('sha256').update(data).digest('hex') });
  }
  await writeFile(join(destination, 'bundle.json'), JSON.stringify({ format: 1, localOnly: !release, ...(release ? { version: release.version } : {}), target, files }, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
  const manifest = await readFile(join(destination, 'bundle.json'));
  const sums = [...files.map(file => `${file.sha256}  ${file.path}`), `${createHash('sha256').update(manifest).digest('hex')}  bundle.json`];
  await writeFile(join(destination, 'SHA256SUMS'), sums.join('\n') + '\n', { flag: 'wx', mode: 0o600 });
}

// Release包另附安装脚本、GPL许可证与版本号，同样计入SHA256SUMS
export async function stageReleaseFiles(source, destination, version) {
  if (/__O_UI_REPO__|__ACME_SH_SHA256__/.test(await readFile(join(source, 'install.sh'), 'utf8'))) throw new Error('install.sh尚未填写发布仓库或acme.sh摘要，拒绝生成Release包');
  for (const [name, mode] of [['install.sh', 0o700], ['LICENSE', 0o600]]) {
    await copyFile(join(source, name), join(destination, name), constants.COPYFILE_EXCL);
    await chmod(join(destination, name), mode);
  }
  await writeFile(join(destination, 'VERSION'), `${version}\n`, { flag: 'wx', mode: 0o600 });
}

// 在空目录output中编译并组装一个完整包；release为{version}时生成Release包
export async function assembleBundle(output, os, arch, release) {
  await mkdir(join(output, 'bin'), { mode: 0o700 });
  execFileSync('go', ['build', '-trimpath', '-o', join(output, 'bin', 'o-ui'), './cmd/o-ui'], { cwd: project, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0', GOOS: os, GOARCH: arch } });
  await chmod(join(output, 'bin', 'o-ui'), 0o700);
  await stageLocalFiles(project, output);
  if (release) await stageReleaseFiles(project, output, release.version);
  const info = JSON.parse(execFileSync('go', ['version', '-m', '-json', join(output, 'bin', 'o-ui')], { cwd: project, encoding: 'utf8' }));
  const modules = Object.fromEntries(info.Deps.map(item => [item.Path, JSON.parse(execFileSync('go', ['list', '-mod=readonly', '-m', '-json', item.Path], { cwd: project, encoding: 'utf8', env: { ...process.env, GOPROXY: 'off' } }))]));
  await stageThirdPartyNotices(project, output, info, execFileSync('go', ['env', 'GOROOT'], { cwd: project, encoding: 'utf8' }).trim(), modules);
  await writeLocalManifest(output, { os, arch, go: execFileSync('go', ['env', 'GOVERSION'], { cwd: project, encoding: 'utf8' }).trim() }, release);
}

async function build() {
  const { os, arch } = buildTarget(process.argv.slice(2));
  execFileSync('npm', ['run', 'build'], { cwd: project, stdio: 'inherit' });
  const parent = join(project, 'artifacts', 'local-build');
  await mkdir(parent, { recursive: true, mode: 0o700 });
  const output = await mkdtemp(join(parent, `build-${os}-${arch}-`));
  try {
    await assembleBundle(output, os, arch);
  } catch (error) {
    throw new Error(`本机构建未完成，保留独立目录供检查：${output}`, { cause: error });
  }
  console.log(`内部测试包已生成：${relative(project, output)}\n目标平台：${os}-${arch}；只在匹配的系统与架构上运行，交叉编译不等于实机验收。\n运行时不需要Go或Node；请阅读包内《本机运行说明.md》。未启动服务、未打包现有数据、未发布。`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  build().catch(error => { console.error(error.message); if (error.cause) console.error(error.cause.message); process.exitCode = 1; });
}
