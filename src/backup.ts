import { api, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { decodeBackup, encodeBackup, isOUIBackup, maxBackupFile } from './backup-model.ts';
import type { PublicInfo } from './server-plan-model.ts';
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
      <p class="form-intro">${t('保存入站、客户端身份、Xray设置、累计流量和入站TLS证书（填写口令时加密）。包含私钥，请妥善保管文件（及口令）。管理员账号、登录会话、订阅链接密钥、核心版本和程序不包含在内；恢复会使现有所有订阅链接失效，之后需逐个重新生成。自定义设置引用的其他文件/Geo资源不随包迁移。未应用草稿首次导出会补齐并保存连接身份，不启动代理。', 'Saves inbounds, client identities, Xray settings, traffic totals and inbound TLS certificates (encrypted when you set a password). It contains private keys, so protect the file (and password). The administrator account, sessions, subscription secrets, core versions and program are not included; restoring invalidates all existing subscription links, which must be regenerated one by one. Other files or Geo resources referenced by custom settings are not migrated. The first export of an unapplied draft completes and saves connection identities without starting a proxy.')}</p>
      <section aria-labelledby="export-title"><h3 id="export-title">${t('导出备份', 'Export backup')}</h3><label class="form-field" for="backup-password">${t('备份口令（可选）', 'Backup password (optional)')}<input id="backup-password" type="password" autocomplete="new-password" aria-describedby="backup-password-hint"><span class="form-hint" id="backup-password-hint">${t('留空则导出不加密的 .json 文件，含私钥请妥善保管；填写则加密为 .ixpb，不保存口令，遗忘无法恢复。', 'Leave empty for an unencrypted .json file that contains private keys, so keep it safe; fill it in for an encrypted .ixpb file. The password is not stored and cannot be recovered.')}</span></label><label class="form-field" for="backup-password-repeat">${t('再次输入口令', 'Repeat the password')}<input id="backup-password-repeat" type="password" autocomplete="new-password"></label><button class="secondary-button" data-export>${t('导出并下载', 'Export and download')}</button></section>
      <section aria-labelledby="restore-title"><h3 id="restore-title">${t('从备份恢复', 'Restore from backup')}</h3><p class="form-hint">${t('先停止本项目Xray。恢复会替换全部业务草稿和身份，预览会列出覆盖/移除记录；不更换当前管理员，不启动代理。已有累计流量不会减少，备份后的未知用量无法补算。', 'Stop o-ui\'s Xray first. Restoring replaces all drafts and identities, and the preview lists overwritten/removed records; the administrator is unchanged and no proxy is started. Existing traffic totals never decrease, and usage after the backup cannot be recovered.')}${t('也可以直接选 OUI／3x-ui 面板导出的数据库备份（x-ui.db），导入其中的入站、客户端和累计流量，原有节点链接继续可用。', ' You can also choose the database backup (x-ui.db) exported by an OUI/3x-ui panel to import its inbounds, clients and traffic totals; existing node links keep working.')}</p><label class="form-field" for="backup-file">${t('备份文件（.ixpb、.json 或 OUI 的 x-ui.db）', 'Backup file (.ixpb, .json or OUI x-ui.db)')}<input id="backup-file" type="file" accept=".ixpb,.json,.db,application/octet-stream,application/json"></label><label class="form-field" for="restore-share-host" data-share-host hidden>${t('分享主机', 'Share host')}<input id="restore-share-host" maxlength="253" autocomplete="off" aria-describedby="restore-share-host-hint"><span class="form-hint" id="restore-share-host-hint">${t('写进导入节点的链接地址，默认本机公网 IP，也可填直接解析到本机的域名。只影响 o-ui 新生成的链接，旧客户端不用改。', 'Server address for the links of imported nodes; defaults to this server\'s public IP, or use a domain that resolves directly to it. It only affects links o-ui generates; existing clients need no change.')}</span></label><label class="form-field" for="restore-password" data-restore-password>${t('该文件的备份口令（加密备份才需要）', 'Backup password for this file (encrypted backups only)')}<input id="restore-password" type="password" autocomplete="off"></label><button class="secondary-button" data-preview>${t('校验并预览', 'Verify and preview')}</button><div data-preview-content hidden></div>
      <label class="check-field" data-confirm-label hidden><input id="backup-confirm" type="checkbox">${t('我同意覆盖业务数据，并移除上方列出的记录；先保留回退副本。', 'I agree to overwrite the data and remove the records listed above; a rollback copy is kept first.')}</label></section>
      <p class="form-error" role="alert" tabindex="-1" hidden></p><p data-status role="status" tabindex="-1" class="form-hint"></p><button class="secondary-button" data-rollback hidden>${t('下载恢复前的回退副本', 'Download the pre-restore rollback copy')}</button><p class="form-hint" data-rollback-hint hidden>${t('回退副本用上方填写的口令加密（未填则不加密），并已保存在本机运行目录。可用同一恢复流程导入；流量仍不会回退。关闭后重新加载数据。', 'The rollback copy uses the password above (unencrypted if empty) and is saved in o-ui\'s runtime directory. Import it with the same restore flow; traffic totals still do not roll back. Data reloads after closing.')}</p></div><footer><button class="primary-button" data-restore hidden disabled>${t('确认覆盖并恢复', 'Confirm overwrite and restore')}</button><button class="secondary-button" data-close>${t('关闭', 'Close')}</button></footer>`;
    const exportPassword = dialog.querySelector<HTMLInputElement>('#backup-password')!;
    const repeat = dialog.querySelector<HTMLInputElement>('#backup-password-repeat')!;
    const restorePassword = dialog.querySelector<HTMLInputElement>('#restore-password')!;
    const shareHost = dialog.querySelector<HTMLInputElement>('#restore-share-host')!;
    const file = dialog.querySelector<HTMLInputElement>('#backup-file')!;
    const confirm = dialog.querySelector<HTMLInputElement>('#backup-confirm')!;
    const content = dialog.querySelector<HTMLElement>('[data-preview-content]')!;
    const error = dialog.querySelector<HTMLElement>('[role="alert"]')!;
    const status = dialog.querySelector<HTMLElement>('[data-status]')!;
    const restore = dialog.querySelector<HTMLButtonElement>('[data-restore]')!;
    const confirmLabel = dialog.querySelector<HTMLElement>('[data-confirm-label]')!;
    function invalidate(): void { preview = null; confirm.checked = false; content.replaceChildren(); content.hidden = true; confirmLabel.hidden = true; restore.hidden = true; restore.disabled = true; }
    file.addEventListener('change', () => { invalidate(); void detectOUI(); }); restorePassword.addEventListener('input', invalidate); shareHost.addEventListener('input', invalidate);
    // 选中 OUI 数据库时改问分享主机（预填本机公网 IP），不需要备份口令。
    async function detectOUI(): Promise<void> {
      const selected = file.files?.[0];
      const oui = !!selected && isOUIBackup(new Uint8Array(await selected.slice(0, 16).arrayBuffer()));
      if (!dialog.open || ticket !== generation || selected !== file.files?.[0]) return;
      dialog.querySelector<HTMLElement>('[data-share-host]')!.hidden = !oui; dialog.querySelector<HTMLElement>('[data-restore-password]')!.hidden = oui;
      if (!oui || shareHost.value) return;
      const info = await api<PublicInfo>('system/public').catch(() => null);
      if (dialog.open && ticket === generation && !shareHost.value) shareHost.value = info?.ipv4?.ip || info?.ipv6?.ip || '';
    }
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
    dialog.querySelector('[data-export]')!.addEventListener('click', () => { void run(t('正在生成备份…', 'Creating the backup…'), async () => {
      if (exportPassword.value !== repeat.value) throw new Error(t('两次新备份密码不一致。', 'The two backup passwords do not match.'));
      const result = await api<{ archive: string; filename: string }>('backup/export', jsonBody('POST', { password: exportPassword.value }));
      if (!dialog.open || ticket !== generation) return;
      const encrypted = exportPassword.value !== '';
      download(result.archive, result.filename); exportPassword.value = ''; repeat.value = ''; status.textContent = encrypted ? t('已生成加密文件并发起下载，请确认文件已保存并妥善保管口令。', 'The encrypted file was created and the download started; make sure it is saved and keep the password safe.') : t('已生成不加密的备份并发起下载；文件含私钥，请妥善保管。', 'An unencrypted backup was created and the download started; it contains private keys, so keep it safe.');
    }); });
    async function requestFile(): Promise<{ archive: string; password: string; shareHost?: string }> {
      const selected = file.files?.[0];
      if (!selected || selected.size === 0 || selected.size > maxBackupFile) throw new Error(t('请选择有效的备份文件（最大约8 MiB）。', 'Choose a valid backup file (up to about 8 MiB).'));
      const data = new Uint8Array(await selected.arrayBuffer());
      return isOUIBackup(data) ? { archive: encodeBackup(data), password: '', shareHost: shareHost.value.trim() } : { archive: encodeBackup(data), password: restorePassword.value };
    }
    dialog.querySelector('[data-preview]')!.addEventListener('click', () => { void run(t('正在校验并核对覆盖范围…', 'Verifying and checking what will be overwritten…'), async () => {
      invalidate();
      preview = await api<BackupPreview>('backup/preview', jsonBody('POST', await requestFile()));
      if (!dialog.open || ticket !== generation) return;
      const list = (title: string, records: BackupRecord[]) => `<details><summary>${title}${t('（', ' (')}${records.length}${t('）', ')')}</summary>${records.length ? `<ul>${records.map(record => `<li>${esc(record.name)} <small>${esc(record.id)}</small></li>`).join('')}</ul>` : t('<p class="form-hint">无</p>', '<p class="form-hint">None</p>')}</details>`;
      const fromOUI = preview.source === 'oui', notes = preview.notes ?? [];
      const heading = fromOUI ? `<h4>${t('从 OUI 导入预览', 'OUI import preview')}</h4><p class="form-hint">${t('导入入站、客户端身份（UUID、HY2 认证值、REALITY 私钥／shortId／SNI）、到期、IP 上限和累计流量；OUI 的面板设置、管理员、Telegram 和路由不导入，o-ui 当前的 Xray 设置保持不变。', 'Imports inbounds, client identities (UUID, HY2 auth, REALITY private key/shortId/SNI), expiry, IP limits and traffic totals. OUI panel settings, administrator, Telegram and routing are not imported; o-ui\'s current Xray settings stay unchanged.')}</p>${notes.length ? `<details open><summary>${t('导入说明', 'Import notes')}${t('（', ' (')}${notes.length}${t('）', ')')}</summary><ul>${notes.map(note => `<li>${esc(note)}</li>`).join('')}</ul></details>` : ''}` : `<h4>${t('恢复预览', 'Restore preview')}</h4><p>${t('备份时间：', 'Backup time: ')}${esc(new Date(preview.createdAt).toLocaleString(locale))}</p>`;
      content.innerHTML = `${heading}<p>${t('将导入', 'Will import')} ${preview.inbounds} ${t('条入站、', ' inbounds, ')}${preview.clients} ${t('个客户端、', ' clients, ')}${preview.certificates} ${t('组证书。', ' certificate sets.')}</p>${list(t('将覆盖的当前入站', 'Current inbounds to overwrite'), preview.replacedInbounds)}${list(t('将覆盖的当前客户端', 'Current clients to overwrite'), preview.replacedClients)}${list(t('将移除的当前入站', 'Current inbounds to remove'), preview.removedInbounds)}${list(t('将移除的当前客户端', 'Current clients to remove'), preview.removedClients)}<p class="form-hint">${t('覆盖包含启停、额度、到期、关联和身份等字段，不仅是名称。回退副本使用上方口令（未填则不加密）；保留本机副本，不自动删除。', 'Overwriting covers enablement, quota, expiry, links and identities, not just names. The rollback copy uses the password above (unencrypted if empty); the local copy is kept and never deleted automatically.')}</p>`;
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
