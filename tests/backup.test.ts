import test from 'node:test';
import assert from 'node:assert/strict';
import { encodeBackup, decodeBackup, maxBackupFile, validateBackupPassword } from '../src/backup-model.ts';

test('备份编码往返与大小边界', () => {
  const bytes = Uint8Array.from({ length: 20001 }, (_, i) => i % 256);
  assert.deepEqual(decodeBackup(encodeBackup(bytes)), bytes);
  assert.throws(() => encodeBackup(new Uint8Array()));
  assert.throws(() => encodeBackup(new Uint8Array(maxBackupFile + 1)));
  assert.throws(() => decodeBackup('!invalid'));
  assert.throws(() => decodeBackup(''));
});
test('备份密码按UTF-8字节校验', () => {
  assert.throws(() => validateBackupPassword('short'));
  assert.throws(() => validateBackupPassword('中'.repeat(86)));
  assert.doesNotThrow(() => validateBackupPassword('中文密码'));
  assert.doesNotThrow(() => validateBackupPassword('x'.repeat(256)));
});
