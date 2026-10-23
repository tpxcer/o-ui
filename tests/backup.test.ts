import test from 'node:test';
import assert from 'node:assert/strict';
import { encodeBackup, decodeBackup, isOUIBackup, maxBackupFile } from '../src/backup-model.ts';

test('备份编码往返与大小边界', () => {
  const bytes = Uint8Array.from({ length: 20001 }, (_, i) => i % 256);
  assert.deepEqual(decodeBackup(encodeBackup(bytes)), bytes);
  assert.throws(() => encodeBackup(new Uint8Array()));
  assert.throws(() => encodeBackup(new Uint8Array(maxBackupFile + 1)));
  assert.throws(() => decodeBackup('!invalid'));
  assert.throws(() => decodeBackup(''));
});

test('按 SQLite 文件头识别 OUI 数据库备份', () => {
  const header = new TextEncoder().encode('SQLite format 3\0rest');
  assert.equal(isOUIBackup(header), true);
  assert.equal(isOUIBackup(new TextEncoder().encode('{"format":"independent-xray-panel"}')), false);
  assert.equal(isOUIBackup(new TextEncoder().encode('IXPBACK\u0001')), false);
  assert.equal(isOUIBackup(header.subarray(0, 15)), false);
});
