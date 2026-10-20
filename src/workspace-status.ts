import type { RuntimeStatus } from './runtime.ts';
import { t } from './i18n.ts';

export function workspaceStatusText(persistent: boolean, revision: number, runtime: RuntimeStatus | null, at: number, now = Date.now()): string {
  if (!persistent) return t('离线会话草稿，不应用核心。', 'Offline session draft; the core is not applied.');
  if (!runtime) return t('核心运行状态未连接，无法确认入站和客户端草稿是否已应用。', 'Core status is unavailable; cannot tell whether the inbound and client draft is applied.');
  const age = now - at;
  if (!Number.isFinite(age) || age < -5000 || age > 10000) return t('核心运行状态已过期，无法确认入站和客户端草稿是否已应用。', 'Core status is stale; cannot tell whether the inbound and client draft is applied.');
  if (!runtime.running) return runtime.unexpectedExit ? t('核心异常退出，入站和客户端配置当前未在运行；请检查后重新应用。', 'The core exited unexpectedly; inbounds and clients are not running. Check and apply again.') : t('核心已停止，入站和客户端配置当前未在运行。', 'The core is stopped; inbounds and clients are not running.');
  const version = runtime.version ?? t('未知', 'unknown');
  const running = t(`Xray v${version} · 入站/客户端修订${runtime.workspaceRevision}`, `Xray v${version} · inbound/client revision ${runtime.workspaceRevision}`);
  if (revision === runtime.workspaceRevision) return t(`已保存入站/客户端修订${revision}当前已应用（${running}）。`, `Saved inbound/client revision ${revision} is applied (${running}).`);
  if (revision < runtime.workspaceRevision) return t(`页面入站/客户端修订${revision}已落后于运行配置（${running}），请重新读取已保存草稿后核对。`, `This page's inbound/client revision ${revision} is behind the running configuration (${running}); reload the saved draft and check.`);
  return t(`已保存入站/客户端修订${revision}待应用；当前运行${running}。`, `Saved inbound/client revision ${revision} is pending; running ${running}.`);
}
