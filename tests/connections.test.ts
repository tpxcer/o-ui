import assert from 'node:assert/strict';
import { test } from 'node:test';
import jsQR from 'jsqr';
import { connectionQR } from '../src/connection-qr.ts';

test('本机生成二维码的实际SVG可以解码，还原中文名称和编码路径', () => {
  for (const link of [
    'vless://00000000-0000-4000-8000-000000000001@127.0.0.1:18443?security=reality&type=xhttp&path=%2F%E4%B8%AD%E6%96%87%2F%252F&pbk=fictional-public-key#%E4%B8%AD%E6%96%87',
    'hysteria2://fictional-test-auth-only@127.0.0.1:18443/?sni=example.test#%E6%B5%8B%E8%AF%95',
  ]) {
    const svg = decodeURIComponent(connectionQR(link).split(',').slice(1).join(','));
    assert.match(svg, /fill="white"/);
    assert.ok(!svg.includes('<script') && !svg.includes('href=') && !svg.includes(link));
    const size = Number(svg.match(/viewBox="0 0 (\d+)/)![1]), scale = 6, pixels = new Uint8ClampedArray(size * size * scale * scale * 4).fill(255);
    for (const match of svg.matchAll(/M(\d+),(\d+)h1v1h-1z/g)) {
      const x = Number(match[1]) * scale, y = Number(match[2]) * scale;
      for (let dy = 0; dy < scale; dy++) for (let dx = 0; dx < scale; dx++) {
        const offset = ((y + dy) * size * scale + x + dx) * 4;
        pixels[offset] = pixels[offset + 1] = pixels[offset + 2] = 0;
      }
    }
    assert.equal(jsQR(pixels, size * scale, size * scale)?.data, link);
  }
});

test('过长链接明确失败，不生成失真或截断二维码', () => {
  assert.throws(() => connectionQR('vless://' + 'x'.repeat(5000)));
});

test('TLS节点证书不受公共CA信任时给出针对性提示，受信时不建议跳过验证', async () => {
  const { certificateNotice } = await import('../src/connection-notice.ts');
  const untrusted = certificateNotice(false);
  assert.match(untrusted, /不受公共CA信任/); assert.match(untrusted, /导入并信任/); assert.match(untrusted, /仅临时测试/);
  assert.equal(certificateNotice(undefined), untrusted);
  const trusted = certificateNotice(true);
  assert.match(trusted, /保持默认证书验证/); assert.equal(trusted.includes('允许不安全'), false);
});
