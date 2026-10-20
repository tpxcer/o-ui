import { escapeHtml as esc } from './workspace.ts';
import { billingLabels, decimalBytes, type PlanStatus } from './server-plan-model.ts';
import { settingRow, selectControl, textControl } from './settings-ui.ts';
import { t } from './i18n.ts';

// 已保存套餐对应的表单值；本周期校准每次保存后清空。
export function planForm(status: PlanStatus | null): Record<string, string> {
  const plan = status?.data;
  return { node: plan?.node ?? '', name: plan?.name ?? '', resources: plan?.resources ?? '', quotaGB: String(plan?.quotaGB ?? 0), resetDay: String(plan?.resetDay ?? 1), billing: plan?.billing ?? 'both', interfaces: plan?.interfaces.join(', ') ?? '', usedGB: '' };
}

// “常规”页的服务器套餐分组：按当前表单值生成设置行，取值与校验沿用 parsePlanForm。
export function planRows(values: Record<string, string>, status: PlanStatus | null, interfaces: string[]): string {
  const usage = status?.usage;
  const used = usage ? `${t('本周期已用', 'Used this period')} ${decimalBytes(usage.used)}${usage.inexact ? t('（可能不精确）', ' (may be inexact)') : ''}` : t('尚未配置，保存后从当前开始累计', 'Not configured; counting starts when you save');
  return [
    settingRow(t('节点位置', 'Node location'), t('显示在首页，如 US, Los Angeles', 'Shown on the dashboard, e.g. US, Los Angeles'), textControl('node', values.node, t('节点位置', 'Node location'), 'maxlength="120"')),
    settingRow(t('套餐名称', 'Plan name'), t('如 KVM 2核 / 2GB / 40GB', 'e.g. KVM 2 vCPU / 2GB / 40GB'), textControl('name', values.name, t('套餐名称', 'Plan name'), 'maxlength="120"')),
    settingRow(t('套餐资源', 'Plan resources'), t('逗号分隔，如 RAM 2 GB, Disk 40 GB', 'Comma separated, e.g. RAM 2 GB, Disk 40 GB'), textControl('resources', values.resources, t('套餐资源', 'Plan resources'), 'maxlength="120"')),
    settingRow(t('月流量上限', 'Monthly quota'), t('单位 GB，0 表示不限', 'In GB, 0 means unlimited'), textControl('quotaGB', values.quotaGB, t('月流量上限', 'Monthly quota'), 'type="number" min="0" max="1000000" step="any"')),
    settingRow(t('每月重置日', 'Monthly reset day'), t('1–31，当月没有该日按月末', '1–31; the last day if the month is shorter'), textControl('resetDay', values.resetDay, t('每月重置日', 'Monthly reset day'), 'type="number" min="1" max="31" step="1"')),
    settingRow(t('计费方向', 'Billing direction'), '', selectControl('billing', values.billing, Object.entries(billingLabels), t('计费方向', 'Billing direction'))),
    settingRow(t('计量网卡', 'Metered interfaces'), `${t('留空为全部活动网卡；当前：', 'Empty means all active interfaces; current: ')}${interfaces.length ? interfaces.join(', ') : t('未读取到', 'not available')}`, textControl('interfaces', values.interfaces, t('计量网卡', 'Metered interfaces'), 'maxlength="200"')),
    settingRow(t('本周期已用校准', 'Usage calibration'), `${used}${t('；可填服务商面板显示的已用量（GB），留空不校准', '; you can enter the usage shown by your provider (GB), empty skips calibration')}`, textControl('usedGB', values.usedGB, t('本周期已用校准', 'Usage calibration'), 'type="number" min="0" max="1000000" step="any"')),
  ].join('') + (status?.issue ? `<p class="form-hint">${esc(status.issue)}</p>` : '');
}
