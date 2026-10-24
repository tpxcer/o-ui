import { lstat, mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';

const npmCatalog = {
  lucide: ['1.59.0', 'ISC', ['LICENSE']],
  qrcode: ['1.5.4', 'MIT', ['license']],
  dijkstrajs: ['1.0.3', 'MIT', ['LICENSE.md']],
  sortablejs: ['1.15.7', 'MIT', ['LICENSE']],
};
const buildRuntimeCatalog = {
  vite: ['8.3.4', ['LICENSE.md']],
  rolldown: ['1.2.14', ['LICENSE', 'THIRD-PARTY-LICENSE']],
};
const goCatalog = {
  'github.com/dustin/go-humanize': ['v1.0.1', 'MIT'],
  'github.com/ebitengine/purego': ['v0.11.1', 'Apache-2.0 / Go runtime BSD-3-Clause', ['LICENSE', 'README.md']],
  'github.com/google/uuid': ['v1.6.0', 'BSD-3-Clause'],
  'github.com/mattn/go-isatty': ['v0.0.24', 'MIT'],
  'github.com/ncruces/go-strftime': ['v1.0.0', 'MIT'],
  'github.com/remyoudompheng/bigfft': ['v0.0.0-20230129092748-24d4a6f8daec', 'BSD-3-Clause'],
  'github.com/shirou/gopsutil/v4': ['v4.26.9', 'BSD-3-Clause'],
  'github.com/tklauser/go-sysconf': ['v0.4.0', 'BSD-3-Clause'],
  'github.com/tklauser/numcpus': ['v0.12.0', 'Apache-2.0'],
  'golang.org/x/crypto': ['v0.58.0', 'BSD-3-Clause'],
  'golang.org/x/sys': ['v0.49.0', 'BSD-3-Clause'],
  'modernc.org/libc': ['v1.77.1', 'BSD-3-Clause / inherited notices', ['LICENSE', 'LICENSE-3RD-PARTY.md']],
  'modernc.org/mathutil': ['v1.7.1', 'BSD-3-Clause', ['LICENSE', 'mersenne/LICENSE']],
  'modernc.org/memory': ['v1.12.1', 'BSD-3-Clause', ['LICENSE', 'LICENSE-GO', 'LICENSE-MMAP-GO', 'LICENSE-LOGO']],
  'modernc.org/sqlite': ['v1.60.1', 'BSD-3-Clause / SQLite public domain / inherited notices', ['LICENSE', 'LICENSE-SQLITE', 'LICENSE-SQLITE_VEC', 'LICENSE-3RD-PARTY.md']],
};

async function original(root, file) {
  let path = root;
  if (!(await lstat(root)).isDirectory()) throw new Error('许可来源必须是普通目录，不接受符号链接');
  const parts = file.split('/');
  for (const [index, part] of parts.entries()) {
    path = join(path, part);
    const info = await lstat(path);
    if (index === parts.length - 1 ? !info.isFile() : !info.isDirectory()) throw new Error('许可来源必须是普通文件，不接受符号链接');
  }
  const data = await readFile(path);
  if (!data.toString('utf8').trim()) throw new Error('许可原文为空，尚未完成内部包');
  return data;
}

export async function stageThirdPartyNotices(source, destination, buildInfo, goRoot, modules) {
  const npm = JSON.parse(await original(source, 'dist/THIRD-PARTY-LICENSES.json'));
  if (!Array.isArray(npm) || !npm.length || !Array.isArray(buildInfo.Deps)) throw new Error('缺少实际构建依赖清单');
  if ((await original(goRoot, 'VERSION')).toString('utf8').split('\n')[0] !== buildInfo.GoVersion) throw new Error('Go许可来源与实际编译工具链版本不一致');
  const components = [], pending = [], seen = new Set();
  async function component(kind, name, version, license, root, files) {
    const key = `${kind}/${name}`;
    if (seen.has(key)) throw new Error('实际构建依赖重复');
    seen.add(key);
    const paths = [];
    for (const file of files) {
      const path = `licenses/${key}/${file}`;
      pending.push([path, await original(root, file)]); paths.push(path);
    }
    const record = { kind, name, version, license, files: paths };
    components.push(record);
    return record;
  }
  await component('toolchain', 'Go', buildInfo.GoVersion, 'BSD-3-Clause', goRoot, ['LICENSE', ...['crypto', 'net', 'sys', 'text'].map(name => `src/vendor/golang.org/x/${name}/LICENSE`)]);
  for (const [name, [version, files]] of Object.entries(buildRuntimeCatalog)) {
    const metadata = JSON.parse(await original(source, `node_modules/${name}/package.json`));
    if (metadata.name !== name || metadata.version !== version) throw new Error('构建注入代码的工具版本未核对许可');
    await component('build-runtime', name, version, 'MIT / inherited notices', join(source, 'node_modules', name), files);
  }
  for (const item of npm) {
    const reviewed = Object.hasOwn(npmCatalog, item.name) && npmCatalog[item.name];
    if (!reviewed || item.version !== reviewed[0] || item.identifier !== reviewed[1]) throw new Error('网页依赖或版本未核对许可，请先更新来源记录');
    const root = join(source, 'node_modules', item.name), metadata = JSON.parse(await original(source, `node_modules/${item.name}/package.json`));
    if (metadata.name !== item.name || metadata.version !== item.version || item.text !== (await original(root, reviewed[2][0])).toString('utf8').trim()) throw new Error('网页许可与实际安装依赖不一致');
    const record = await component('npm', item.name, item.version, item.name === 'lucide' ? 'ISC / Feather MIT' : item.identifier, root, reviewed[2]);
    if (item.name === 'qrcode') {
      const header = (await original(root, 'lib/core/qrcode.js')).toString('utf8').match(/\/\*\*[\s\S]*?\*\//)?.[0];
      if (!header?.includes('Copyright (c) 2009 Kazuhiko Arase')) throw new Error('二维码继承署名发生变化，需重新核对');
      const path = 'licenses/npm/qrcode/UPSTREAM-COPYRIGHT.txt';
      pending.push([path, Buffer.from(header)]); record.files.push(path);
    }
    if (item.name === 'dijkstrajs') {
      const path = 'licenses/npm/dijkstrajs/MIT-FULL-TEXT.txt';
      pending.push([path, await original(source, 'scripts/notices/dijkstrajs-MIT.txt')]); record.files.push(path);
    }
  }
  for (const item of buildInfo.Deps) {
    const reviewed = Object.hasOwn(goCatalog, item.Path) && goCatalog[item.Path], module = modules[item.Path];
    if (!reviewed || item.Version !== reviewed[0] || item.Replace || !module?.Dir || module.Version !== item.Version || module.Replace) throw new Error('Go依赖、版本或替换来源未核对许可，请先更新来源记录');
    await component('go', item.Path, item.Version, reviewed[1], module.Dir, reviewed[2] || ['LICENSE']);
  }
  components.sort((a, b) => `${a.kind}/${a.name}`.localeCompare(`${b.kind}/${b.name}`, 'en'));
  const rows = components.map(item => `| ${item.name} | ${item.version} | ${item.license} | ${item.files.map(file => `[${file.split('/').at(-1)}](${file})`).join('、')} |`);
  const summary = `# 第三方许可与来源\n\n仅供新项目内部测试。面板源码以GPL-3.0授权（见源码仓库LICENSE）；本文件只列第三方依赖许可，不构成法律独立性保证。\n\n网页条目来自Vite实际构建模块，Go条目来自本包程序的构建元数据；许可证保留原文，网页二维码另保留源头署名并补MIT全文。Go标准库及其vendor声明一并附带。build-runtime条目保守提供Vite/Rolldown生成的预加载及打包辅助代码许可，不表示完整工具被打包。原始汇总可能提到未链接的工具、测试或素材，不代表这些内容已打包或授权使用商标。\n\n| 组件 | 版本 | 许可说明（以原文为准） | 随包原文 |\n| --- | --- | --- | --- |\n${rows.join('\n')}\n\n机器可读覆盖清单见[components.json](licenses/components.json)，全部文件纳入SHA256SUMS。不包含本机缓存路径或用户运行数据。\n\n本包不包含Xray、Geo数据或第三方验收客户端。Xray按用户选择另行从官方安装；其MPL-2.0、依赖及Geo数据的源代码/通知/分发义务须在公开发行方案确定后另审，不能用本清单替代。其余开发和测试工具不随此二进制包分发，其声明仍在源码安装依赖中。\n`;
  await mkdir(join(destination, 'licenses'), { mode: 0o700 });
  for (const [path, data] of pending) {
    await mkdir(dirname(join(destination, path)), { recursive: true, mode: 0o700 });
    await writeFile(join(destination, path), data, { flag: 'wx', mode: 0o600 });
  }
  await writeFile(join(destination, 'licenses/components.json'), JSON.stringify({ format: 1, components }, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
  await writeFile(join(destination, 'THIRD-PARTY-NOTICES.md'), summary, { flag: 'wx', mode: 0o600 });
}
