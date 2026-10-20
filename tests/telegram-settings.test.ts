import test from 'node:test';
import assert from 'node:assert/strict';
import { telegramForm, telegramRequest } from '../src/telegram-settings.ts';

const view = { enabled: false, chatIds: '', reportTime: '', notifyLogin: false, cpuThreshold: 0, backupEnabled: false, tokenSet: false, backupPasswordSet: false };
const token = '123456789:AAFakeTokenForIsolatedTestsOnly_abcdefgh';

test('Telegram设置：令牌与口令只写不读，留空保留；启用与备份的前置条件', () => {
  const form = telegramForm({ ...view, tokenSet: true, chatIds: '1001' });
  assert.equal(form.tgToken, ''); assert.equal(form.tgBackupPassword, '');
  assert.deepEqual(telegramRequest({ ...form, tgEnabled: true, tgChatIds: ' 1001 , -2002 ', tgCpuThreshold: '' }, { ...view, tokenSet: true }), { enabled: true, token: '', chatIds: '1001,-2002', reportTime: '', notifyLogin: false, cpuThreshold: 0, backupEnabled: false, backupPassword: '' });
  for (const bad of [{ tgEnabled: true }, { tgToken: 'bad' }, { tgChatIds: '12a' }, { tgReportTime: '24:00' }, { tgCpuThreshold: '101' }, { tgBackupEnabled: true, tgReportTime: '09:00' }, { tgBackupPassword: 'short' }]) {
    assert.throws(() => telegramRequest({ ...telegramForm(view), ...bad }, view), Error, JSON.stringify(bad));
  }
  assert.equal(telegramRequest({ ...telegramForm(view), tgEnabled: true, tgToken: token, tgChatIds: '1', tgReportTime: '09:00', tgBackupEnabled: true, tgBackupPassword: 'backup-passphrase-ok' }, view).backupPassword, 'backup-passphrase-ok');
});
