import { api } from './api.ts';
import { bytes, chartScale, duration, fresh, networkArea, networkPath, networkSegments, coreReadings, type CoreMetrics, type SystemSnapshot, type Capacity } from './monitor-model.ts';
import { escapeHtml as esc } from './workspace.ts';
import { resourceColor } from './model.ts';
import type { RuntimeStatus } from './runtime.ts';
import './monitor.css';
import { locale, t } from './i18n.ts';
import { infoTip } from './info-tip.ts';

export function initMonitor(root: HTMLElement, runtime: () => RuntimeStatus | null): void {
  const $ = <T extends Element = HTMLElement>(selector: string) => root.querySelector<T>(selector)!;
  // 状态与采样时间常显，统计口径收进ⓘ。
  const status = document.createElement('p');
  status.className = 'monitor-status info-tip-host';
  status.innerHTML = `<span>${t('正在读取系统采样…', 'Reading system samples…')}</span>${infoTip('')}`;
  $('.network-panel').append(status);
  const statusText = status.querySelector('span')!, statusTip = status.querySelector('.info-tip-text')!;
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
  coreStatus.className = 'monitor-status core-metrics-status info-tip-host';
  coreStatus.innerHTML = `<span></span>${infoTip(t('仅本面板自有 Xray：内存为 RSS，线程不含监督进程；TCP 不含监听，UDP 含监听、不是 QUIC 会话或设备数。', 'o-ui\'s own Xray only: memory is RSS, threads exclude the supervisor; TCP excludes listeners, UDP includes listeners and is not QUIC sessions or devices.'))}`;
  $('.statistics').after(coreStatus);
  const coreStatusText = coreStatus.querySelector('span')!;
  const dd = [...root.querySelectorAll<HTMLElement>('.statistics dd')];
  let last: SystemSnapshot | null = null, disposed = false, busy = false, error = '';
  let lastCore: CoreMetrics | null = null, coreBusy = false, coreError = '';
  let timer: ReturnType<typeof setTimeout> | undefined;
  let coreTimer: ReturnType<typeof setTimeout> | undefined;
  function paintCore(): void {
    if (disposed || !root.isConnected) return;
    const readings = coreReadings(coreError ? null : lastCore);
    for (const [i, target] of [2, 3, 6, 7].entries()) dd[target].textContent = readings.values[i];
    coreStatusText.textContent = coreError || readings.note;
  }
  function resource(index: number, detail: string, value: number | null): void {
    const row = root.querySelectorAll<HTMLElement>('.resource')[index];
    row.querySelector('.resource-copy span')!.textContent = detail;
    const valid = value !== null && Number.isFinite(value) && value >= 0 && value <= 100;
    row.querySelector('.resource-percent')!.textContent = valid ? `${value.toFixed(1)}%` : '—';
    row.style.setProperty('--resource-color', resourceColor(valid ? value : 0, 'var(--blue)'));
    const meter = row.querySelector<HTMLElement>('.meter')!;
    meter.querySelector<HTMLElement>('span')!.style.width = `${valid ? value : 0}%`;
    meter.setAttribute('aria-label', `${row.querySelector('.resource-copy strong')!.textContent}${t('使用率', ' usage')}${valid ? '' : t('不可用', ' unavailable')}`);
    if (valid) meter.setAttribute('aria-valuenow', value.toFixed(1)); else meter.removeAttribute('aria-valuenow');
  }
  function capacity(index: number, c: Capacity | null, active: boolean): void {
    const none = c?.total === '0';
    resource(index, !active || !c ? t('采集不可用', 'Sampling unavailable') : none ? t('未配置交换空间', 'No swap configured') : `${bytes(c.used)} / ${bytes(c.total)}${index === 3 ? t(' · 数据盘', ' · data disk') : ''}`, active ? c?.percent ?? null : null);
  }
  // 网速图可在“节点流量”（Xray客户端流量，客户端角度）与“网卡总速度”（物理网卡，服务器角度，同OUI）之间切换，选择记在本浏览器。
  type Source = 'node' | 'host';
  let source: Source = 'node';
  try { if (localStorage.getItem('independent-panel-speed-source') === 'host') source = 'host'; } catch { /* 禁用存储时用默认。 */ }
  const sourceLabel = $('.network-panel .section-label'), heading = document.createElement('div'), toggle = document.createElement('div');
  heading.className = 'network-heading';
  toggle.className = 'speed-source';
  toggle.setAttribute('role', 'group');
  toggle.setAttribute('aria-label', t('网速统计范围', 'Speed source'));
  toggle.innerHTML = ([['node', t('节点流量', 'Node traffic')], ['host', t('网卡总速度', 'NIC total')]] as const).map(([key, text]) => `<button type="button" data-speed-source="${key}">${text}</button>`).join('');
  sourceLabel.replaceWith(heading);
  heading.append(sourceLabel, toggle);
  toggle.addEventListener('click', event => {
    const key = (event.target as Element).closest<HTMLElement>('[data-speed-source]')?.dataset.speedSource;
    if ((key !== 'node' && key !== 'host') || key === source) return;
    source = key;
    try { localStorage.setItem('independent-panel-speed-source', key); } catch { /* 同上。 */ }
    paint();
  });
  const view = () => source === 'host' ? { speed: last?.hostNetwork ?? null, points: last?.hostHistory ?? [] } : { speed: last?.network ?? null, points: last?.history ?? [] };
  // 网速图悬停：竖线、两条线上的点与读数；图表每次采样重绘，所以记住指针位置并在重绘后补画。
  let hover: number | null = null;
  const chart = $('.traffic-chart');
  chart.addEventListener('pointermove', event => {
    const svg = chart.querySelector('svg');
    if (!svg) return;
    const box = svg.getBoundingClientRect();
    hover = Math.min(1, Math.max(0, (event.clientX - box.left) / box.width));
    drawHover();
  });
  chart.addEventListener('pointerleave', () => { hover = null; drawHover(); });
  function drawHover(): void {
    chart.querySelector('.chart-hover')?.remove();
    const svg = chart.querySelector('svg'), points = view().points, end = Date.parse(last?.at ?? '');
    if (hover === null || !svg || !points.length || !Number.isFinite(end)) return;
    const start = end - 180000, target = start + hover * 180000;
    let best = points[0], gap = Infinity;
    for (const point of points) { const distance = Math.abs(Date.parse(point.at) - target); if (distance < gap) { gap = distance; best = point; } }
    if (gap > 6000 || (best.upload === null && best.download === null)) return;
    const peak = chartScale(Math.max(0, ...points.flatMap(p => [p.upload ?? 0, p.download ?? 0])));
    const box = svg.getBoundingClientRect(), host = chart.getBoundingClientRect();
    const x = box.left - host.left + (Date.parse(best.at) - start) / 180000 * box.width, top = box.top - host.top;
    const y = (value: number) => top + (1 - Math.min(value / peak, 1)) * box.height;
    const dot = (value: number | null, color: string) => value === null ? '' : `<b class="chart-dot" style="left:${x}px;top:${y(value)}px;color:${color}"></b>`;
    const row = (value: number | null, color: string, label: string) => `<span><b style="background:${color}"></b>${label}<strong>${value === null ? '—' : `${esc(bytes(value, 1000))}/s`}</strong></span>`;
    const overlay = document.createElement('div');
    overlay.className = 'chart-hover';
    overlay.setAttribute('aria-hidden', 'true');
    overlay.innerHTML = `<span class="chart-cursor" style="left:${x}px;top:${top}px;height:${box.height}px"></span>${dot(best.upload, 'var(--series-1)')}${dot(best.download, 'var(--series-2)')}<div class="chart-tip${(x - (box.left - host.left)) > box.width * .6 ? ' is-left' : ''}" style="left:${x}px;top:${top}px"><time>${new Date(best.at).toLocaleTimeString(locale, { hour12: false })}</time>${row(best.upload, 'var(--series-1)', t('上传', 'Upload'))}${row(best.download, 'var(--series-2)', t('下载', 'Download'))}</div>`;
    chart.append(overlay);
  }
  function paint(): void {
    if (disposed || !root.isConnected) return;
    const s = last, active = !!s && fresh(s.at) && !error;
    $('.status-card').classList.toggle('monitor-stale', !active);
    const net = active ? s.network : null, speed = active ? view().speed : null;
    toggle.querySelectorAll<HTMLElement>('[data-speed-source]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.speedSource === source)));
    $('.speed-readings').innerHTML = `<div class="speed upload"><span class="speed-label"><b></b>${t('上传', 'Upload')}</span><strong>${speed?.upload == null ? '—' : esc(bytes(speed.upload, 1000))}<small>/s</small></strong></div><div class="speed download"><span class="speed-label"><b></b>${t('下载', 'Download')}</span><strong>${speed?.download == null ? '—' : esc(bytes(speed.download, 1000))}<small>/s</small></strong></div>`;
    resource(0, active && s.host ? `${s.host.cpus} ${t('核 ·', 'cores ·')} ${s.host.cpuModel || s.host.architecture}` : t('采集不可用', 'Sampling unavailable'), active ? s.cpu : null);
    capacity(1, s?.memory ?? null, active); capacity(2, s?.swap ?? null, active); capacity(3, s?.disk ?? null, active);
    const server = [...root.querySelectorAll<HTMLElement>('.server-card dd')];
    $('#server-title').textContent = active && s.host ? s.host.name : t('本机服务器 · 信息不可用', 'This server · info unavailable');
    $('.server-description').textContent = active && s.host ? `${s.host.system} · ${s.host.architecture}` : t('系统信息采集不可用', 'System info unavailable');
    server[4].textContent = active && s.load ? s.load.map(n => n.toFixed(2)).join(' / ') : '—';
    const rt = runtime();
    dd[0].textContent = rt ? rt.running && rt.startedAt ? duration(Math.max(0, (Date.now() - Date.parse(rt.startedAt)) / 1000)) : t('已停止', 'Stopped') : '—';
    dd[1].textContent = active && s.host ? duration(s.host.uptime) : '—';
    dd[4].textContent = bytes(net?.sent ?? null, 1000); dd[5].textContent = bytes(net?.received ?? null, 1000);
    const points = view().points, end = Date.parse(s?.at ?? '');
    const peak = chartScale(Math.max(0, ...points.flatMap(p => [p.upload ?? 0, p.download ?? 0])));
    const graph = $('.traffic-chart');
    const series: ['upload' | 'download', string][] = [['upload', 'var(--series-1)'], ['download', 'var(--series-2)']];
    graph.setAttribute('aria-label', source === 'host' ? t('最近三分钟服务器网卡总速度', 'Server NIC speed over the last three minutes') : t('最近三分钟面板业务速度', 'Proxy traffic speed over the last three minutes'));
    graph.innerHTML = !points.length ? t('<p class="form-intro">等待采样</p>', '<p class="form-intro">Waiting for samples</p>') : `<div class="axis-labels">${[1, .75, .5, .25].map(n => `<span>${esc(bytes(peak * n, 1000))}/s</span>`).join('')}</div><div class="chart-plot"><svg viewBox="0 0 900 170" preserveAspectRatio="none" aria-hidden="true"><defs>${series.map(([direction, color]) => `<linearGradient id="live-${direction}-fill" x1="0" y1="0" x2="0" y2="1"><stop stop-color="${color}" stop-opacity=".38"/><stop offset="1" stop-color="${color}" stop-opacity=".02"/></linearGradient>`).join('')}</defs>${[0,42.5,85,127.5,169].map(y => `<line x1="0" y1="${y}" x2="900" y2="${y}" class="grid-line"/>`).join('')}${series.map(([direction]) => `<path d="${networkArea(points, direction, peak, end)}" fill="url(#live-${direction}-fill)"/>`).join('')}${series.map(([direction, color]) => `<path d="${networkPath(points, direction, peak, end)}" class="chart-line" stroke="${color}"/>`).join('')}</svg>${series.map(([direction, color]) => { const tail = networkSegments(points, direction, peak, end).at(-1)?.at(-1); return tail && tail[0] > 880 ? `<span class="chart-live" style="left:${(tail[0] / 9).toFixed(2)}%;top:${(tail[1] / 1.7).toFixed(2)}%;color:${color}"></span>` : ''; }).join('')}</div>`;
    drawHover();
    const at = s && fresh(s.at) ? new Date(s.at).toLocaleTimeString(locale, {hour12:false}) : s && Date.parse(s.at) > 0 ? `${t('已过期', 'stale')} ${new Date(s.at).toLocaleTimeString(locale, {hour12:false})}` : t('尚无有效采样', 'No valid samples yet');
    statusText.textContent = `${error || (active ? s.issues.length ? `${t('部分不可用：', 'Partly unavailable: ')}${s.issues.join(t('；', '; '))}` : speed?.upload == null ? t('正在建立速率基线', 'Building the rate baseline') : t('采集中', 'Sampling') : t('采样已过期或尚未就绪', 'Sample is stale or not ready'))} · ${at}`;
    statusTip.textContent = source === 'host' ? t('按服务器物理网卡统计（同 OUI，不含回环与 docker、tun 等虚拟网卡）：上传为服务器发出，下载为服务器收到，含网站、SSH 等全部流量；经节点的流量进出各计一次。', 'Measured on the server\'s physical NICs (like OUI; loopback and virtual NICs such as docker or tun excluded): upload is sent by the server, download is received, including all traffic such as websites and SSH; proxied traffic counts once in and once out.') : t('只统计本面板客户端经 Xray 的流量（客户端角度），不含系统其他程序。', 'Counts only client traffic through o-ui\'s Xray (from the client\'s side), not other programs.');
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
