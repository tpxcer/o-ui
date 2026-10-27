import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, extname, resolve, sep } from 'node:path';

const directory = resolve(fileURLToPath(new URL('../dist/', import.meta.url)));
let html = await readFile(resolve(directory, 'index.html'), 'utf8');
async function inlineImage(from, reference) {
  const asset = resolve(from, reference);
  const mime = { '.png': 'image/png', '.svg': 'image/svg+xml', '.webp': 'image/webp' }[extname(asset)];
  if (!asset.startsWith(directory + sep) || !mime) throw new Error('预览图片必须是 dist 内的支持格式');
  return `data:${mime};base64,${(await readFile(asset)).toString('base64')}`;
}
for (const match of [...html.matchAll(/<link[^>]*rel="icon"[^>]*href="([^"]+)"[^>]*>/g)]) {
  html = html.replace(match[0], match[0].replace(match[1], await inlineImage(directory, match[1])));
}
for (const match of [...html.matchAll(/<script[^>]*src="([^"]+)"[^>]*><\/script>|<link[^>]*href="([^"]+\.css)"[^>]*>/g)]) {
  const asset = resolve(directory, (match[1] || match[2]).replace(/^\//, ''));
  if (!asset.startsWith(directory + sep)) throw new Error('预览资产必须位于 dist 内');
  let content = await readFile(asset, 'utf8');
  if (match[2]) {
    for (const image of [...content.matchAll(/url\((['"]?)([^)'"\s]+)\1\)/g)]) {
      if (image[2].startsWith('data:') || image[2].startsWith('#')) continue;
      content = content.replace(image[0], `url("${await inlineImage(dirname(asset), image[2])}")`);
    }
  }
  const inline = match[1]
    ? `<script type="module">${content.replace(/<\/script/gi, '<\\/script')}</script>`
    : `<style>${content.replace(/<\/style/gi, '<\\/style')}</style>`;
  // 用回调原样插入；脚本里的 $`、$'、$& 不能被当成替换模板展开。
  html = html.replace(match[0], () => inline);
}
if (/<(?:script|link)[^>]*(?:src|href)="\//.test(html)) throw new Error('预览仍有外部资源引用');
await writeFile(resolve(directory, '首页预览.html'), html);
console.log('已导出 dist/首页预览.html（无需本地服务）');
