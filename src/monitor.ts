import { api } from './api.ts';
import { bytes, duration, fresh, networkPath, coreReadings, type CoreMetrics, type SystemSnapshot, type Capacity } from './monitor-model.ts';
import { escapeHtml as esc } from './workspace.ts';
import { resourceColor } from './model.ts';
import type { RuntimeStatus } from './runtime.ts';
import './monitor.css';
import { locale, t } from './i18n.ts';

export function initMonitor(root: HTMLElement, runtime: () => RuntimeStatus | null): void {
  const $ = <T extends Element = HTMLElement>(selector: string) => root.querySelector<T>(selector)!;
  const status = document.createElement('p');
  status.className = 'monitor-status';
  status.textContent = t('正在读取系统采样…', 'Reading system samples…');
  $('.network-panel').append(status);
  $('.status-card').setAttribute('aria-label', t('真实系统资源', 'System resources'));
  $('.statistics').setAttribute('aria-label', t('真实运行统计', 'Runtime statistics'));
  const groups = [...root.querySelectorAll('.stat-group')];
  groups[1].querySelector('h2')!.textContent = t('Xray 使用情况', 'Xray usage');
  groups[1].querySelectorAll('dt')[0].textContent = t('常驻内存', 'Resident memory');
  groups[1].querySelectorAll('dt')[1].textContent = t('系统线程', 'OS threads');
  groups[2].querySelector('h2')!.textContent = t('业务流量累计', 'Proxy traffic totals');
  groups[2].querySelectorAll('dt')[0].textContent = t('上传', 'Upload');
  groups[2].querySelectorAll('dt')[1].textContent = t('下载', 'Download');
  groups[3].querySelector('h2')!.textContent = t('连接数', 'Connections');
  groups[3].querySelectorAll('dt')[0].textContent = t('TCP 已建立', 'TCP established');
  groups[3].querySelectorAll('dt')[1].textContent = t('UDP 套接字', 'UDP sockets');
  const coreStatus = document.createElement('p');
  coreStatus.className = 'monitor-status core-metrics-status';
  $('.statistics').after(coreStatus);
  const dd = [...root.querySelectorAll<HTMLElement>('.statistics dd')];
  let last: SystemSnapshot | null = null, disposed = false, busy = false, error = '';
  let lastCore: CoreMetrics | null = null, coreBusy = false, coreError = '';
  let timer: ReturnType<typeof setTimeout> | undefined;
  let coreTimer: ReturnType<typeof setTimeout> | undefined;
  function paintCore(): void {
    if (disposed || !root.isConnected) return;
    const readings = coreReadings(coreError ? null : lastCore);
    for (const [i, target] of [2, 3, 6, 7].entries()) dd[target].textContent = readings.values[i];
    coreStatus.textContent = `${coreError || readings.note}${t('。仅本面板自有 Xray：内存为 RSS，线程不含监督进程；TCP 不含监听，UDP 含监听、不是 QUIC 会话或设备数。', '. o-ui\'s own Xray only: memory is RSS, threads exclude the supervisor; TCP excludes listeners, UDP includes listeners and is not QUIC sessions or devices.')}`;
  }
  function resource(index: number, detail: string, value: number | null): void {
    const row = root.querySelectorAll<HTMLElement>('.resource')[index];
    row.querySelector('.resource-copy span')!.textContent = detail;
    const valid = value !== null && Number.isFinite(value) && value >= 0 && value <= 100;
    row.querySelector('.resource-percent')!.textContent = valid ? `${value.toFixed(1)}%` : '—';
    row.style.setProperty('--resource-color', resourceColor(valid ? value : 0, ['var(--blue)', 'var(--cyan)', 'var(--orange)', 'var(--purple)'][index]));
    const meter = row.querySelector<HTMLElement>('.meter')!;
    meter.querySelector<HTMLElement>('span')!.style.width = `${valid ? value : 0}%`;
    meter.setAttribute('aria-label', `${row.querySelector('.resource-copy strong')!.textContent}${t('使用率', ' usage')}${valid ? '' : t('不可用', ' unavailable')}`);
    if (valid) meter.setAttribute('aria-valuenow', value.toFixed(1)); else meter.removeAttribute('aria-valuenow');
  }
  function capacity(index: number, c: Capacity | null, active: boolean): void {
    const none = c?.total === '0';
    resource(index, !active || !c ? t('采集不可用', 'Sampling unavailable') : none ? t('未配置交换空间', 'No swap configured') : `${bytes(c.used)} / ${bytes(c.total)}${index === 3 ? t(' · 数据盘', ' · data disk') : ''}`, active ? c?.percent ?? null : null);
  }
  function paint(): void {
    if (disposed || !root.isConnected) return;
    const s = last, active = !!s && fresh(s.at) && !error;
    $('.status-card').classList.toggle('monitor-stale', !active);
    const net = active ? s.network : null;
    $('.speed-readings').innerHTML = `<div class="speed upload"><span class="speed-label"><b></b>${t('上传', 'Upload')}</span><strong>${net?.upload == null ? '—' : esc(bytes(net.upload))}<small>/s</small></strong></div><div class="speed download"><span class="speed-label"><b></b>${t('下载', 'Download')}</span><strong>${net?.download == null ? '—' : esc(bytes(net.download))}<small>/s</small></strong></div>`;
    resource(0, active && s.host ? `${s.host.cpus} ${t('核 ·', 'cores ·')} ${s.host.cpuModel || s.host.architecture}` : t('采集不可用', 'Sampling unavailable'), active ? s.cpu : null);
    capacity(1, s?.memory ?? null, active); capacity(2, s?.swap ?? null, active); capacity(3, s?.disk ?? null, active);
    const server = [...root.querySelectorAll<HTMLElement>('.server-card dd')];
    $('#server-title').textContent = active && s.host ? s.host.name : t('本机服务器 · 信息不可用', 'This server · info unavailable');
    $('.server-description').textContent = active && s.host ? `${s.host.system} · ${s.host.architecture}` : t('系统信息采集不可用', 'System info unavailable');
    server[4].textContent = active && s.load ? s.load.map(n => n.toFixed(2)).join(' / ') : '—';
    const rt = runtime();
    dd[0].textContent = rt ? rt.running && rt.startedAt ? duration(Math.max(0, (Date.now() - Date.parse(rt.startedAt)) / 1000)) : t('已停止', 'Stopped') : '—';
    dd[1].textContent = active && s.host ? duration(s.host.uptime) : '—';
    dd[4].textContent = bytes(net?.sent ?? null); dd[5].textContent = bytes(net?.received ?? null);
    const points = s?.history ?? [], end = Date.parse(s?.at ?? '');
    const peak = Math.max(1024, ...points.flatMap(p => [p.upload ?? 0, p.download ?? 0]));
    const graph = $('.traffic-chart');
    graph.setAttribute('aria-label', t('最近三分钟面板业务速度', 'Proxy traffic speed over the last three minutes'));
    graph.innerHTML = !points.length ? t('<p class="form-intro">等待采样</p>', '<p class="form-intro">Waiting for samples</p>') : `<div class="axis-labels">${[1, .75, .5, .25].map(n => `<span>${esc(bytes(peak * n))}/s</span>`).join('')}</div><svg viewBox="0 0 900 170" preserveAspectRatio="none" aria-hidden="true">${[0,42.5,85,127.5,169].map(y => `<line x1="0" y1="${y}" x2="900" y2="${y}" class="grid-line"/>`).join('')}<path d="${networkPath(points, 'upload', peak, end)}" class="chart-line" stroke="var(--blue)"/><path d="${networkPath(points, 'download', peak, end)}" class="chart-line" stroke="var(--green)"/></svg>`;
    const at = s && fresh(s.at) ? new Date(s.at).toLocaleTimeString(locale, {hour12:false}) : s && Date.parse(s.at) > 0 ? `${t('已过期', 'stale')} ${new Date(s.at).toLocaleTimeString(locale, {hour12:false})}` : t('尚无有效采样', 'No valid samples yet');
    status.textContent = `${error || (active ? s.issues.length ? `${t('部分不可用：', 'Partly unavailable: ')}${s.issues.join(t('；', '; '))}` : net?.upload == null ? t('正在建立速率基线', 'Building the rate baseline') : t('采集中', 'Sampling') : t('采样已过期或尚未就绪', 'Sample is stale or not ready'))} · ${at}${t('。只统计本面板客户端经 Xray 的流量，不含系统其他程序。', '. Counts only client traffic through o-ui\'s Xray, not other programs.')}`;
  }
  async function refresh(): Promise<void> {
    if (disposed || busy) return;
    busy = true;
    try { last = await api<SystemSnapshot>('system/status'); error = ''; }
    catch (reason) { error = (reason as Error).message; }
    busy = false; paint();
    schedule();
  }
  async function refreshCore(): Promise<void> {
    if (disposed || coreBusy) return;
    coreBusy = true;
    try { lastCore = await api<CoreMetrics>('runtime/metrics'); coreError = ''; }
    catch (reason) { coreError = (reason as Error).message; }
    coreBusy = false; paintCore();
    scheduleCore();
  }
  function scheduleCore(): void {
    if (!disposed) coreTimer = setTimeout(() => { if (document.hidden) scheduleCore(); else void refreshCore(); }, 2000);
  }
  function schedule(): void {
    if (!disposed) timer = setTimeout(() => { if (document.hidden) schedule(); else void refresh(); }, 2000);
  }
  const freshnessTimer = setInterval(() => { if (!document.hidden) { if (last && !fresh(last.at)) paint(); if (lastCore && !fresh(lastCore.at)) paintCore(); } }, 1000);
  const visibility = () => { if (!document.hidden) { clearTimeout(timer); clearTimeout(coreTimer); void refresh(); void refreshCore(); } };
  document.addEventListener('visibilitychange', visibility);
  const stop = () => { disposed = true; clearTimeout(timer); clearTimeout(coreTimer); clearInterval(freshnessTimer); document.removeEventListener('visibilitychange', visibility); };
  window.addEventListener('panel-session-expired', stop, { once:true });
  window.addEventListener('pagehide', stop, { once:true });
  void refresh();
  void refreshCore();
}
