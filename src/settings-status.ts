import type { RuntimeStatus } from './runtime.ts';
import { t } from './i18n.ts';

export function settingsStatusText(persistent: boolean, revision: number, runtime: RuntimeStatus | null, at: number, now = Date.now()): string {
  if (!persistent) return t('离线会话草稿，不应用核心。', 'Offline session draft; the core is not applied.');
  if (!runtime) return t('核心运行状态未连接，无法确认设置是否生效。', 'Core status is unavailable; cannot tell whether the settings are in effect.');
  const age = now - at;
  if (!Number.isFinite(age) || age < -5000 || age > 10000) return t('核心运行状态已过期，无法确认设置是否生效。', 'Core status is stale; cannot tell whether the settings are in effect.');
  if (!runtime.running) return runtime.unexpectedExit ? t('核心异常退出，设置当前未在运行；请检查后在首页点“重启”重新应用。', 'The core exited unexpectedly; the settings are not running. Check, then click Restart on the dashboard to apply again.') : t('核心已停止，设置当前未在运行。', 'The core is stopped; the settings are not running.');
  const version = runtime.version ?? t('未知', 'unknown');
  const running = t(`Xray v${version} · 设置修订${runtime.settingsRevision}`, `Xray v${version} · settings revision ${runtime.settingsRevision}`);
  if (revision === runtime.settingsRevision) return t(`已保存设置修订${revision}当前已生效（${running}）。`, `Saved settings revision ${revision} is in effect (${running}).`);
  if (revision < runtime.settingsRevision) return t(`页面设置修订${revision}已落后于运行设置（${running}），请刷新核对最新草稿。`, `This page's settings revision ${revision} is behind the running settings (${running}); refresh and check the latest draft.`);
  return t(`已保存设置修订${revision}待应用；当前运行${running}。`, `Saved settings revision ${revision} is pending; running ${running}.`);
}
