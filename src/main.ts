import { createIcons, Gauge, LogIn, Users, Settings, Wrench, PanelLeftClose,
  PanelLeftOpen, Moon, Sun, Eye, EyeOff, ArrowUp, ArrowDown, List, Power, RotateCw,
  RefreshCw, SlidersHorizontal, DatabaseBackup, ChartArea, ChartNoAxesCombined, Menu, X,
  Info, Plus, Search, Pencil, Trash2, CalendarDays, Shuffle, QrCode, Zap, LogOut, GripVertical, Bell, BellOff, SunMoon, ChevronDown } from 'lucide';
import { demo, linePath, percent, resourceColor, samples } from './model.ts';
import { initManagement } from './management.ts';
import { initCore } from './core.ts';
import { applySavedConfig, initRuntime, runtimeStatusText, stopCore } from './runtime.ts';
import { initBackup } from './backup.ts';
import { initMonitor } from './monitor.ts';
import { initLogs } from './logs.ts';
import type { RuntimeStatus } from './runtime.ts';
import { initXraySettings } from './xray-settings.ts';
import { installInfoTips } from './info-tip.ts';
import { initPanelSettings } from './panel-settings.ts';
import { initServerInfo } from './server-info.ts';
import { requireLogin } from './auth.ts';
import { api, jsonBody } from './api.ts';
import { confirmAction } from './confirm.ts';
import { downloadIcon, startUpgrade, type PanelVersion } from './upgrade.ts';
import './style.css';
import { lang, languageButton, locale, setLang, t } from './i18n.ts';
import { panelBase, panelPage, pageURL, type PanelPage } from './panel-routing.ts';

document.documentElement.lang = locale;
document.querySelector('.skip-link')!.textContent = t('跳到主要内容', 'Skip to main content');
document.addEventListener('click', event => {
  const next = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-lang]')?.dataset.lang : undefined;
  if (next === 'zh' || next === 'en') setLang(next);
});

const icons = { Gauge, LogIn, Users, Settings, Wrench, PanelLeftClose,
  PanelLeftOpen, Moon, Sun, Eye, EyeOff, ArrowUp, ArrowDown, List, Power, RotateCw,
  RefreshCw, SlidersHorizontal, DatabaseBackup, ChartArea, ChartNoAxesCombined, Menu, X,
  Info, Plus, Search, Pencil, Trash2, CalendarDays, Shuffle, QrCode, Zap, LogOut, GripVertical, Bell, BellOff, SunMoon, ChevronDown };
const icon = (name: string) => `<i data-lucide="${name}" aria-hidden="true"></i>`;
const action = (label: string, name: string, id: string) =>
  `<button class="action" data-action="${id}">${icon(name)}<span>${label}</span></button>`;

function chart(): string {
  const upload = linePath(samples.map(s => s.upload), 900, 170, 320, true);
  const download = linePath(samples.map(s => s.download), 900, 170, 320, true);
  return `<div class="traffic-chart" role="img" aria-label="${t('演示上传和下载历史趋势，非实时监控', 'Demo upload and download history, not live monitoring')}">
    <div class="axis-labels"><span>320 KB/s</span><span>240 KB/s</span><span>160 KB/s</span><span>80 KB/s</span></div>
    <svg viewBox="0 0 900 170" preserveAspectRatio="none" aria-hidden="true">
      <defs><linearGradient id="upload-fill" x1="0" y1="0" x2="0" y2="1"><stop stop-color="var(--series-1)" stop-opacity=".18"/><stop offset="1" stop-color="var(--series-1)" stop-opacity=".02"/></linearGradient>
      <linearGradient id="download-fill" x1="0" y1="0" x2="0" y2="1"><stop stop-color="var(--series-2)" stop-opacity=".15"/><stop offset="1" stop-color="var(--series-2)" stop-opacity=".01"/></linearGradient></defs>
      ${[0, 42.5, 85, 127.5, 169].map(y => `<line x1="0" y1="${y}" x2="900" y2="${y}" class="grid-line"/>`).join('')}
      <path d="${upload} L900,170 L0,170 Z" fill="url(#upload-fill)"/>
      <path d="${download} L900,170 L0,170 Z" fill="url(#download-fill)"/>
      <path d="${upload}" class="chart-line" stroke="var(--series-1)"/>
      <path d="${download}" class="chart-line" stroke="var(--series-2)"/>
    </svg>
  </div>`;
}

function resource(name: string, detail: string, value: number, normal: string, trend: number[]): string {
  const color = resourceColor(value, normal);
  const path = linePath(trend, 72, 22, 100);
  return `<div class="resource" style="--resource-color:${color}">
    <div class="resource-top"><div class="resource-copy"><strong>${name}</strong><span>${detail}</span></div>
      <svg class="sparkline" viewBox="0 0 72 22" aria-hidden="true"><path d="${path} L72,22 L0,22 Z" fill="currentColor" opacity=".12"/><path d="${path}" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>
      <strong class="resource-percent">${value}%</strong></div>
    <div class="meter" role="progressbar" aria-label="${name}${t('使用率（演示）', ' usage (demo)')}" aria-valuenow="${value}" aria-valuemin="0" aria-valuemax="100"><span style="width:${value}%"></span></div>
  </div>`;
}

