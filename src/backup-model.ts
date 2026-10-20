import { t } from './i18n.ts';
export const maxBackupFile = (8 << 20) + 64;
export type BackupRecord = { id: string; name: string };
export interface BackupPreview {
  fingerprint: string; createdAt: string; inbounds: number; clients: number; certificates: number;
  replacedInbounds: BackupRecord[]; replacedClients: BackupRecord[];
  removedInbounds: BackupRecord[]; removedClients: BackupRecord[];
}

export function validateBackupPassword(password: string): void {
  const length = new TextEncoder().encode(password).length;
  if (length < 12 || length > 256) throw new Error(t('备份密码需为12至256字节；请妥善保存，遗忘无法恢复。', 'Backup password must be 12–256 bytes; keep it safe, it cannot be recovered.'));
}

export function encodeBackup(data: Uint8Array): string {
  if (!data.length || data.length > maxBackupFile) throw new Error(t('请选择有效的备份文件（最大约8 MiB）。', 'Choose a valid backup file (up to about 8 MiB).'));
  let binary = '';
  for (let offset = 0; offset < data.length; offset += 8192) binary += String.fromCharCode(...data.subarray(offset, offset + 8192));
  return btoa(binary);
}

export function decodeBackup(encoded: string): Uint8Array<ArrayBuffer> {
  if (encoded.length > Math.ceil(maxBackupFile / 3) * 4) throw new Error(t('返回的备份超过大小上限。', 'The returned backup exceeds the size limit.'));
  const binary = atob(encoded);
  if (!binary.length || binary.length > maxBackupFile) throw new Error(t('返回的备份文件无效。', 'The returned backup file is invalid.'));
  return Uint8Array.from(binary, character => character.charCodeAt(0));
}
