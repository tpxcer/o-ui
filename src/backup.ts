import { api, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { decodeBackup, encodeBackup, maxBackupFile, validateBackupPassword } from './backup-model.ts';
import type { BackupPreview, BackupRecord } from './backup-model.ts';
import './backup.css';
import { locale, t } from './i18n.ts';

function download(archive: string, name: string): void {
  const url = URL.createObjectURL(new Blob([decodeBackup(archive)], { type: 'application/octet-stream' }));
  const link = document.createElement('a'); link.href = url; link.download = name.replace(/[^a-zA-Z0-9_.-]/g, '_');
  document.body.append(link); link.click(); link.remove(); window.setTimeout(() => URL.revokeObjectURL(url), 30000);
}

export function initBackup(refreshIcons: () => void): { open: () => void } {
  const dialog = document.createElement('dialog');
  dialog.className = 'backup-dialog'; dialog.setAttribute('aria-labelledby', 'backup-title'); document.body.append(dialog);
  let returnFocus: HTMLElement | null = null, busy = false, generation = 0, needsReload = false;
  let preview: BackupPreview | null = null, rollback: { archive: string; filename: string } | null = null;
  function clear(): void { generation++; preview = null; rollback = null; dialog.replaceChildren(); }
  dialog.addEventListener('close', () => { clear(); if (needsReload) location.reload(); else if (returnFocus?.isConnected) returnFocus.focus(); });
  dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); });
  window.addEventListener('panel-session-expired', () => { if (dialog.open && !busy) dialog.close(); });
  document.addEventListener('visibilitychange', () => { if (document.hidden && dialog.open && !busy && !needsReload) dialog.close(); });
  window.addEventListener('hashchange', () => { if (dialog.open && !busy) dialog.close(); });

  return { open: () => {
    if (dialog.open) return;
    clear(); busy = false; needsReload = false; returnFocus = document.activeElement as HTMLElement;
    const ticket = generation;
    dialog.innerHTML = `<header class="dialog-heading"><h2 id="backup-title">${t('备份和恢复', 'Backup and restore')}</h2><button class="icon-button" data-close aria-label="${t('关闭备份和恢复', 'Close backup and restore')}"><i data-lucide="x" aria-hidden="true"></i></button></header><div class="backup-body">
      <p class="form-intro">${t('加密保存入站、客户端身份、Xray设置、累计流量和入站TLS证书。包含私钥，请保管文件及密码。管理员账号、登录会话、订阅链接密钥、核心版本和程序不包含在内；恢复会使现有所有订阅链接失效，之后需逐个重新生成。自定义设置引用的其他文件/Geo资源不随包迁移。未应用草稿首次导出会补齐并保存连接身份，不启动代理。', 'Encrypts inbounds, client identities, Xray settings, traffic totals and inbound TLS certificates. It contains private keys, so protect the file and password. The administrator account, sessions, subscription secrets, core versions and program are not included; restoring invalidates all existing subscription links, which must be regenerated one by one. Other files or Geo resources referenced by custom settings are not migrated. The first export of an unapplied draft completes and saves connection identities without starting a proxy.')}</p>
      <section aria-labelledby="export-title"><h3 id="export-title">${t('导出加密备份', 'Export encrypted backup')}</h3><label class="form-field" for="backup-password">${t('新备份密码', 'New backup password')}<input id="backup-password" type="password" autocomplete="new-password" maxlength="256" aria-describedby="backup-password-hint"><span class="form-hint" id="backup-password-hint">${t('12至256字节，不保存密码，遗忘无法恢复。', '12–256 bytes; the password is not stored and cannot be recovered.')}</span></label><label class="form-field" for="backup-password-repeat">${t('再次输入新备份密码', 'Repeat new backup password')}<input id="backup-password-repeat" type="password" autocomplete="new-password" maxlength="256"></label><button class="secondary-button" data-export>${t('导出并下载', 'Export and download')}</button></section>
      <section aria-labelledby="restore-title"><h3 id="restore-title">${t('从备份恢复', 'Restore from backup')}</h3><p class="form-hint">${t('先停止本项目Xray。恢复会替换全部业务草稿和身份，预览会列出覆盖/移除记录；不更换当前管理员，不启动代理。已有累计流量不会减少，备份后的未知用量无法补算。', 'Stop o-ui\'s Xray first. Restoring replaces all drafts and identities, and the preview lists overwritten/removed records; the administrator is unchanged and no proxy is started. Existing traffic totals never decrease, and usage after the backup cannot be recovered.')}</p><label class="form-field" for="backup-file">${t('加密备份文件', 'Encrypted backup file')}<input id="backup-file" type="file" accept=".ixpb,application/octet-stream"></label><label class="form-field" for="restore-password">${t('该文件的备份密码', 'Backup password for this file')}<input id="restore-password" type="password" autocomplete="off" maxlength="256"></label><button class="secondary-button" data-preview>${t('校验并预览', 'Verify and preview')}</button><div data-preview-content hidden></div>
      <label class="check-field" data-confirm-label hidden><input id="backup-confirm" type="checkbox">${t('我同意覆盖业务数据，并移除上方列出的记录；先保留加密回退副本。', 'I agree to overwrite the data and remove the records listed above; an encrypted rollback copy is kept first.')}</label></section>
      <p class="form-error" role="alert" tabindex="-1" hidden></p><p data-status role="status" tabindex="-1" class="form-hint"></p><button class="secondary-button" data-rollback hidden>${t('下载恢复前的回退副本', 'Download the pre-restore rollback copy')}</button><p class="form-hint" data-rollback-hint hidden>${t('回退副本使用上方“该文件的备份密码”加密，并已保存在本机运行目录。可用同一恢复流程导入；流量仍不会回退。关闭后重新加载数据。', 'The rollback copy is encrypted with the backup password above and saved in o-ui\'s runtime directory. Import it with the same restore flow; traffic totals still do not roll back. Data reloads after closing.')}</p></div><footer><button class="primary-button" data-restore hidden disabled>${t('确认覆盖并恢复', 'Confirm overwrite and restore')}</button><button class="secondary-button" data-close>${t('关闭', 'Close')}</button></footer>`;
    const exportPassword = dialog.querySelector<HTMLInputElement>('#backup-password')!;
    const repeat = dialog.querySelector<HTMLInputElement>('#backup-password-repeat')!;
    const restorePassword = dialog.querySelector<HTMLInputElement>('#restore-password')!;
    const file = dialog.querySelector<HTMLInputElement>('#backup-file')!;
    const confirm = dialog.querySelector<HTMLInputElement>('#backup-confirm')!;
    const content = dialog.querySelector<HTMLElement>('[data-preview-content]')!;
    const error = dialog.querySelector<HTMLElement>('[role="alert"]')!;
    const status = dialog.querySelector<HTMLElement>('[data-status]')!;
    const restore = dialog.querySelector<HTMLButtonElement>('[data-restore]')!;
    const confirmLabel = dialog.querySelector<HTMLElement>('[data-confirm-label]')!;
    function invalidate(): void { preview = null; confirm.checked = false; content.replaceChildren(); content.hidden = true; confirmLabel.hidden = true; restore.hidden = true; restore.disabled = true; }
    file.addEventListener('change', invalidate); restorePassword.addEventListener('input', invalidate);
    confirm.addEventListener('change', () => { restore.disabled = !confirm.checked || !preview || busy; });
    dialog.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => { if (!busy) dialog.close(); }));
    async function run(message: string, work: () => Promise<void>): Promise<void> {
      if (busy) return;
      busy = true; error.hidden = true; status.textContent = message;
      const controls = [...dialog.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input, button')];
      const disabled = controls.map(control => control.disabled); controls.forEach(control => { control.disabled = true; });
      try { await work(); }
      catch (reason) { if (dialog.open && ticket === generation) { error.textContent = (reason as Error).message; error.hidden = false; status.textContent = ''; error.focus(); } }
      finally {
        busy = false;
        if (dialog.open && ticket === generation) {
          controls.forEach((control, index) => { control.disabled = disabled[index]; });
          restore.disabled = !preview || !confirm.checked || needsReload;
          if (needsReload) dialog.querySelectorAll<HTMLInputElement | HTMLButtonElement>('section input, section button').forEach(control => { control.disabled = true; });
          if (error.hidden) { if (preview && !needsReload) confirm.focus(); else status.focus(); }
        }
      }
    }
    dialog.querySelector('[data-export]')!.addEventListener('click', () => { void run(t('正在加密并生成备份…', 'Encrypting and creating the backup…'), async () => {
      validateBackupPassword(exportPassword.value);
      if (exportPassword.value !== repeat.value) throw new Error(t('两次新备份密码不一致。', 'The two backup passwords do not match.'));
      const result = await api<{ archive: string; filename: string }>('backup/export', jsonBody('POST', { password: exportPassword.value }));
      if (!dialog.open || ticket !== generation) return;
      download(result.archive, result.filename); exportPassword.value = ''; repeat.value = ''; status.textContent = t('已生成加密文件并发起下载，请确认文件已保存并妥善保管密码。', 'The encrypted file was created and the download started; make sure it is saved and keep the password safe.');
    }); });
    async function requestFile(): Promise<{ archive: string; password: string }> {
      validateBackupPassword(restorePassword.value);
      const selected = file.files?.[0];
      if (!selected || selected.size === 0 || selected.size > maxBackupFile) throw new Error(t('请选择有效的加密备份文件（最大约8 MiB）。', 'Choose a valid encrypted backup file (up to about 8 MiB).'));
      return { archive: encodeBackup(new Uint8Array(await selected.arrayBuffer())), password: restorePassword.value };
    }
    dialog.querySelector('[data-preview]')!.addEventListener('click', () => { void run(t('正在解密、校验并核对覆盖范围…', 'Decrypting, verifying and checking what will be overwritten…'), async () => {
      invalidate();
      preview = await api<BackupPreview>('backup/preview', jsonBody('POST', await requestFile()));
      if (!dialog.open || ticket !== generation) return;
      const list = (title: string, records: BackupRecord[]) => `<details><summary>${title}${t('（', ' (')}${records.length}${t('）', ')')}</summary>${records.length ? `<ul>${records.map(record => `<li>${esc(record.name)} <small>${esc(record.id)}</small></li>`).join('')}</ul>` : t('<p class="form-hint">无</p>', '<p class="form-hint">None</p>')}</details>`;
      content.innerHTML = `<h4>${t('恢复预览', 'Restore preview')}</h4><p>${t('备份时间：', 'Backup time: ')}${esc(new Date(preview.createdAt).toLocaleString(locale))}</p><p>${t('将导入', 'Will import')} ${preview.inbounds} ${t('条入站、', ' inbounds, ')}${preview.clients} ${t('个客户端、', ' clients, ')}${preview.certificates} ${t('组证书。', ' certificate sets.')}</p>${list(t('将覆盖的当前入站', 'Current inbounds to overwrite'), preview.replacedInbounds)}${list(t('将覆盖的当前客户端', 'Current clients to overwrite'), preview.replacedClients)}${list(t('将移除的当前入站', 'Current inbounds to remove'), preview.removedInbounds)}${list(t('将移除的当前客户端', 'Current clients to remove'), preview.removedClients)}<p class="form-hint">${t('覆盖包含启停、额度、到期、关联和身份等字段，不仅是名称。回退副本与该文件使用相同备份密码；保留本机副本，不自动删除。', 'Overwriting covers enablement, quota, expiry, links and identities, not just names. The rollback copy uses the same backup password as this file; the local copy is kept and never deleted automatically.')}</p>`;
      content.hidden = false; confirmLabel.hidden = false; restore.hidden = false; status.textContent = t('校验通过，尚未修改当前数据。请逐项核对覆盖及移除清单。', 'Verification passed; current data is unchanged. Review the overwrite and removal lists item by item.'); confirm.focus();
    }); });
    restore.addEventListener('click', () => { void run(t('正在保存加密回退副本并事务恢复，请勿关闭…', 'Saving the encrypted rollback copy and restoring in a transaction; do not close…'), async () => {
      if (!preview || !confirm.checked) throw new Error(t('请先预览并明确确认覆盖范围。', 'Preview first and explicitly confirm what will be overwritten.'));
      const result = await api<{ rollbackFile: string; rollbackArchive: string; message: string }>('backup/restore', jsonBody('POST', { ...await requestFile(), fingerprint: preview.fingerprint, confirmation: '确认覆盖并恢复' }));
      if (!dialog.open || ticket !== generation) return;
      needsReload = true; rollback = { archive: result.rollbackArchive, filename: result.rollbackFile };
      exportPassword.value = ''; repeat.value = ''; restorePassword.value = ''; file.value = ''; invalidate();
      status.textContent = `${result.message}${t('。本机回退副本：', '. Local rollback copy: ')}${result.rollbackFile}`; status.focus();
      dialog.querySelector<HTMLElement>('[data-rollback]')!.hidden = false; dialog.querySelector<HTMLElement>('[data-rollback-hint]')!.hidden = false;
      dialog.querySelector('footer [data-close]')!.textContent = t('关闭并重新加载', 'Close and reload');
    }); });
    dialog.querySelector('[data-rollback]')!.addEventListener('click', () => { if (rollback) download(rollback.archive, rollback.filename); });
    refreshIcons(); dialog.showModal(); exportPassword.focus();
  } };
}