function field(label: string, value: string, sensitive = false): string {
  return `<div class="field"><dt>${label}</dt><dd${sensitive ? ` class="private-value" data-value="${value}" aria-label="${t('已隐藏', 'Hidden')}"` : ''}>${sensitive ? '••••••••••••' : value}</dd></div>`;
}

const app = document.querySelector<HTMLDivElement>('#app')!;
const accessBase = panelBase();
async function start(): Promise<void> {
const authenticated = await requireLogin();
app.innerHTML = `
  <div class="mobile-header"><button class="icon-button" id="mobile-menu" aria-label="${t('打开导航', 'Open navigation')}" aria-expanded="false" aria-controls="sidebar">${icon('menu')}</button><strong class="brand-name">o-ui</strong><span class="demo-label">${t('演示', 'Demo')}</span></div>
  <button class="menu-scrim" id="menu-scrim" aria-label="${t('关闭导航', 'Close navigation')}" hidden></button>
  <aside class="sidebar" id="sidebar" aria-label="${t('主导航', 'Main navigation')}">
    <div class="brand"><div class="brand-title"><strong class="brand-name">o-ui</strong><small class="slogan">${t('让世界触手可及', 'World within reach')}</small></div><div class="brand-actions">${languageButton()}<button class="icon-button" id="theme-toggle" aria-label="${t('切换到浅色主题', 'Switch to light theme')}">${icon('moon')}</button><button class="icon-button mobile-close" id="mobile-close" aria-label="${t('关闭导航菜单', 'Close navigation menu')}">${icon('x')}</button></div></div>
    <nav>
      ${[[t('系统状态', 'Dashboard'), 'gauge', 'home'], [t('入站列表', 'Inbounds'), 'log-in', 'inbounds'], [t('客户端', 'Clients'), 'users', 'clients'], [t('面板设置', 'Panel settings'), 'settings', 'settings'], [t('Xray 设置', 'Xray settings'), 'wrench', 'xray']].map(([label, name, id]) =>
        `<button class="nav-item${id === 'home' ? ' active' : ''}" data-nav="${id}" aria-label="${label}"${id === 'home' ? ' aria-current="page"' : ''}>${icon(name)}<span>${label}</span></button>`).join('')}
    </nav>
    <div class="repo-row"><a class="nav-item repo-link" href="https://github.com/tpxcer/o-ui" target="_blank" rel="noopener noreferrer" aria-label="${t('o-ui 项目主页（GitHub）', 'o-ui project home (GitHub)')}" title="${t('打开项目主页', 'Open the project home page')}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 .3a12 12 0 0 0-3.8 23.38c.6.12.83-.26.83-.57L9 21.07c-3.34.72-4.04-1.61-4.04-1.61-.55-1.39-1.34-1.76-1.34-1.76-1.08-.74.09-.73.09-.73 1.2.09 1.84 1.24 1.84 1.24 1.07 1.83 2.8 1.3 3.49 1 .1-.78.42-1.31.76-1.61-2.67-.3-5.47-1.33-5.47-5.93 0-1.31.47-2.38 1.24-3.22-.14-.3-.54-1.52.1-3.18 0 0 1-.32 3.3 1.23a11.5 11.5 0 0 1 6 0c2.28-1.55 3.29-1.23 3.29-1.23.64 1.66.24 2.88.12 3.18a4.65 4.65 0 0 1 1.23 3.22c0 4.61-2.8 5.63-5.48 5.92.42.36.81 1.1.81 2.22l-.01 3.29c0 .31.2.69.82.57A12 12 0 0 0 12 .3"/></svg><span class="prototype-version">${t('本地开发版 · 非生产面板', 'Local build · not a production panel')}</span></a><a class="icon-button tg-link" href="https://t.me/tpxcer" target="_blank" rel="noopener noreferrer" aria-label="${t('加入 Telegram 讨论群', 'Join the Telegram group')}" title="${t('加入 Telegram 讨论群', 'Join the Telegram group')}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.056 0zm4.962 7.224c.1-.002.321.023.465.14a.506.506 0 0 1 .171.325c.016.093.036.306.02.472-.18 1.898-.962 6.502-1.36 8.627-.168.9-.499 1.201-.82 1.23-.696.065-1.225-.46-1.9-.902-1.056-.693-1.653-1.124-2.678-1.8-1.185-.78-.417-1.21.258-1.91.177-.184 3.247-2.977 3.307-3.23.007-.032.014-.15-.056-.212s-.174-.041-.249-.024c-.106.024-1.793 1.14-5.061 3.345-.48.33-.913.49-1.302.48-.428-.008-1.252-.241-1.865-.44-.752-.245-1.349-.374-1.297-.789.027-.216.325-.437.893-.663 3.498-1.524 5.83-2.529 6.998-3.014 3.332-1.386 4.025-1.627 4.476-1.635z"/></svg></a></div>
    <div class="sidebar-bottom">
      ${authenticated ? `<button class="nav-item" id="logout">${icon('log-out')}<span>${t('退出登录', 'Sign out')}</span></button>` : ''}
      <button class="collapse-button" id="collapse-sidebar" aria-label="${t('收起侧栏', 'Collapse sidebar')}" aria-expanded="true">${icon('panel-left-close')}</button></div>
  </aside>
  <main id="main" tabindex="-1">
    <div class="demo-notice">${icon('info')}<span>${t('资源、入站与客户端仍为演示 · 登录、Xray 版本与设置草稿已接入本机服务；不连接 VPS', 'Resources, inbounds and clients are demo data · sign-in, Xray versions and settings drafts use the local service')}</span></div>
    <div id="home-view">
    <header class="page-heading"><div><h1 class="page-title" tabindex="-1">${t('系统状态', 'Dashboard')}</h1><p>${t('服务器资源、网速与 Xray 运行概况。', 'Server resources, network speed and Xray at a glance.')}</p></div></header>
    <section class="card status-card" aria-label="${t('资源状态（演示）', 'Resource status (demo)')}">
      <div class="network-panel"><div class="section-label">${t('整体速度', 'Overall speed')}</div>
        <div class="speed-readings"><div class="speed upload"><span class="speed-label"><b></b>${t('上传', 'Upload')}</span><strong>${icon('arrow-up')}268.00 <small>KB/s</small></strong></div>
        <div class="speed download"><span class="speed-label"><b></b>${t('下载', 'Download')}</span><strong>${icon('arrow-down')}174.00 <small>KB/s</small></strong></div></div>
        ${chart()}
      </div>
      <div class="resources">
        ${resource('CPU', '2.80 GHz', 7, 'var(--blue)', [5, 8, 6, 7, 6, 8, 7])}
        ${resource(t('内存', 'Memory'), '757.76 MB / 2.00 GB', 37, 'var(--blue)', [35, 36, 36, 37, 37, 36, 37])}
        ${resource(t('交换分区', 'Swap'), '61.44 MB / 1.00 GB', 6, 'var(--blue)', [6, 6, 6, 6, 6, 6, 6])}
        ${resource(t('存储', 'Storage'), '15.60 GB / 40.00 GB', 39, 'var(--blue)', [38, 38, 39, 39, 39, 39, 39])}
      </div>
    </section>
    <div class="home-grid">
      <section class="card server-card" aria-labelledby="server-title"><header class="card-header"><h2 id="server-title">${demo.hostname}</h2><div class="card-header-actions"><button class="icon-button" id="privacy-toggle" aria-label="${t('显示 IP 地址', 'Show IP address')}" aria-pressed="false">${icon('eye-off')}</button><button class="icon-button card-collapse" aria-expanded="true" aria-controls="server-body" aria-label="${t('服务器信息', 'Server info')}">${icon('chevron-down')}</button></div></header>
        <div class="card-body" id="server-body"><p class="server-description">${demo.provider} · ${demo.system} · KVM</p><dl>
          ${field('IPv4', demo.ipv4, true)}${field('IPv6', demo.ipv6, true)}${field(t('归属地', 'Location'), demo.location)}${field(t('运营商', 'ISP'), demo.network)}${field(t('系统负载', 'System load'), demo.load)}
        </dl></div>
      </section>
      <section class="card package-card" aria-labelledby="package-title"><header class="card-header"><h2 id="package-title">${t('服务器套餐', 'Server plan')}</h2><button class="icon-button card-collapse" aria-expanded="true" aria-controls="package-body" aria-label="${t('服务器套餐', 'Server plan')}">${icon('chevron-down')}</button></header>
        <div class="card-body" id="package-body"><div class="traffic-allowance"><div class="allowance-heading"><span>${t('本月流量', 'Traffic this month')}</span><strong>${demo.trafficUsed} GB / 2.00 TB</strong></div>
          <div class="allowance-progress"><div class="meter" role="progressbar" aria-label="${t('本月流量使用率（演示）', 'Monthly traffic usage (demo)')}" aria-valuenow="${percent(demo.trafficUsed, demo.trafficTotal).toFixed(1)}" aria-valuemin="0" aria-valuemax="100"><span style="width:${percent(demo.trafficUsed, demo.trafficTotal)}%"></span></div><span>10.4%</span></div>
          <p>${t('下次重置：', 'Next reset: ')}${demo.reset}</p></div>
          <dl>${field(t('节点', 'Node'), 'demo-01 / US, California')}${field(t('套餐', 'Plan'), `<span class="package-plan-name">${t('演示套餐 · 2核 / 2GB / 40GB', 'Demo plan · 2 cores / 2GB / 40GB')}</span><div class="package-resources" aria-label="${t('套餐资源', 'Plan resources')}"><span class="tag">RAM 2.00 GB</span><span class="tag">Disk 40.00 GB</span></div>`)}</dl><dl class="package-network">${field(t('服务商 IP', 'Provider IP'), demo.ipv4, true)}${field('PTR', 'demo.example', true)}</dl>
        </div>
      </section>
      <section class="card xray-card" aria-labelledby="xray-title"><header class="card-header"><h2 id="xray-title"><button class="core-title-trigger" data-action="core-open" aria-label="${t('Xray版本管理', 'Xray version management')}">Xray</button></h2><span class="core-stopped" id="core-home-status">${t('核心管理待连接 · 代理未启动', 'Core management not connected · proxy not started')}</span></header><div class="actions four-actions">
        ${action(t('日志', 'Logs'), 'list', 'xray-log')}${action(t('停止', 'Stop'), 'power', 'stop')}${action(t('重启', 'Restart'), 'rotate-cw', 'restart')}${action(t('切换版本', 'Switch version'), 'wrench', 'core-version')}
      </div></section>
      <section class="card panel-card" aria-labelledby="panel-title"><header class="card-header"><h2 id="panel-title">o-ui <span class="version-tag">${t('原型', 'prototype')}</span></h2></header><div class="actions six-actions">
        ${action(t('日志', 'Logs'), 'list', 'panel-log')}${action(t('配置', 'Config'), 'sliders-horizontal', 'config')}${action(t('备份和恢复', 'Backup and restore'), 'database-backup', 'backup')}${action(t('系统历史', 'System history'), 'chart-area', 'history')}${action(t('Xray 指标', 'Xray metrics'), 'chart-no-axes-combined', 'metrics')}${action(t('检测更新', 'Check for updates'), 'refresh-cw', 'update')}
      </div></section>
    </div>
    <section class="card statistics" aria-label="${t('运行统计（演示）', 'Runtime statistics (demo)')}">
      ${[[t('系统正常运行时间', 'System uptime'), [['Xray', t('3小时', '3h')], ['OS', t('5天14小时', '5d 14h')]]], [t('使用情况', 'Usage'), [[t('内存', 'Memory'), '68.24 MB'], [t('线程', 'Threads'), '32']]], [t('总数据', 'Total data'), [[t('已发送', 'Sent'), '72.18 GB'], [t('已接收', 'Received'), '69.82 GB']]], [t('连接数', 'Connections'), [['TCP', '238'], ['UDP', '10']]]].map(([title, pairs]) => `<div class="stat-group"><h2>${title}</h2><dl>${(pairs as string[][]).map(([label, value]) => `<div><dt>${label}</dt><dd>${value}</dd></div>`).join('')}</dl></div>`).join('')}
    </section>
    </div>
    <div id="workspace-view" hidden></div>
    <div id="core-view" hidden></div>
    <div id="panel-view" hidden></div>
  </main>
  <dialog id="detail-dialog" aria-labelledby="dialog-title" aria-describedby="dialog-description"><div class="dialog-heading"><h2 id="dialog-title"></h2><button class="icon-button" id="close-dialog" aria-label="${t('关闭弹窗', 'Close dialog')}">${icon('x')}</button></div><div class="dialog-body"><p id="dialog-description"></p></div><footer><button class="secondary-button" id="dialog-action" hidden></button><button class="primary-button" id="confirm-dialog">${t('知道了', 'OK')}</button></footer></dialog>
`;
let release: PanelVersion | null = null;
if (authenticated) {
  document.querySelector('.demo-label')!.remove();
  document.querySelector('.demo-notice')!.remove();
  const versionTag = document.querySelector('#panel-title .version-tag')!, sidebarVersion = document.querySelector('.prototype-version')!;
  versionTag.textContent = ''; sidebarVersion.textContent = 'o-ui';
  void api<PanelVersion>('panel/version').then(version => { versionTag.textContent = version.current; sidebarVersion.textContent = version.current; }).catch(() => {});
  void api<PanelVersion>('panel/version?check=1').then(version => { release = version; paintUpdate(); }).catch(() => {});
  document.querySelector('.speed-readings')!.innerHTML = t('<p class="form-intro">网速尚未采集</p>', '<p class="form-intro">Network speed not sampled yet</p>');
  document.querySelector('.traffic-chart')!.innerHTML = t('<p class="form-intro">暂无真实历史记录</p>', '<p class="form-intro">No history yet</p>');
  document.querySelector('.traffic-chart')!.setAttribute('aria-label', t('暂无真实流量采样', 'No traffic samples yet'));
  document.querySelectorAll('.sparkline').forEach(element => element.remove());
  document.querySelectorAll('.resource-copy span, .resource-percent').forEach(element => { element.textContent = '—'; });
  document.querySelectorAll<HTMLElement>('.meter').forEach(element => { element.removeAttribute('aria-valuenow'); element.setAttribute('aria-label', t('尚未采集', 'Not sampled yet')); element.querySelector<HTMLElement>('span')!.style.width = '0'; });
  document.querySelector('#server-title')!.textContent = t('本机服务器 · 信息待接入', 'This server · info pending');
  document.querySelector('.server-description')!.textContent = t('尚未读取系统与运营商信息', 'System and ISP info not loaded yet');
  document.querySelectorAll('.server-card dd, .package-card dd, .statistics dd').forEach(element => { element.textContent = '—'; element.removeAttribute('data-value'); element.classList.remove('private-value'); element.removeAttribute('aria-label'); });
  document.querySelector('.traffic-allowance')!.innerHTML = t('<p class="form-intro">套餐与月流量尚未配置，不使用演示数据填充。</p>', '<p class="form-intro">Plan and monthly traffic are not configured; no demo data is shown.</p>');
  document.querySelectorAll(t('[aria-label*="（演示）"]', '[aria-label*=" (demo)"]')).forEach(element => { element.setAttribute('aria-label', element.getAttribute('aria-label')!.replace(t('（演示）', ' (demo)'), t('（尚未采集）', ' (not sampled yet)'))); });
}

function refreshIcons(): void { createIcons({ icons, attrs: { 'stroke-width': 1.7 } }); }
installInfoTips();

// 主题三档循环：深色 → 浅色 → 跟随系统；没保存过选择时沿用原来的深色默认（用户2026-10-27要求加跟随系统）。
type ThemeChoice = 'dark' | 'light' | 'system';
const themeToggle = document.querySelector<HTMLButtonElement>('#theme-toggle')!;
const systemDark = window.matchMedia('(prefers-color-scheme: dark)');
const nextTheme = (choice: ThemeChoice): ThemeChoice => choice === 'dark' ? 'light' : choice === 'light' ? 'system' : 'dark';
let themeChoice: ThemeChoice = 'dark';
function applyTheme(): void {
  document.documentElement.dataset.theme = themeChoice === 'system' ? systemDark.matches ? 'dark' : 'light' : themeChoice;
  const names: Record<ThemeChoice, string> = { dark: t('深色', 'dark'), light: t('浅色', 'light'), system: t('跟随系统', 'system') };
  const label = `${t('主题：', 'Theme: ')}${names[themeChoice]}${t('，点击切换为', '; switch to ')}${names[nextTheme(themeChoice)]}`;
  themeToggle.setAttribute('aria-label', label); themeToggle.title = label;
  themeToggle.innerHTML = icon(themeChoice === 'system' ? 'sun-moon' : themeChoice === 'dark' ? 'moon' : 'sun');
  refreshIcons();
}
let savedTheme: string | null = null;
try { savedTheme = localStorage.getItem('independent-panel-theme'); } catch { /* 禁用存储不影响主题切换。 */ }
if (savedTheme === 'light' || savedTheme === 'system') themeChoice = savedTheme;
applyTheme();
systemDark.addEventListener('change', () => { if (themeChoice === 'system') applyTheme(); });
themeToggle.addEventListener('click', () => {
  themeChoice = nextTheme(themeChoice);
  applyTheme();
  try { localStorage.setItem('independent-panel-theme', themeChoice); } catch { /* 同上。 */ }
});

// 手机上服务器信息和套餐卡默认收起、只留标题，点箭头展开；桌面始终展开（用户2026-10-27要求）。
const narrow = window.matchMedia('(max-width: 700px)');
document.querySelectorAll<HTMLButtonElement>('.card-collapse').forEach(button => {
  const card = button.closest<HTMLElement>('.card')!, name = button.getAttribute('aria-label')!;
  const set = (collapsed: boolean) => { card.classList.toggle('is-collapsed', collapsed); button.setAttribute('aria-expanded', String(!collapsed)); button.title = `${collapsed ? t('展开', 'Expand ') : t('收起', 'Collapse ')}${name}`; };
  set(narrow.matches);
  button.addEventListener('click', () => set(!card.classList.contains('is-collapsed')));
});

let showPrivate = false;
const privacyToggle = document.querySelector<HTMLButtonElement>('#privacy-toggle')!;
privacyToggle.addEventListener('click', () => {
  showPrivate = !showPrivate;
  privacyToggle.setAttribute('aria-pressed', String(showPrivate));
  privacyToggle.setAttribute('aria-label', `${showPrivate ? t('隐藏', 'Hide') : t('显示', 'Show')} ${t('IP 地址', 'IP address')}`);
  privacyToggle.innerHTML = icon(showPrivate ? 'eye' : 'eye-off');
  document.querySelectorAll<HTMLElement>('.private-value').forEach(element => {
    element.textContent = showPrivate ? element.dataset.value! : '••••••••••••';
    element.classList.toggle('revealed', showPrivate);
    element.setAttribute('aria-label', showPrivate ? element.dataset.value! : t('已隐藏', 'Hidden'));
  });
  refreshIcons();
});

const sidebar = document.querySelector<HTMLElement>('#sidebar')!;
const mobileMenu = document.querySelector<HTMLButtonElement>('#mobile-menu')!;
const scrim = document.querySelector<HTMLButtonElement>('#menu-scrim')!;
const mobileQuery = matchMedia('(max-width: 700px)');
let menuOpen = false;
function syncSidebar(): void {
  sidebar.inert = mobileQuery.matches && !menuOpen;
  document.querySelector<HTMLElement>('#main')!.inert = mobileQuery.matches && menuOpen;
  document.body.classList.toggle('menu-open', mobileQuery.matches && menuOpen);
  scrim.hidden = !(mobileQuery.matches && menuOpen);
  mobileMenu.setAttribute('aria-expanded', String(mobileQuery.matches && menuOpen));
  mobileMenu.setAttribute('aria-label', menuOpen ? t('关闭导航', 'Close navigation') : t('打开导航', 'Open navigation'));
}
function closeMenu(): void { menuOpen = false; syncSidebar(); }
mobileMenu.addEventListener('click', () => {
  menuOpen = !menuOpen;
  syncSidebar();
  if (menuOpen) sidebar.querySelector<HTMLButtonElement>('.nav-item')?.focus();
});
scrim.addEventListener('click', () => { closeMenu(); mobileMenu.focus(); });
document.querySelector('#mobile-close')!.addEventListener('click', () => { closeMenu(); mobileMenu.focus(); });
mobileQuery.addEventListener('change', () => { closeMenu(); });
document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && menuOpen) { closeMenu(); mobileMenu.focus(); }
  if (event.key === 'Tab' && menuOpen && mobileQuery.matches) {
    const focusables = [...sidebar.querySelectorAll<HTMLButtonElement>('button')]
      .filter(button => button.getClientRects().length > 0);
    const first = focusables[0], last = focusables.at(-1)!;
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  }
});
syncSidebar();

