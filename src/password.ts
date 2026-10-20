import { t } from './i18n.ts';

export function passwordChangeError(current: string, next: string, confirm: string, username = ''): string {
  if (!current) return t('请输入当前密码。', 'Enter the current password.');
  if (username && !/^[a-zA-Z0-9_.-]{3,64}$/.test(username)) return t('用户名需为3至64位字母、数字、点、下划线或短横线。', 'Username must be 3–64 letters, digits, dots, underscores or hyphens.');
  if (!next && !confirm) return username ? '' : t('请填写新用户名或新密码。', 'Enter a new username or a new password.');
  if (next !== confirm) return t('两次输入的新密码不一致。', 'The two new passwords do not match.');
  const length = new TextEncoder().encode(next).length;
  if (length < 6 || length > 72) return t('新密码需为6至72字节，中文字符会占多个字节。', 'The new password must be 6–72 bytes; non-ASCII characters take several bytes.');
  if (current === next) return t('新密码不能与当前密码相同。', 'The new password must differ from the current one.');
  return '';
}
