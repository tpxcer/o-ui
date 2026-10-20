import { api, APIError, jsonBody } from './api.ts';
import { escapeHtml as esc } from './workspace.ts';
import { billingLabels, decimalBytes, parsePlanForm, type PlanStatus } from './server-plan-model.ts';
import { t } from './i18n.ts';

export function initServerPlanSettings(root: HTMLElement): { render: () => void } {
  let status: PlanStatus | null = null, interfaces: string[] = [], loading = false, saving = false, reloadRequired = false, error = '', message = '';
  function controls(focus = false): void {
    root.querySelectorAll<HTMLInputElement | HTMLSelectElement>('input, select').forEach(element => { element.disabled = !status || loading || saving; });
    const save = root.querySelector<HTMLButtonElement>('#plan-save');
    if (save) { save.disabled = !status || loading || saving || reloadRequired; save.textContent = saving ? t('正在保存…', 'Saving…') : t('保存服务器套餐', 'Save server plan'); }
    const reload = root.querySelector<HTMLButtonElement>('[data-plan-reload]');
    if (reload) reload.disabled = loading || saving;
    const alert = root.querySelector<HTMLElement>('[role="alert"]');
    if (alert) { alert.textContent = error; alert.hidden = !error; if (focus && error) alert.focus(); }
    const feedback = root.querySelector<HTMLElement>('[role="status"]');
    if (feedback) feedback.textContent = loading ? t('正在读取…', 'Reading…') : saving ? t('正在保存…', 'Saving…') : message;
  }
  function paint(): void {
    const plan = status?.data, usage = status?.usage;
    const text = (id: string, label: string, value: string, attributes: string) => `<label class="form-field" for="plan-${id}">${label}<input id="plan-${id}" name="${id}" value="${esc(value)}" autocomplete="off" ${attributes}></label>`;
    root.innerHTML = `<h2 class="xray-editor-title">${t('服务器套餐', 'Server plan')}</h2>
      <p class="core-warning">${t('仅用于首页显示与本月流量统计，由你手动填写，不连接服务商接口。用量按本机网卡计数累计并保存；面板未运行时如服务器重启，期间流量无法补算，会标为“不精确”，可填写服务商面板显示的已用量校准。', 'Used only for the dashboard and monthly traffic, filled in by you; no provider API is used. Usage is accumulated from this server\'s interface counters and saved. If the server reboots while o-ui is not running, that traffic cannot be recovered and the period is marked "inexact"; you can calibrate it with the usage shown by your provider.')}</p>
      <p class="form-intro">${usage ? `${t('本周期已用', 'Used this period')} ${esc(decimalBytes(usage.used))}${t('（面板实测上传', '(measured upload')} ${esc(decimalBytes(usage.sent))} ${t('· 下载', '· download')} ${esc(decimalBytes(usage.received))}${t('，其余为校准值）', ', the rest is calibration)')}${usage.inexact ? t('，可能不精确', ', may be inexact') : ''}${t('。', '. ')}` : t('尚未配置，保存后从当前开始累计。', 'Not configured; accumulation starts when you save.')}${status?.issue ? ` ${esc(status.issue)}` : ''}</p>
      <form id="plan-form">${text('node', t('节点位置', 'Node location'), plan?.node ?? '', t('maxlength="120" placeholder="如：US, Los Angeles"', 'maxlength="120" placeholder="e.g. US, Los Angeles"'))}
      ${text('name', t('套餐名称', 'Plan name'), plan?.name ?? '', t('maxlength="120" placeholder="如：KVM 2核 / 2GB / 40GB"', 'maxlength="120" placeholder="e.g. KVM 2 vCPU / 2GB / 40GB"'))}
      ${text('resources', t('套餐资源（逗号分隔）', 'Plan resources (comma separated)'), plan?.resources ?? '', t('maxlength="120" placeholder="如：RAM 2 GB, Disk 40 GB"', 'maxlength="120" placeholder="e.g. RAM 2 GB, Disk 40 GB"'))}
      ${text('quotaGB', t('月流量上限（GB，0 表示不限）', 'Monthly quota (GB, 0 = unlimited)'), String(plan?.quotaGB ?? 0), 'type="number" min="0" max="1000000" step="any" required')}
      ${text('resetDay', t('每月重置日（1–31，当月没有该日按月末）', 'Monthly reset day (1–31; last day if the month is shorter)'), String(plan?.resetDay ?? 1), 'type="number" min="1" max="31" step="1" required')}
      <label class="form-field" for="plan-billing">${t('计费方向', 'Billing direction')}<select id="plan-billing" name="billing">${Object.entries(billingLabels).map(([value, label]) => `<option value="${value}" ${(plan?.billing ?? 'both') === value ? 'selected' : ''}>${label}</option>`).join('')}</select></label>
      ${text('interfaces', t('计量网卡（留空为全部活动非回环网卡）', 'Metered interfaces (empty = all active non-loopback interfaces)'), plan?.interfaces.join(', ') ?? '', t('maxlength="200" placeholder="如：eth0"', 'maxlength="200" placeholder="e.g. eth0"'))}
      <p class="form-hint">${t('当前活动网卡：', 'Active interfaces: ')}${interfaces.length ? esc(interfaces.join(t('、', ', '))) : t('未读取到', 'not available')}${t('。容器或隧道等虚拟网卡会重复计算同一流量，建议只填写公网网卡。修改重置日、计费方向或网卡后，本周期会标为不精确。', '. Container or tunnel interfaces count the same traffic twice, so list only the public interface. Changing the reset day, billing direction or interfaces marks this period as inexact.')}</p>
      ${text('usedGB', t('本周期已用校准（GB，可选）', 'Usage calibration for this period (GB, optional)'), '', t('type="number" min="0" max="1000000" step="any" placeholder="留空则不校准"', 'type="number" min="0" max="1000000" step="any" placeholder="Leave empty to skip"'))}
      <p class="form-error" role="alert" tabindex="-1" hidden></p><p class="core-message" role="status"></p>
      <button id="plan-save" class="primary-button">${t('保存服务器套餐', 'Save server plan')}</button></form>
      <button type="button" class="secondary-button" data-plan-reload>${t('重新读取套餐设置', 'Reload plan settings')}</button>`;
    controls();
    root.querySelector('#plan-form')!.addEventListener('submit', event => {
      event.preventDefault(); if (!status || loading || saving || reloadRequired) return;
      const values = Object.fromEntries([...new FormData(event.target as HTMLFormElement)].map(([key, value]) => [key, String(value)]));
      let parsed: ReturnType<typeof parsePlanForm>;
      try { parsed = parsePlanForm(values); } catch (reason) { error = `${(reason as Error).message}${t('；输入已保留。', '; your input is kept.')}`; controls(true); return; }
      saving = true; error = ''; message = ''; controls();
      void api<PlanStatus>('system/plan', jsonBody('PUT', { revision: status.revision, data: parsed.plan, usedGB: parsed.usedGB })).then(next => {
        status = next; message = parsed.usedGB === null ? t('套餐设置已保存。', 'Plan settings saved.') : t('套餐设置已保存，本周期用量已按校准值重新起算。', 'Plan settings saved; usage for this period restarts from the calibration value.');
      }).catch(reason => {
        reloadRequired = !(reason instanceof APIError) || reason.status === 409 || reason.status >= 500;
        error = `${(reason as Error).message}${t('；输入已保留。', '; your input is kept.')}${reloadRequired ? t('请先复制需要保留的输入，再重新读取套餐设置确认结果，不要重复提交旧修订。', ' Copy anything you want to keep, then reload the plan settings to confirm the result; do not resubmit the old revision.') : t('请修正后重试。', ' Please correct it and retry.')}`;
      }).finally(() => { saving = false; if (root.isConnected) { if (error) controls(true); else paint(); } });
    });
    root.querySelector('[data-plan-reload]')!.addEventListener('click', () => { void load(); });
  }
  async function load(): Promise<void> {
    if (loading || saving) return;
    if (status && !window.confirm(t('重新读取会丢弃未保存的套餐设置，请先复制需要保留的内容。不重启面板，是否继续？', 'Reloading discards unsaved plan settings; copy anything you want to keep first. o-ui will not restart. Continue?'))) return;
    loading = true; error = ''; message = ''; controls();
    let loaded = false;
    try {
      status = await api<PlanStatus>('system/plan'); reloadRequired = false; loaded = true;
      try { interfaces = (await api<{ interfaces: string[] }>('system/status')).interfaces ?? []; } catch { interfaces = []; }
    } catch (reason) { error = `${(reason as Error).message}${t('；原输入仍保留。', '; your previous input is kept.')}`; }
    finally { loading = false; if (root.isConnected) { if (loaded) paint(); else controls(true); } }
  }
  return { render() { if (!root.innerHTML) paint(); if (!status) void load(); } };
}
