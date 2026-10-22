import { t } from './i18n.ts';

// Telegram通知机器人设置（参照OUI的Telegram标签）：令牌与备份口令只写不读，留空表示保持已保存的值。
export interface TelegramView { enabled: boolean; chatIds: string; reportTime: string; notifyLogin: boolean; cpuThreshold: number; backupEnabled: boolean; tokenSet: boolean; backupPasswordSet: boolean }
export interface TelegramStatus { revision: number; data: TelegramView }
export interface TelegramForm { tgEnabled: boolean; tgToken: string; tgChatIds: string; tgReportTime: string; tgNotifyLogin: boolean; tgCpuThreshold: string; tgBackupEnabled: boolean; tgBackupPassword: string }
export interface TelegramRequest { enabled: boolean; token: string; chatIds: string; reportTime: string; notifyLogin: boolean; cpuThreshold: number; backupEnabled: boolean; backupPassword: string }

export function telegramForm(view: TelegramView): TelegramForm {
  return { tgEnabled: view.enabled, tgToken: '', tgChatIds: view.chatIds, tgReportTime: view.reportTime, tgNotifyLogin: view.notifyLogin, tgCpuThreshold: String(view.cpuThreshold), tgBackupEnabled: view.backupEnabled, tgBackupPassword: '' };
}

export function telegramRequest(form: TelegramForm, view: TelegramView): TelegramRequest {
  const token = form.tgToken.trim(), chats = form.tgChatIds.split(',').map(item => item.trim()).filter(Boolean), threshold = Number(form.tgCpuThreshold.trim() || '0');
  if (token && !/^[0-9]{5,16}:[A-Za-z0-9_-]{30,64}$/.test(token)) throw new Error(t('Bot Token 格式无效（应为“数字:字母数字串”，从 @BotFather 获取）', 'Invalid bot token (it looks like digits:letters, from @BotFather)'));
  if (chats.length > 10 || !chats.every(chat => /^-?[0-9]{1,20}$/.test(chat))) throw new Error(t('Chat ID 只能是数字（群组以 - 开头），多个用英文逗号分隔，最多 10 个', 'Chat IDs must be numbers (groups start with -), comma separated, at most 10'));
  if (form.tgReportTime && !/^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(form.tgReportTime)) throw new Error(t('每日报告时间格式应为 HH:MM，留空表示不发送', 'Daily report time must be HH:MM, or empty to disable'));
  if (!Number.isInteger(threshold) || threshold < 0 || threshold > 100) throw new Error(t('CPU 提醒阈值须为 0 至 100（0 表示关闭）', 'CPU alert threshold must be 0–100 (0 turns it off)'));
  if (form.tgEnabled && (!(token || view.tokenSet) || !chats.length)) throw new Error(t('启用机器人须填写 Bot Token 和至少一个 Chat ID', 'Enabling the bot needs a bot token and at least one chat ID'));
  if (form.tgBackupEnabled && (!(form.tgBackupPassword || view.backupPasswordSet) || !form.tgReportTime)) throw new Error(t('发送备份须设置备份加密口令和每日报告时间', 'Sending backups needs a backup password and a daily report time'));
  return { enabled: form.tgEnabled, token, chatIds: chats.join(','), reportTime: form.tgReportTime, notifyLogin: form.tgNotifyLogin, cpuThreshold: threshold, backupEnabled: form.tgBackupEnabled, backupPassword: form.tgBackupPassword };
}
