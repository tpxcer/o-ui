import { api } from './api.ts';
import { logRows, logTime, type LogLevel, type LogSnapshot, type LogSource } from './logs-model.ts';
import './logs.css';
import { locale, t } from './i18n.ts';

export function initLogs(refreshIcons: () => void): { open: (source: 'panel' | 'xray') => void; close: () => void } {
  const dialog = document.createElement('dialog');
  dialog.className = 'logs-dialog'; dialog.setAttribute('aria-labelledby', 'logs-title'); document.body.append(dialog);
  let generation = 0, timer: number | undefined, returnFocus: HTMLElement | null = null;
  function clear(): void { generation++; window.clearInterval(timer); timer = undefined; dialog.replaceChildren(); }
  function close(): void { clear(); if (dialog.open) dialog.close(); }
  dialog.addEventListener('cancel', event => { event.preventDefault(); close(); });
  dialog.addEventListener('close', () => { if (!dialog.open) { clear(); if (returnFocus?.isConnected) returnFocus.focus(); returnFocus = null; } });
  window.addEventListener('panel-session-expired', close);
  window.addEventListener('hashchange', close);
  document.addEventListener('visibilitychange', () => { if (document.hidden) close(); });
  return { close, open: initialSource => {
    if (dialog.open) return;
    clear(); returnFocus = document.activeElement as HTMLElement;
    const ticket = generation;
    let snapshot: LogSnapshot | null = null, paused = false, busy = false, last = '', failure = false, pending = false, rendered = '';
    dialog.innerHTML = `<header class="dialog-heading"><h2 id="logs-title">${t('日志与诊断', 'Logs and diagnostics')}</h2><button class="icon-button" data-close aria-label="${t('关闭日志', 'Close logs')}"><i data-lucide="x" aria-hidden="true"></i></button></header>
      <div class="logs-body"><p class="form-hint">${t('仅显示本轮操作结果及核心固定类别摘要，不是完整原始日志或访问日志。不记录密码、私钥、客户端身份、地址及配置；未识别原文隐藏。最多256条，满后覆盖，面板重启清空；不会读取外部日志文件。', 'Shows only this run\'s operation results and categorized core summaries, not complete raw or access logs. Passwords, private keys, client identities, addresses and configs are never recorded; unrecognized text is hidden. Up to 256 entries, oldest overwritten, cleared when o-ui restarts; external log files are never read.')}</p>
      <div class="logs-tools"><label class="form-field" for="logs-source">${t('来源', 'Source')}<select id="logs-source"><option value="all">${t('全部', 'All')}</option><option value="xray">Xray</option><option value="panel">${t('面板', 'Panel')}</option></select></label><label class="form-field" for="logs-level">${t('级别', 'Level')}<select id="logs-level"><option value="all">${t('全部', 'All')}</option><option value="info">${t('信息', 'Info')}</option><option value="warning">${t('警告', 'Warning')}</option><option value="error">${t('错误', 'Error')}</option></select></label><button class="secondary-button" data-pause aria-pressed="false">${t('暂停刷新', 'Pause refresh')}</button></div>
      <p class="form-error" role="alert" hidden></p><p class="form-hint logs-status" role="status">${t('正在读取本轮日志…', 'Reading this run\'s logs…')}</p><div class="logs-entries" tabindex="0" aria-label="${t('日志摘要，最新在前', 'Log summaries, newest first')}"><p class="logs-empty">${t('正在读取…', 'Reading…')}</p></div></div>
      <footer><button class="secondary-button" data-refresh>${t('刷新', 'Refresh')}</button><button class="primary-button" data-close>${t('关闭', 'Close')}</button></footer>`;
    const source = dialog.querySelector<HTMLSelectElement>('#logs-source')!, level = dialog.querySelector<HTMLSelectElement>('#logs-level')!;
    const entries = dialog.querySelector<HTMLElement>('.logs-entries')!, status = dialog.querySelector<HTMLElement>('.logs-status')!, error = dialog.querySelector<HTMLElement>('.form-error')!;
    const refresh = dialog.querySelector<HTMLButtonElement>('[data-refresh]')!, pause = dialog.querySelector<HTMLButtonElement>('[data-pause]')!;
    source.value = initialSource;
    const active = () => dialog.open && ticket === generation;
    const signature = (value: LogSnapshot) => `${value.since}/${value.dropped}/${value.entries.at(-1)?.sequence ?? 0}`;
    function describe(): void {
      if (!snapshot) { status.textContent = failure ? t('读取未成功，请检查本机服务后重试。', 'Read failed; check the panel service and retry.') : t('正在读取本轮日志…', 'Reading this run\'s logs…'); return; }
      status.textContent = `${paused ? t('已暂停自动刷新。', 'Auto refresh paused. ') : t('约2秒刷新。', 'Refreshes about every 2 seconds. ')}${failure ? t('读取失败，下方保留上次记录。', 'Read failed; the previous entries are kept below.') : ''}${pending ? t('有新记录，点击刷新查看，避免打断正在阅读的位置。', 'New entries available; click Refresh to view them without losing your place.') : ''}${t('上次读取', 'Last read')} ${last}${t('；本轮始于', '; this run started')} ${logTime(snapshot.since)}${t('，保留', ', keeping ')}${snapshot.entries.length}/${snapshot.limit}${t('条，已覆盖', ' entries, overwritten ')}${snapshot.dropped}${t('条。', '.')}`;
    }
    function render(): void {
      if (snapshot) { entries.innerHTML = logRows(snapshot, source.value as LogSource, level.value as LogLevel); rendered = signature(snapshot); }
      pending = false; describe();
    }
    async function load(manual = false): Promise<void> {
      if (!active() || busy || document.hidden || paused && !manual) return;
      busy = true; refresh.disabled = true;
      try {
        const result = await api<LogSnapshot>('logs');
        if (!active() || paused && !manual) return;
        snapshot = result; last = new Date().toLocaleTimeString(locale, { hour12: false }); failure = false; error.hidden = true;
        if (manual || entries.scrollTop < 4) { render(); if (manual) entries.scrollTop = 0; } else { pending = signature(result) !== rendered; describe(); }
      } catch (reason) {
        if (!active()) return;
        failure = true; error.textContent = (reason as Error).message; error.hidden = false;
        if (!snapshot) entries.innerHTML = t('<p class="logs-empty">暂无可显示的记录；读取失败不代表没有错误。</p>', '<p class="logs-empty">No entries to show; a failed read does not mean there were no errors.</p>');
        describe();
      } finally { if (active()) { busy = false; refresh.disabled = false; } }
    }
    dialog.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', close));
    for (const control of [source, level]) control.addEventListener('change', () => { entries.scrollTop = 0; render(); });
    pause.addEventListener('click', () => { paused = !paused; pause.textContent = paused ? t('继续刷新', 'Resume refresh') : t('暂停刷新', 'Pause refresh'); pause.setAttribute('aria-pressed', String(paused)); describe(); if (!paused) void load(); });
    refresh.addEventListener('click', () => { void load(true); });
    refreshIcons(); dialog.showModal(); source.focus(); void load();
    timer = window.setInterval(() => { void load(); }, 2000);
  } };
}
