import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const hooks = registerHooks({ load(url, context, next) {
  return url.endsWith('.css') ? { format: 'module', source: '', shortCircuit: true } : next(url, context);
} });
const { downloadIcon, nextProgress } = await import('../src/upgrade.ts');
hooks.deregister();

test('升级进度参照OUI：先快后慢逼近95%，确认新版本前不到100%', () => {
  const steps = [8];
  while (steps.length < 60) steps.push(nextProgress(steps.at(-1)!));
  assert.deepEqual(steps.slice(0, 5), [8, 18, 27, 35, 42]);
  assert.ok(steps.every((value, i) => i === 0 || value >= steps[i - 1]), 'progress never goes back');
  assert.equal(steps.at(-1), 95);
  assert.equal(nextProgress(95), 95);
  assert.equal(nextProgress(100), 100);
});

test('下载图标只在升级进行中播放动画', () => {
  assert.match(downloadIcon(true), /download-icon is-active/);
  assert.doesNotMatch(downloadIcon(false), /is-active/);
  assert.match(downloadIcon(false), /aria-hidden="true"/);
});
