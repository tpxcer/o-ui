import test from 'node:test';
import assert from 'node:assert/strict';
import { encodeBackup, decodeBackup, maxBackupFile } from '../src/backup-model.ts';

test('备份编码往返与大小边界', () => {
  const bytes = Uint8Array.from({ length: 20001 }, (_, i) => i % 256);
  assert.deepEqual(decodeBackup(encodeBackup(bytes)), bytes);
  assert.throws(() => encodeBackup(new Uint8Array()));
  assert.throws(() => encodeBackup(new Uint8Array(maxBackupFile + 1)));
  assert.throws(() => decodeBackup('!invalid'));
  assert.throws(() => decodeBackup(''));
});
