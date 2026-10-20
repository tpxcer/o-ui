import test from 'node:test';
import assert from 'node:assert/strict';
import { logRows, logTime, type LogSnapshot } from '../src/logs-model.ts';

const snapshot: LogSnapshot = { since: '2026-10-18T10:00:00Z', limit: 256, dropped: 0, entries: [
  { sequence: 1, at: '2026-10-18T10:01:00Z', source: 'panel', level: 'info', code: 'panel.started', message: '启动' },
  { sequence: 2, at: '2026-10-18T10:02:00Z', source: 'xray', level: 'error', code: 'core.port_busy', message: '端口占用' },
  { sequence: 3, at: '2026-10-18T10:03:00Z', source: 'xray', level: 'warning', code: 'core.hidden', message: '未知摘要' },
] };
test('日志来源与级别组合过滤、最新在前，不修改快照', () => {
  assert.ok(logRows(snapshot, 'all', 'all').indexOf('未知摘要') < logRows(snapshot, 'all', 'all').indexOf('启动'));
  assert.ok(!logRows(snapshot, 'panel', 'all').includes('端口占用'));
  assert.ok(logRows(snapshot, 'xray', 'error').includes('端口占用'));
  assert.ok(!logRows(snapshot, 'xray', 'error').includes('未知摘要'));
  assert.ok(logRows(snapshot, 'panel', 'error').includes('暂无记录'));
  assert.equal(snapshot.entries[0].sequence, 1);
});
test('日志动态文本转义，错误有文字标识，时间无效不造值', () => {
  const html = logRows({ ...snapshot, entries: [{ ...snapshot.entries[1], message: '<script>alert("x")</script>&' }] }, 'all', 'all');
  assert.ok(!html.includes('<script>')); assert.ok(html.includes('&lt;script&gt;')); assert.ok(html.includes('错误'));
  assert.equal(logTime('invalid'), '时间未知'); assert.ok(logTime(snapshot.since).includes('2026'));
  assert.ok(logRows({ ...snapshot, entries: [] }, 'all', 'all').includes('写入文件'));
});