const collapseButton = document.querySelector<HTMLButtonElement>('#collapse-sidebar')!;
collapseButton.addEventListener('click', () => {
  const collapsed = document.body.classList.toggle('sidebar-collapsed');
  collapseButton.setAttribute('aria-expanded', String(!collapsed));
  collapseButton.setAttribute('aria-label', collapsed ? t('展开侧栏', 'Expand sidebar') : t('收起侧栏', 'Collapse sidebar'));
  collapseButton.innerHTML = icon(collapsed ? 'panel-left-open' : 'panel-left-close');
  refreshIcons();
});

const dialog = document.querySelector<HTMLDialogElement>('#detail-dialog')!;
const descriptions: Record<string, [string, string]> = {
  'stop': [t('停止 Xray', 'Stop Xray'), t('此处只展示操作入口，没有发送停止指令。接入真实核心后会先确认操作影响，再执行停止。', 'This only shows the action entry; no stop command was sent. With a real core, the impact is confirmed before stopping.')],
  'restart': [t('重启 Xray', 'Restart Xray'), t('此处没有发送重启指令。接入真实核心后会提示连接中断风险并确认，不能把演示反馈当成执行成功。', 'No restart command was sent. With a real core, the interruption risk is shown and confirmed first; demo feedback is not a real result.')],
  'update': [t('面板更新', 'Panel updates'), authenticated ? t('内部测试包不提供在线更新，也不检查更新。更换版本时先正常停止面板，再用新包的 o-ui 命令指定同一数据目录启动（见包内《本机运行说明》）；Xray 核心在版本管理中单独选择，不随面板更新。', 'The panel does not update online or check for updates. To change versions, stop the panel normally, then start the new package\'s o-ui command with the same data directory (see the package\'s run guide). The Xray core is selected separately in version management and is not updated with the panel.') : t('当前原型尚未发版，没有发起更新检查。后续面板更新与 Xray 核心更新分开，不自动覆盖现有核心。', 'This prototype has no releases; no update check was made. Panel updates and Xray core updates are separate and never overwrite the existing core automatically.')],
  'backup': [t('备份和恢复', 'Backup and restore'), t('离线演示不读写真实数据。请登录本机服务使用加密备份与恢复。', 'The offline demo does not read or write real data. Sign in to use encrypted backup and restore.')],
  'config': [t('配置', 'Config'), t('核心配置管理尚未接入。后续采用生成候选、官方核心预检、应用与核验的流程，失败保留上一份可用配置。', 'Core configuration management is not connected here. It generates a candidate, runs the official preflight, applies and verifies, keeping the last working configuration on failure.')],
  'history': [t('系统历史', 'System history'), authenticated ? t('首页显示最近3分钟的面板业务网速，只保存在内存中，面板重启后清空。', 'The dashboard shows proxy traffic speed for the last 3 minutes, kept in memory and cleared when o-ui restarts.') : t('当前趋势是固定演示样本，并非采集结果。', 'The current trend is a fixed demo sample, not collected data.')],
  'metrics': [t('Xray 指标', 'Xray metrics'), authenticated ? t('首页显示本面板 Xray 的内存、线程和 TCP/UDP 连接数，约每2秒更新；客户端流量见客户端页。', 'The dashboard shows memory, threads and TCP/UDP sockets of o-ui\'s Xray, updated about every 2 seconds; client traffic is on the Clients page.') : t('离线预览只显示演示数据，不采集核心指标。', 'The offline preview shows demo data only and collects no core metrics.')],
  'xray-log': [t('Xray 日志', 'Xray logs'), t('离线演示不读取真实日志，请登录本机面板查看脱敏诊断。', 'The offline demo reads no real logs; sign in to view redacted diagnostics.')],
  'panel-log': [t('面板日志', 'Panel logs'), t('离线演示不读取真实日志，请登录本机面板查看本轮操作摘要。', 'The offline demo reads no real logs; sign in to view the operation summary.')],
};
async function checkUpdate(): Promise<void> {
  showDetail(t('检测更新', 'Check for updates'), t('正在检查 GitHub 最新版本…', 'Checking GitHub for the latest release…'));
  let text: string, upgrade = '', notes: string[] = [];
  try {
    const v = await api<PanelVersion>('panel/version?check=1');
    release = v; paintUpdate();
    if (v.updateAvailable) notes = (v.notes ?? []).map(note => lang === 'en' ? note.en : note.zh);
    if (v.issue) text = v.issue;
    else if (v.updateAvailable) {
      text = `${t('发现新版本 ', 'New version ')}${v.latest}${t('（当前 ', ' is available (current ')}${v.current}${t('）。', '). ')}${v.canUpgrade ? t('点“立即升级”在线升级，升级期间面板与代理会中断约半分钟，数据和配置会保留，失败会自动回退。', 'Click Update now to upgrade online; the panel and proxies stop for about half a minute, data and settings are kept, and a failed update rolls back.') : t('请在服务器上运行一次 o-ui update 升级（之后即可在网页上一键升级），数据和配置会保留。', 'Run o-ui update on the server once (after that you can update from the web); data and settings are kept.')}`;
      if (v.canUpgrade) upgrade = v.latest ?? '';
    } else text = `${t('已是最新版本 ', 'You are on the latest version ')}${v.current}${t('。', '.')}`;
  } catch (reason) { text = (reason as Error).message; }
  if (!dialog.open) return;
  document.querySelector('#dialog-description')!.textContent = text;
  // 发现新版本时展示本次更新的主要内容（GitHub发布说明开头的要点）。
  if (notes.length) {
    const box = document.createElement('div'), list = document.createElement('ul'), title = document.createElement('strong');
    box.className = 'release-notes'; title.textContent = t('本次更新', 'In this release');
    for (const note of notes) { const item = document.createElement('li'); item.textContent = note; list.append(item); }
    box.append(title, list);
    document.querySelector('#dialog-description')!.after(box);
  }
  if (upgrade) {
    const action = document.querySelector<HTMLButtonElement>('#dialog-action')!;
    action.textContent = t('立即升级', 'Update now'); action.hidden = false; action.disabled = false;
    // 弹窗里已说明中断与回退，点“立即升级”直接开始，不再弹浏览器确认框（用户2026-10-22要求）。
    action.onclick = () => { dialog.close(); void startUpgrade(upgrade, paintUpdate); };
  }
}
// 首页“o-ui”卡片的升级按钮（参照OUI）：发现新版本时变为橙色“立即升级”，升级中显示进度；失败后恢复。
function paintUpdate(progress: number | null = null): void {
  const button = document.querySelector<HTMLButtonElement>('.panel-card [data-action="update"]');
  if (!button || !release?.updateAvailable || !release.latest) return;
  const label = progress !== null ? `${t('升级中 ', 'Updating ')}${progress}%` : release.canUpgrade ? t('立即升级', 'Update now') : t('有新版本', 'New version');
  button.classList.add('is-update');
  button.disabled = progress !== null;
  button.title = `${t('新版本 ', 'New version ')}${release.latest}`;
  button.innerHTML = `${downloadIcon(progress !== null)}<span>${label}</span>`;
}
async function quickRuntime(action: 'restart' | 'stop'): Promise<void> {
  const stop = action === 'stop';
  if (!await confirmAction(stop ? t('所有代理连接会断开。', 'All proxy connections will drop.') : t('现有连接会短暂中断。', 'Existing connections will drop briefly.'), { title: stop ? t('停止 Xray', 'Stop Xray') : t('重启 Xray', 'Restart Xray'), confirmLabel: stop ? t('停止', 'Stop') : t('重启', 'Restart'), danger: true })) return;
  showDetail(stop ? t('停止 Xray', 'Stop Xray') : t('重启 Xray', 'Restart Xray'), t('正在处理…', 'Working…'));
  let text: string;
  try {
    const status = stop ? await stopCore() : await applySavedConfig();
    text = stop ? t('Xray 已停止。', 'Xray stopped.') : status.running ? `${t('Xray 已重启：', 'Xray restarted: ')}${runtimeStatusText(status)}` : t('没有已启用的入站，Xray 保持停止。', 'No enabled inbounds; Xray stays stopped.');
  } catch (reason) { text = (reason as Error).message; }
  if (dialog.open) document.querySelector('#dialog-description')!.textContent = text;
  void runtime.refresh();
}
let detailReturnFocus: HTMLElement | null = null;
function showDetail(title: string, description: string): void {
  detailReturnFocus = document.activeElement as HTMLElement;
  const action = document.querySelector<HTMLButtonElement>('#dialog-action');
  if (action) { action.hidden = true; action.onclick = null; }
  document.querySelector('#dialog-title')!.textContent = title;
  document.querySelector('#dialog-description')!.textContent = description;
  dialog.querySelector('.release-notes')?.remove();
  dialog.showModal();
}
document.querySelectorAll<HTMLButtonElement>('[data-action]').forEach(button => {
  button.addEventListener('click', () => {
    if (['core-version', 'core-open'].includes(button.dataset.action!)) { closeMenu(); core.openPicker(); return; }
    if (authenticated && button.dataset.action === 'backup') { closeMenu(); backup.open(); return; }
    if (authenticated && ['xray-log', 'panel-log'].includes(button.dataset.action!)) { closeMenu(); logs.open(button.dataset.action === 'xray-log' ? 'xray' : 'panel'); return; }
    if (authenticated && ['restart', 'stop'].includes(button.dataset.action!)) { closeMenu(); void quickRuntime(button.dataset.action as 'restart' | 'stop'); return; }
    if (authenticated && button.dataset.action === 'config') { closeMenu(); runtime.open('apply'); return; }
    if (authenticated && button.dataset.action === 'update') { closeMenu(); void checkUpdate(); return; }
    const [title, description] = descriptions[button.dataset.action!]!;
    closeMenu();
    showDetail(title, description);
  });
});
let latestRuntime: RuntimeStatus | null = null;
let xraySettings: ReturnType<typeof initXraySettings> | null = null;
let management: ReturnType<typeof initManagement> | null = null;
const runtime = initRuntime((status, at) => {
  latestRuntime = status;
  xraySettings?.setRuntime(status, at);
  management?.setRuntime(status, at);
  const label = document.querySelector('#core-home-status');
  if (label) label.textContent = runtimeStatusText(status);
});
const backup = initBackup(refreshIcons);
const logs = initLogs(refreshIcons);
if (authenticated) initMonitor(document.querySelector<HTMLElement>('#home-view')!, () => latestRuntime);
const serverInfo = authenticated ? initServerInfo(document.querySelector<HTMLElement>('#home-view')!, () => showPrivate) : null;
if (authenticated) window.setInterval(() => { if (!document.hidden) void runtime.refresh(); }, 5000);
management = initManagement(document.querySelector('#workspace-view')!, refreshIcons, navigate, showDetail, authenticated, () => { void runtime.refresh(); });
const core = initCore(refreshIcons, status => {
  // 按钮固定写“切换版本”，当前选用的版本放在悬停提示与读屏名称里。
  const button = document.querySelector<HTMLElement>('[data-action="core-version"]');
  const hint = status ? `${t('切换 Xray 版本，当前 v', 'Switch Xray version, current v')}${status.selected}` : t('Xray 版本管理', 'Xray version management');
  if (button) { button.title = hint; button.setAttribute('aria-label', hint); }
});
xraySettings = initXraySettings(document.querySelector('#core-view')!, refreshIcons, authenticated, () => { void quickRuntime('restart'); }, () => { void runtime.refresh(); });
const panelSettings = initPanelSettings(document.querySelector('#panel-view')!, authenticated);
let displayedPage = '';
function navigate(page: string, historyMode: 'push' | 'replace' | 'none' = 'push'): void {
  logs.close();
  if (page !== 'settings') panelSettings.clearPassword();
  if (!['home', 'inbounds', 'clients', 'xray', 'settings'].includes(page)) {
    closeMenu();
    showDetail(page === 'settings' ? t('面板设置', 'Panel settings') : t('Xray 设置', 'Xray settings'), t('此页尚未接入，当前可体验首页、入站列表和客户端。没有读取或修改任何服务器配置。', 'This page is not connected yet; try the dashboard, inbounds and clients. No server configuration was read or changed.'));
    return;
  }
  window.dispatchEvent(new Event('panel-navigate'));
  displayedPage = page;
  closeMenu();
  document.title = `o-ui · ${{ home: t('系统状态', 'Dashboard'), inbounds: t('入站列表', 'Inbounds'), clients: t('客户端', 'Clients'), xray: t('Xray 设置', 'Xray settings'), settings: t('面板设置', 'Panel settings') }[page]}`;
  document.querySelector<HTMLElement>('#home-view')!.hidden = page !== 'home';
  document.querySelector<HTMLElement>('#workspace-view')!.hidden = !['inbounds', 'clients'].includes(page);
  document.querySelector<HTMLElement>('#core-view')!.hidden = page !== 'xray';
  document.querySelector<HTMLElement>('#panel-view')!.hidden = page !== 'settings';
  if (['inbounds', 'clients'].includes(page)) management?.render(page as 'inbounds' | 'clients');
  if (page === 'xray') xraySettings!.render();
  if (page === 'settings') panelSettings.render();
  if (page === 'home') serverInfo?.refresh();
  document.querySelectorAll<HTMLButtonElement>('[data-nav]').forEach(button => {
    const active = button.dataset.nav === page;
    button.classList.toggle('active', active);
    if (active) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  });
  const target = pageURL(page as PanelPage, accessBase, location.protocol === 'file:');
  const current = location.protocol === 'file:' ? location.hash : location.pathname + location.search + location.hash;
  if (historyMode !== 'none' && current !== target) {
    if (historyMode === 'replace') history.replaceState(null, '', target);
    else history.pushState(null, '', target);
  }
  // base指向资源目录，跳转到主要内容仍应留在当前页面。
  document.querySelector<HTMLAnchorElement>('.skip-link')!.href = location.protocol === 'file:' ? '#main' : `${location.pathname}#main`;
  document.querySelector<HTMLElement>('#main')!.focus({ preventScroll: true });
  window.scrollTo(0, 0);
}
document.querySelectorAll<HTMLButtonElement>('[data-nav]').forEach(button => button.addEventListener('click', () => navigate(button.dataset.nav!)));
window.addEventListener('popstate', () => {
  const page = panelPage(location.pathname, location.hash, accessBase);
  if (page !== displayedPage) navigate(page, 'none');
});
window.addEventListener('hashchange', () => {
  if (['home', 'inbounds', 'clients', 'xray', 'settings'].includes(location.hash.slice(1))) navigate(panelPage(location.pathname, location.hash, accessBase), 'replace');
});
navigate(panelPage(location.pathname, location.hash, accessBase), 'replace');
document.querySelector('#close-dialog')!.addEventListener('click', () => dialog.close());
document.querySelector('#confirm-dialog')!.addEventListener('click', () => dialog.close());
dialog.addEventListener('close', () => {
  if (mobileQuery.matches && (detailReturnFocus?.closest('#sidebar') || !detailReturnFocus?.isConnected)) mobileMenu.focus();
});
document.querySelector('#logout')?.addEventListener('click', () => {
  void api('auth/logout', jsonBody('POST', {})).then(() => location.reload()).catch(reason => showDetail(t('退出失败', 'Sign-out failed'), (reason as Error).message));
});
}
window.addEventListener('panel-session-expired', () => {
  document.querySelectorAll<HTMLDialogElement>('dialog[open]').forEach(dialog => dialog.close());
  document.querySelector('#app')?.replaceChildren();
  location.reload();
}, { once: true });
void start();
