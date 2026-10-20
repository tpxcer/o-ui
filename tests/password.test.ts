import test from 'node:test';
import assert from 'node:assert/strict';
import { passwordChangeError } from '../src/password.ts';

test('改密需当前密码、新密码确认与不同密码', () => {
  assert.match(passwordChangeError('', 'next-test-password', 'next-test-password'), /当前密码/);
  assert.match(passwordChangeError('current-password', 'next-test-password', 'mismatch'), /不一致/);
  assert.match(passwordChangeError('same-test-password', 'same-test-password', 'same-test-password'), /不能.*相同/);
  assert.equal(passwordChangeError('current-password', 'next-test-password', 'next-test-password'), '');
});
test('改密密码按UTF8字节校验，不截断或裁掉空格', () => {
  for (const next of ['a'.repeat(11),'a'.repeat(73),'密'.repeat(25)]) assert.match(passwordChangeError('current-password',next,next), /12至72字节/);
  for (const next of ['a'.repeat(12),'a'.repeat(72),'密'.repeat(24),'  long password  ']) assert.equal(passwordChangeError('current-password',next,next),'');
});
