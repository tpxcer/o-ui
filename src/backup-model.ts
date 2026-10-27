import { t } from './i18n.ts';
export const maxBackupFile = (8 << 20) + 64;
export type BackupRecord = { id: string; name: string };
export interface BackupPreview {
  fingerprint: string; createdAt: string; inbounds: number; clients: number; certificates: number;
  source?: 'oui'; notes?: string[];
  replacedInbounds: BackupRecord[]; replacedClients: BackupRecord[];
  removedInbounds: BackupRecord[]; removedClients: BackupRecord[];
}

// OUI（3x-ui系）的备份是整个 x-ui.db，按 SQLite 文件头识别。
const sqliteHeader = 'SQLite format 3\0';
export const isOUIBackup = (data: Uint8Array): boolean => data.length >= 16 && [...sqliteHeader].every((character, index) => data[index] === character.charCodeAt(0));

export function encodeBackup(data: Uint8Array): string {
  if (!data.length || data.length > maxBackupFile) throw new Error(t('请选择有效的备份文件（最大约 8 MB）。', 'Choose a valid backup file (up to about 8 MB).'));
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
