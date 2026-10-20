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
  for (const next of ['a'.repeat(5),'a'.repeat(73),'密'.repeat(25)]) assert.match(passwordChangeError('current-password',next,next), /6至72字节/);
  for (const next of ['a'.repeat(6),'a'.repeat(72),'密'.repeat(2),'密'.repeat(24),'  long password  ']) assert.equal(passwordChangeError('current-password',next,next),'');
});

test('账号修改可只改用户名或只改密码', () => {
  assert.equal(passwordChangeError('current-password', '', '', 'admin2'), '');
  assert.match(passwordChangeError('current-password', '', '', ''), /新用户名或新密码/);
  assert.match(passwordChangeError('current-password', '', '', 'a b'), /用户名/);
  assert.equal(passwordChangeError('current-password', 'abc123', 'abc123', 'admin2'), '');
});
