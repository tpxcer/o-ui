import { api } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { allowance, binaryGB, decimalBytes, planMode, planTags, type PlanStatus, type PublicInfo } from './server-plan-model.ts';
import { locale, t } from './i18n.ts';

const hidden = '••••••••••••';

// 首页服务器与套餐卡片：公网信息由后端按需查询并缓存，套餐用量约30秒刷新。
export function initServerInfo(root: HTMLElement, reveal: () => boolean): { refresh: () => void } {
  const serverValues = () => [...root.querySelectorAll<HTMLElement>('.server-card dd')];
  const body = root.querySelector<HTMLElement>('.package-card .card-body')!;
  let publicInfo: PublicInfo | null = null, publicError = '', plan: PlanStatus | null = null, planError = '', disposed = false;

  function setPrivate(element: HTMLElement | undefined, value: string, fallback: string): void {
    if (!element) return;
    element.classList.toggle('private-value', !!value);
    element.classList.toggle('revealed', !!value && reveal());
    if (!value) { element.removeAttribute('data-value'); element.removeAttribute('aria-label'); element.textContent = fallback; return; }
    element.dataset.value = value;
    element.textContent = reveal() ? value : hidden;
    element.setAttribute('aria-label', reveal() ? value : t('已隐藏', 'Hidden'));
  }
  function paintPublic(): void {
    const [v4, v6, location, network] = serverValues();
    const pending = !publicInfo && !publicError;
    const failed = pending ? t('正在查询…', 'Looking up…') : t('查询失败', 'Lookup failed');
    setPrivate(v4, publicInfo?.ipv4?.ip ?? '', publicInfo ? t('查询失败', 'Lookup failed') : failed);
    setPrivate(v6, publicInfo?.ipv6?.ip ?? '', publicInfo ? t('未查询到', 'Not found') : failed);
    const first = publicInfo?.ipv4 ?? publicInfo?.ipv6;
    if (location) location.textContent = first?.location || (publicInfo ? '—' : failed);
    if (network) network.textContent = first?.org || (publicInfo ? '—' : failed);
    const providerValues = [...body.querySelectorAll<HTMLElement>('[data-public]')];
    setPrivate(providerValues[0], publicInfo?.ipv4?.ip ?? '', publicInfo ? t('查询失败', 'Lookup failed') : failed);
    setPrivate(providerValues[1], publicInfo?.ipv4?.hostname ?? '', publicInfo ? t('无', 'None') : failed);
    if (providerValues[1]) providerValues[1].closest<HTMLElement>('.field')!.hidden = !publicInfo?.ipv4?.hostname;
  }
  function field(label: string, value: string, attribute = ''): string {
    return `<div class="field"><dt>${label}</dt><dd${attribute}>${value}</dd></div>`;
  }
  function planDetails(node: string, name: string, tags: string, source: 'provider' | 'public'): string {
    const description = `<span class="package-plan-name">${esc(name || '—')}</span>${tags ? `<div class="package-resources" aria-label="${t('套餐资源', 'Plan resources')}">${tags}</div>` : ''}`;
    return `<dl>${field(t('节点', 'Node'), esc(node || '—'))}${field(t('套餐', 'Plan'), description)}</dl><dl class="package-network">${field(t('服务商 IP', 'Provider IP'), '—', ` data-${source}`)}${field('PTR', '—', ` data-${source}`)}</dl>`;
  }
  // 服务商拉取：本月流量、节点、套餐与IP/PTR都来自服务商接口（参照OUI）。
  function paintProvider(): void {
    const p = plan?.provider;
    let summary = `<p>${esc(plan?.providerError || t('正在从服务商读取…', 'Reading from the provider…'))}</p>`;
    if (p) {
      const quota = Number(p.planMonthlyData), used = Number(p.dataCounter), percent = quota > 0 && p.dataCounter ? Math.min(100, used / quota * 100).toFixed(1) : null;
      const reset = p.dataNextReset > 0 ? new Date(p.dataNextReset * 1000).toLocaleString(locale, { hour12: false, year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—';
      summary = `<div class="allowance-heading"><span>${t('本月流量', 'Traffic this month')}</span><strong>${esc(decimalBytes(p.dataCounter))} / ${quota > 0 ? esc(decimalBytes(p.planMonthlyData)) : t('不限', 'Unlimited')}</strong></div>
        <div class="allowance-progress"><div class="meter" role="progressbar" aria-label="${t('本月流量使用率', 'Monthly traffic usage')}" ${percent === null ? '' : `aria-valuenow="${percent}"`} aria-valuemin="0" aria-valuemax="100"><span style="width:${percent ?? 0}%"></span></div><span>${percent === null ? t('不限', 'Unlimited') : `${percent}%`}</span></div>
        <p>${t('下次重置：', 'Next reset: ')}${esc(reset)} · ${t('数据来自服务商接口', 'From the provider API')}</p>`;
    }
    const tags = p ? [['RAM', p.planRam], ['Disk', p.planDisk], ['Swap', p.planSwap]].filter(([, value]) => binaryGB(value)).map(([name, value]) => `<span class="tag">${name} ${binaryGB(value)}</span>`).join(' ') : '';
    body.innerHTML = `<div class="traffic-allowance">${summary}</div>${planDetails(p?.nodeLocation || p?.nodeAlias || '', p?.plan || '', tags, 'provider')}`;
    const [ips, ptr] = [...body.querySelectorAll<HTMLElement>('[data-provider]')];
    setPrivate(ips, p?.ipAddresses.join(', ') ?? '', '—');
    const hostname = p ? Object.values(p.ptr).filter(Boolean).join(', ') : '';
    setPrivate(ptr, hostname, '—');
    ptr.closest<HTMLElement>('.field')!.hidden = !hostname;
  }
  function paintPlan(): void {
    const mode = planMode(plan);
    body.closest<HTMLElement>('.package-card')!.hidden = !!plan && mode === 'off';
    if (plan && mode === 'provider') { paintProvider(); return; }
    const data = plan?.data, usage = plan?.usage;
    let summary = `<p>${esc(planError || (plan ? t('尚未配置套餐与月流量。可在“面板设置 → 服务器套餐”手动填写；不连接服务商接口。', 'No plan or monthly traffic configured. Fill it in under "Panel settings → Server plan"; no provider API is used.') : t('正在读取套餐…', 'Reading plan…')))}</p>`;
    if (data && usage) {
      const a = allowance(usage), percent = a.percent === null ? null : a.percent.toFixed(1);
      summary = `<div class="allowance-heading"><span>${t('本月流量', 'Traffic this month')}</span><strong>${esc(a.used)} / ${esc(a.total)}</strong></div>
        <div class="allowance-progress"><div class="meter" role="progressbar" aria-label="${t('本月流量使用率', 'Monthly traffic usage')}" ${percent === null ? '' : `aria-valuenow="${percent}"`} aria-valuemin="0" aria-valuemax="100"><span style="width:${percent ?? 0}%"></span></div><span>${percent === null ? t('不限', 'Unlimited') : `${percent}%`}</span></div>
        <p>${t('下次重置：', 'Next reset: ')}${esc(a.reset)}${usage.inexact ? t(' · 本周期用量可能不精确（中途开始统计、重启或停机跨周期），可在设置中校准', ' · usage this period may be inexact (started mid-period, rebooted, or stopped across the reset); calibrate it in settings') : ''}${plan?.issue ? ` · ${esc(plan.issue)}` : ''}</p>`;
    }
    const tags = data ? planTags(data.resources).map(tag => `<span class="tag">${esc(tag)}</span>`).join(' ') : '';
    body.innerHTML = `<div class="traffic-allowance">${summary}</div>${planDetails(data?.node || '', data?.name || '', tags, 'public')}`;
    paintPublic();
  }
  async function loadPublic(): Promise<void> {
    try { publicInfo = await api<PublicInfo>('system/public'); publicError = ''; }
    catch (reason) { publicError = (reason as Error).message; }
    if (!disposed && root.isConnected) paintPublic();
  }
  async function loadPlan(): Promise<void> {
    try { plan = await api<PlanStatus>('system/plan'); planError = ''; }
    catch (reason) { planError = `${t('套餐读取失败：', 'Failed to read plan: ')}${(reason as Error).message}`; }
    if (!disposed && root.isConnected) paintPlan();
  }
  function tick(): void {
    if (!root.isConnected) { disposed = true; return; }
    if (!document.hidden && !root.hidden) void loadPlan();
    window.setTimeout(tick, 30000);
  }
  paintPlan();
  void loadPublic();
  tick();
  return { refresh: () => { void loadPlan(); } };
}
