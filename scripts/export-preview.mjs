import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve, sep } from 'node:path';

const directory = resolve(fileURLToPath(new URL('../dist/', import.meta.url)));
let html = await readFile(resolve(directory, 'index.html'), 'utf8');
for (const match of [...html.matchAll(/<script[^>]*src="([^"]+)"[^>]*><\/script>|<link[^>]*href="([^"]+\.css)"[^>]*>/g)]) {
  const asset = resolve(directory, (match[1] || match[2]).replace(/^\//, ''));
  if (!asset.startsWith(directory + sep)) throw new Error('预览资产必须位于 dist 内');
  const content = await readFile(asset, 'utf8');
  const inline = match[1]
    ? `<script type="module">${content.replace(/<\/script/gi, '<\\/script')}</script>`
    : `<style>${content.replace(/<\/style/gi, '<\\/style')}</style>`;
  html = html.replace(match[0], inline);
}
if (/<(?:script|link)[^>]*(?:src|href)="\//.test(html)) throw new Error('预览仍有外部资源引用');
await writeFile(resolve(directory, '首页预览.html'), html);
console.log('已导出 dist/首页预览.html（无需本地服务）');
