import { escapeHtml as esc } from './workspace.ts';
import { locale, t } from './i18n.ts';

export type LogSource = 'all' | 'panel' | 'xray';
export type LogLevel = 'all' | 'info' | 'warning' | 'error';
export type LogEntry = { sequence: number; at: string; source: 'panel' | 'xray'; level: Exclude<LogLevel, 'all'>; code: string; message: string };
export type LogSnapshot = { since: string; limit: number; dropped: number; entries: LogEntry[] };
export const levelNames = { info: t('信息', 'Info'), warning: t('警告', 'Warning'), error: t('错误', 'Error') };

export function logTime(at: string): string {
  return Number.isFinite(Date.parse(at)) ? new Date(at).toLocaleString(locale, { hour12: false }) : t('时间未知', 'Unknown time');
}

export function logRows(snapshot: LogSnapshot, source: LogSource, level: LogLevel): string {
  const entries = snapshot.entries.filter(entry => (source === 'all' || entry.source === source) && (level === 'all' || entry.level === level));
  if (!entries.length) return t('<p class="logs-empty">当前筛选下暂无记录。核心日志关闭、写入文件或无可识别错误时，可能没有核心诊断。</p>', '<p class="logs-empty">No entries for the current filter. Core diagnostics may be absent when core logging is off, written to a file, or no recognizable errors occurred.</p>');
  return [...entries].reverse().map(entry => `<article class="log-entry"><div class="log-meta"><time>${esc(logTime(entry.at))}</time><span>${entry.source === 'panel' ? t('面板', 'Panel') : 'Xray'}</span><span class="log-level log-${['info', 'warning', 'error'].includes(entry.level) ? entry.level : 'info'}">${esc(levelNames[entry.level] ?? t('未知', 'Unknown'))}</span><span>#${esc(String(entry.sequence))}</span></div><p>${esc(entry.message)}</p></article>`).join('');
}
