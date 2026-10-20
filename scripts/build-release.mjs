import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { assembleBundle } from './build-local.mjs';

const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
// 发布平台只保留这两种（用户2026-10-19决定）
export const releaseArchitectures = ['amd64', 'arm64'];

// 版本号按发布日期（用户2026-10-21决定）：当天第一个为2026-10-21，第二个起为2026-10-21-v1、-v2……
export function releaseVersion(args = []) {
  const match = args.length === 2 && args[0] === '--version' ? /^(\d{4})-(\d{2})-(\d{2})(-v[1-9]\d{0,3})?$/.exec(args[1]) : null;
  const day = match ? new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]))) : null;
  if (!match || !day || day.getUTCFullYear() !== Number(match[1]) || day.getUTCMonth() !== Number(match[2]) - 1 || day.getUTCDate() !== Number(match[3])) {
    throw new Error('用法：npm run build:release -- --version 2026-10-21（同日第二个起为2026-10-21-v1、-v2……）');
  }
  return args[1];
}

// 打成只含o-ui/目录的tar.gz：属主统一为root，不带macOS扩展属性或AppleDouble文件
export function packBundle(parent, output) {
  const bsd = /bsdtar/.test(execFileSync('tar', ['--version'], { encoding: 'utf8' }));
  const flags = bsd ? ['--uid', '0', '--gid', '0', '--uname', 'root', '--gname', 'root', '--no-xattrs', '--no-acls', '--no-mac-metadata']
    : ['--owner=0', '--group=0', '--numeric-owner', '--no-xattrs', '--no-acls'];
  execFileSync('tar', [...flags, '-czf', output, '-C', parent, 'o-ui'], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
}

export async function writeReleaseSums(directory, names) {
  const lines = [];
  for (const name of names) lines.push(`${createHash('sha256').update(await readFile(join(directory, name))).digest('hex')}  ${name}`);
  await writeFile(join(directory, 'SHA256SUMS'), lines.join('\n') + '\n', { flag: 'wx', mode: 0o644 });
}

async function build() {
  const version = releaseVersion(process.argv.slice(2));
  execFileSync('npm', ['run', 'build'], { cwd: project, stdio: 'inherit' });
  const parent = join(project, 'artifacts', 'release');
  await mkdir(parent, { recursive: true, mode: 0o700 });
  const output = await mkdtemp(join(parent, `o-ui-${version}-`));
  const names = [];
  try {
    for (const arch of releaseArchitectures) {
      const stage = join(output, `.stage-${arch}`);
      await mkdir(join(stage, 'o-ui'), { recursive: true, mode: 0o700 });
      await assembleBundle(join(stage, 'o-ui'), 'linux', arch, { version });
      const name = `o-ui-linux-${arch}.tar.gz`;
      packBundle(stage, join(output, name));
      await chmod(join(output, name), 0o644);
      await rm(stage, { recursive: true });
      names.push(name);
    }
    await copyFile(join(project, 'install.sh'), join(output, 'install.sh'));
    await chmod(join(output, 'install.sh'), 0o755);
    names.push('install.sh');
    await writeReleaseSums(output, names);
  } catch (error) {
    throw new Error(`Release构建未完成，保留目录供检查：${output}`, { cause: error });
  }
  console.log(`Release文件已生成：${relative(project, output)}\n${[...names, 'SHA256SUMS'].join('\n')}\n仅linux-amd64/linux-arm64；交叉编译不等于目标机实测。未上传、未发布。`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  build().catch(error => { console.error(error.message); if (error.cause) console.error(error.cause.message); process.exitCode = 1; });
}
