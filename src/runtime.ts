import { api, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import type { CoreStatus } from './core.ts';
import { isLoopbackListen } from './proxy-endpoint.ts';
import { coreCompatibilityWarning } from './core-compatibility.ts';
import type { Workspace } from './workspace.ts';
import { locale, t } from './i18n.ts';

export interface RuntimeStatus {
  running: boolean; version?: string; workspaceRevision: number; settingsRevision: number; inbounds: number; startedAt?: string;
  unexpectedExit?: boolean; exitedAt?: string;
}
// 参照OUI：一键按已保存配置重启（停止时即启动），或直接停止Xray。
export async function applySavedConfig(): Promise<RuntimeStatus> { return api<RuntimeStatus>('runtime/apply-saved', jsonBody('POST', {})); }
export async function stopCore(): Promise<RuntimeStatus> { return api<RuntimeStatus>('runtime/stop', jsonBody('POST', {})); }
export function runtimeStatusText(status: RuntimeStatus | null): string {
  if (!status) return t('核心运行状态未连接', 'Core status unavailable');
  if (status.running) return `${t('运行中 v', 'Running v')}${status.version ?? ''} · ${status.inbounds} ${t('条代理入站', 'proxy inbounds')}`;
  return status.unexpectedExit ? t('Xray 异常退出 · 未自动重启，请检查后重新应用', 'Xray exited unexpectedly · not restarted automatically; check and apply again') : t('Xray 已停止', 'Xray stopped');
}
export interface RuntimeHistory {
  revision: number; available: boolean; version?: string; workspaceRevision: number; settingsRevision: number; inbounds: number; appliedAt?: string; issue?: string; nonLoopbackProxy?: boolean;
}
export function runtimeHistoryText(history: RuntimeHistory | null): string {
  if (!history) return t('运行历史未读取，不能恢复。', 'Run history not loaded; cannot restore.');
  if (history.issue) return history.issue;
  if (!history.appliedAt) return t('尚无成功应用记录。', 'Nothing has been applied yet.');
  const date = new Date(history.appliedAt);
  const time = Number.isNaN(date.getTime()) ? t('时间未知', 'Unknown time') : date.toLocaleString(locale, { hour12: false });
  return `${t('上次应用：v', 'Last applied: v')}${history.version ?? ''} ${t('· 入站修订', '· inbound revision')} ${history.workspaceRevision}${t('／设置修订', ' / settings revision')} ${history.settingsRevision} · ${history.inbounds} ${t('条入站 ·', 'inbounds ·')} ${time}${history.available ? '' : t('；无可恢复入站', '; no restorable inbounds')}`;
}
type Snapshot = { version: string; workspaceRevision: number; settingsRevision: number };

export function initRuntime(onStatus: (status: RuntimeStatus | null, at: number) => void): { open: (action?: 'apply' | 'stop' | 'restart') => void; refresh: () => Promise<void> } {
  let snapshot: Snapshot | null = null, status: RuntimeStatus | null = null;
  let history: RuntimeHistory | null = null;
  let busy = false, confirmed = false, message = '', error = '', action: 'apply' | 'stop' | 'resume' | 'restart' = 'apply';
  let restartSnapshot: { revision: number; startedAt: string } | null = null;
  let draftNonLoopback = false, proxyConfirmed = false;
  let statusRequest = 0, statusAccepted = 0;
  const dialog = document.createElement('dialog');
  dialog.className = 'edit-dialog'; dialog.setAttribute('aria-labelledby', 'runtime-title');
  document.body.append(dialog);
  let returnFocus: HTMLElement | null = null;
  dialog.addEventListener('close', () => { if (returnFocus?.isConnected && returnFocus.getClientRects().length) returnFocus.focus(); });
  dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); });
  const canRestart = () => !!restartSnapshot && !!history?.available && history.revision === restartSnapshot.revision && !!status?.running && status.startedAt === restartSnapshot.startedAt;
  const needsProxyConfirmation = () => action !== 'stop' && (action === 'apply' ? draftNonLoopback : !!history?.nonLoopbackProxy);
  function paint(): void {
    const compatibility = action === 'stop' ? '' : coreCompatibilityWarning(action === 'apply' ? snapshot?.version : history?.version);
    dialog.innerHTML = `<header class="dialog-heading"><h2 id="runtime-title">${action === 'stop' ? t('停止 Xray', 'Stop Xray') : action === 'resume' ? t('恢复上次已应用配置', 'Restore last applied configuration') : action === 'restart' ? t('重启当前已应用配置', 'Restart applied configuration') : t('预检与应用 Xray', 'Preflight and apply Xray')}</h2><button class="icon-button" id="runtime-close" aria-label="${t('关闭核心操作', 'Close core actions')}" ${busy ? 'disabled' : ''}>×</button></header><div class="edit-body">
      <p class="core-warning">${t('应用会重启 Xray，现有连接会短暂中断；预检或启动失败时保留并尝试恢复原来的配置。', 'Applying restarts Xray and briefly interrupts existing connections; if the preflight or start fails, the previous configuration is kept and restored.')}</p>
      <p>${esc(runtimeStatusText(status))}${''}</p>
      ${compatibility ? `<p class="core-warning" role="note">${esc(compatibility)}</p>` : ''}
      ${action === 'apply' && snapshot ? `<p class="form-intro">${t('本次使用：v', 'This run uses v')}${esc(snapshot.version)} ${t('· 入站修订', '· inbound revision')} ${snapshot.workspaceRevision}${t('／设置修订', ' / settings revision')} ${snapshot.settingsRevision}${t('。只应用已保存内容，不会自动保存其他页面未提交的修改。', '. Only saved content is applied; unsubmitted changes on other pages are not saved.')}</p><p class="form-hint">${t('官方预检后启动自有子进程。非零额度需开启统计，约2秒采样，可能超额；到期/用尽时重载会中断同核心其他用户。异常退出可能存在未采集尾流。', 'After the official preflight, o-ui starts its own child process. Non-zero quotas need statistics on; sampling runs about every 2 seconds, so quotas can be exceeded slightly. Reloading on expiry or exhaustion interrupts other users of the same core. Traffic just before an unexpected exit may not be counted.')}</p>` : ''}
      ${action !== 'stop' ? `<p class="form-intro">${esc(runtimeHistoryText(history))}</p>` : ''}
      ${action === 'apply' && history?.available ? `<button class="secondary-button" id="runtime-resume" ${busy || !status || status.running ? 'disabled' : ''}>${t('恢复上次已应用配置…', 'Restore last applied configuration…')}</button><p class="form-hint">${t('需先停止本项目核心。恢复不会应用较新的草稿；面板重启时仅在上次退出前核心仍在运行（未手动停止）才自动恢复上次应用。', 'Stop o-ui\'s core first. Restoring never applies newer drafts; when the panel restarts, the last apply is restored automatically only if the core was still running (not stopped manually) at exit.')}</p>` : ''}
      ${action === 'resume' ? `<p class="core-warning">${t('将使用上述历史的精确核心版本、配置及身份，不使用当前选择的版本或较新草稿。重新预检，并按当前时间和累计流量剔除已到期或用尽客户端；不会回退用量。历史文件或证书无效时拒绝恢复。', 'Uses the exact core version, configuration and identities from the history above, not the selected version or newer drafts. The preflight runs again and clients that are expired or out of quota are removed; usage is never rolled back. Restoring is refused if the history files or certificates are invalid.')}</p><label class="check-field"><input id="runtime-confirm" type="checkbox" ${confirmed ? 'checked' : ''} ${busy ? 'disabled' : ''}>${t('我确认恢复上次已应用配置，不应用当前草稿', 'I confirm restoring the last applied configuration instead of the current draft')}</label><button class="secondary-button" id="runtime-back" ${busy ? 'disabled' : ''}>${t('返回应用当前已保存配置', 'Back to applying the saved configuration')}</button>` : ''}
      ${action === 'restart' ? `<p class="core-warning">${t('只重启当前已应用的精确版本、配置和身份，不应用较新草稿或另选核心。先保存当前用量、重新预检，并剔除已到期或用尽的客户端；全部受限时停止核心。连接会中断；预检失败不停止旧核心，启动失败尝试恢复旧配置。', 'Restarts only the exact applied version, configuration and identities; newer drafts and other cores are not used. Usage is saved first, the preflight runs again and expired or exhausted clients are removed; if all are restricted the core stops. Connections are interrupted; a failed preflight keeps the old core running, and a failed start tries to restore the old configuration.')}</p>${!canRestart() ? t('<p class="form-hint">当前运行实例或历史已改变，或核心已停止；请关闭后重新读取。停止时请显式应用配置或恢复历史，不会把重启当作启动。</p>', '<p class="form-hint">The running instance or history changed, or the core stopped; close and reload. When stopped, apply or restore explicitly; restart never acts as start.</p>') : ''}<label class="check-field"><input id="runtime-confirm" type="checkbox" ${confirmed ? 'checked' : ''} ${busy || !canRestart() ? 'disabled' : ''}>${t('我确认只重启当前已应用配置，不应用草稿', 'I confirm restarting only the applied configuration, without drafts')}</label>` : ''}
      ${needsProxyConfirmation() ? `<label class="check-field"><input id="runtime-proxy-confirm" type="checkbox" ${proxyConfirmed ? 'checked' : ''} ${busy ? 'disabled' : ''}>${t('我确认开放非回环代理监听，允许其他设备尝试连接这些代理端口', 'I confirm opening non-loopback proxy listeners so other devices can try to connect to these ports')}</label><p class="core-warning">${t('可能对局域网或公网可达，实际范围取决于地址、系统与防火墙；分享链接包含代理凭据，请妥善保管。官方预检不会启动监听。', 'They may be reachable from the LAN or the internet depending on address, system and firewall. Share links contain proxy credentials; keep them safe. The official preflight does not start listeners.')}</p>` : ''}
      <p class="core-message" role="status">${esc(message)}</p>${error ? `<p class="form-error" role="alert" tabindex="-1">${esc(error)}</p>` : ''}</div>
      <footer>${action !== 'stop' ? `<button class="secondary-button" id="runtime-preflight" ${busy || (action === 'apply' ? !snapshot : action === 'restart' ? !canRestart() : !history?.available || !status || status.running) ? 'disabled' : ''}>${action === 'resume' ? t('官方预检历史配置', 'Preflight history config') : t('官方预检', 'Official preflight')}</button>` : ''}<button class="primary-button" id="runtime-execute" ${busy || !status || (needsProxyConfirmation() && !proxyConfirmed) || (action === 'apply' && !snapshot) || (action === 'resume' && (!confirmed || !history?.available || status?.running)) || (action === 'restart' && (!confirmed || !canRestart())) ? 'disabled' : ''}>${busy ? t('正在处理…', 'Working…') : action === 'stop' ? t('确认停止', 'Confirm stop') : action === 'resume' ? t('确认恢复并启动', 'Confirm restore and start') : action === 'restart' ? t('确认重启当前配置', 'Confirm restart') : t('确认应用并启动', 'Confirm apply and start')}</button></footer>`;
    dialog.querySelector('#runtime-close')!.addEventListener('click', () => dialog.close());
    dialog.querySelector('#runtime-preflight')?.addEventListener('click', () => { void run(action === 'resume' ? 'resume-preflight' : action === 'restart' ? 'restart-preflight' : 'preflight'); });
    dialog.querySelector('#runtime-execute')!.addEventListener('click', () => { void run(action); });
    dialog.querySelector('#runtime-resume')?.addEventListener('click', () => { action = 'resume'; confirmed = false; proxyConfirmed = false; error = ''; message = ''; paint(); dialog.querySelector<HTMLElement>('#runtime-confirm')?.focus(); });
    dialog.querySelector('#runtime-back')?.addEventListener('click', () => { action = 'apply'; confirmed = false; proxyConfirmed = false; error = ''; message = ''; paint(); dialog.querySelector<HTMLElement>('#runtime-preflight')?.focus(); });
    dialog.querySelector<HTMLInputElement>('#runtime-confirm')?.addEventListener('change', event => {
      confirmed = (event.currentTarget as HTMLInputElement).checked;
      paint(); dialog.querySelector<HTMLElement>('#runtime-confirm')?.focus();
    });
    dialog.querySelector<HTMLInputElement>('#runtime-proxy-confirm')?.addEventListener('change', event => {
      proxyConfirmed = (event.currentTarget as HTMLInputElement).checked;
      paint(); dialog.querySelector<HTMLElement>('#runtime-proxy-confirm')?.focus();
    });
  }
  async function refresh(afterFailure = false): Promise<void> {
    if (busy && !afterFailure) return;
    const request = ++statusRequest, at = Date.now();
    try {
      const next = await api<RuntimeStatus>('runtime/status');
      if (request <= statusAccepted) return;
      statusAccepted = request; status = next; onStatus(status, at);
    } catch { if (request > statusAccepted) { statusAccepted = request; status = null; onStatus(null, at); } }
  }
  async function load(): Promise<void> {
    statusAccepted = ++statusRequest;
    const at = Date.now();
    busy = true; snapshot = null; history = null; restartSnapshot = null; confirmed = false; proxyConfirmed = false; draftNonLoopback = false; error = ''; message = t('正在读取已保存修订与运行历史…', 'Reading saved revisions and run history…'); paint();
    try {
      if (action === 'stop') {
        status = await api<RuntimeStatus>('runtime/status');
      } else if (action === 'restart') {
        [status, history] = await Promise.all([api<RuntimeStatus>('runtime/status'), api<RuntimeHistory>('runtime/history')]);
        if (status.running && status.startedAt) restartSnapshot = { revision: history.revision, startedAt: status.startedAt };
      } else {
        const [core, workspace, settings, runtime, last] = await Promise.all([
          api<CoreStatus>('core/status'), api<{ revision: number; data: Workspace | null }>('workspace'), api<{ revision: number }>('settings'), api<RuntimeStatus>('runtime/status'), api<RuntimeHistory>('runtime/history'),
        ]);
        snapshot = { version: core.selected, workspaceRevision: workspace.revision, settingsRevision: settings.revision };
        draftNonLoopback = (workspace.data?.inbounds ?? []).some(inbound => inbound.enabled && !isLoopbackListen(inbound.listen));
        history = last; status = runtime;
      }
      onStatus(status, at); message = t('保存和应用相互独立；请核对后操作。', 'Saving and applying are separate; review before proceeding.');
    } catch (reason) { error = (reason as Error).message; message = ''; }
    busy = false; paint(); dialog.querySelector<HTMLElement>(error ? '[role="alert"]' : '#runtime-close')?.focus();
  }
  async function run(operation: 'preflight' | 'apply' | 'stop' | 'resume-preflight' | 'resume' | 'restart-preflight' | 'restart'): Promise<void> {
    if (busy) return;
    const resume = operation.startsWith('resume');
    const restart = operation.startsWith('restart');
    const starting = operation === 'apply' || operation === 'resume' || operation === 'restart';
    if (starting && needsProxyConfirmation() && !proxyConfirmed) {
      error = t('请先明确确认开放非回环代理监听；未启动或重启核心。', 'Confirm opening non-loopback proxy listeners first; the core was not started or restarted.');
      paint(); dialog.querySelector<HTMLElement>('[role="alert"]')?.focus(); return;
    }
    if (restart && (!canRestart() || (operation === 'restart' && !confirmed))) {
      error = t('请核对当前运行实例并明确确认重启；实例已改变时请关闭后重新读取。', 'Review the running instance and confirm the restart; if it changed, close and reload.');
      paint(); dialog.querySelector<HTMLElement>('[role="alert"]')?.focus(); return;
    }
    if (resume && (!history?.available || status?.running || (operation === 'resume' && !confirmed))) {
      error = status?.running ? t('核心已运行，请先停止后再恢复。', 'The core is running; stop it before restoring.') : t('请重新读取可用历史并明确确认恢复。', 'Reload the available history and confirm the restore.');
      paint(); dialog.querySelector<HTMLElement>('[role="alert"]')?.focus(); return;
    }
    const historyRevision = history?.revision;
    statusAccepted = ++statusRequest;
    const at = Date.now();
    busy = true; error = ''; message = operation === 'stop' ? t('正在停止自有核心…', 'Stopping o-ui\'s core…') : t('正在生成配置并执行官方预检，请勿重复提交…', 'Generating the configuration and running the official preflight; do not submit again…'); paint();
    try {
      const request = operation === 'stop' ? {} : restart ? { ...restartSnapshot, ...(operation === 'restart' ? { confirmation: '确认重启当前配置' } : {}) } : resume ? { revision: history!.revision, ...(operation === 'resume' ? { confirmation: '确认恢复上次应用' } : {}) } : snapshot;
      status = await api<RuntimeStatus>(`runtime/${operation}`, jsonBody('POST', { ...request, ...(starting && needsProxyConfirmation() && proxyConfirmed ? { proxyConfirmation: '确认开放代理监听' } : {}) }));
      onStatus(status, at);
      message = operation.endsWith('preflight') ? t('官方预检通过；未启动或重启核心。', 'Official preflight passed; the core was not started or restarted.') : operation === 'stop' ? t('本项目核心已停止。', 'o-ui\'s core is stopped.') : status.running ? restart ? t('已重启当前已应用配置；较新草稿、核心选择与累计用量保留。', 'Restarted the applied configuration; newer drafts, core selection and usage totals are kept.') : resume ? t('已恢复上次应用并启动；较新草稿与累计用量保留。连接导出仍要求草稿与运行修订一致。', 'Restored the last apply and started; newer drafts and usage totals are kept. Connection export still requires matching draft and running revisions.') : t('已应用并启动本机核心；可在客户端操作中按需显示连接信息，进程运行不等于已通过连接验收。', 'Applied and started the core; show connection info from the client actions as needed. A running process does not mean connections are verified.') : t('操作完成；没有有效入站，独立核心已停止，不会保留旧监听。', 'Done; there are no valid inbounds, so the core is stopped and no old listeners remain.');
    } catch (reason) { error = (reason as Error).message; message = ''; await refresh(true); }
    if (operation !== 'stop') { try { history = await api<RuntimeHistory>('runtime/history'); } catch { history = null; } }
    if (starting) proxyConfirmed = false;
    if (operation === 'resume' || operation === 'restart' || history?.revision !== historyRevision) confirmed = false;
    busy = false; paint(); dialog.querySelector<HTMLElement>(error ? '[role="alert"]' : '#runtime-close')?.focus();
  }
  void refresh();
  return { refresh, open: (next = 'apply') => { if (dialog.open) return; action = next; returnFocus = document.activeElement as HTMLElement; dialog.showModal(); void load(); } };
}
