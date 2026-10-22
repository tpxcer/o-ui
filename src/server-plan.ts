import { escapeHtml as esc } from './workspace.ts';
import { billingLabels, decimalBytes, planMode, planModeLabels, type PlanMode, type PlanStatus } from './server-plan-model.ts';
import { settingRow, selectControl, textControl } from './settings-ui.ts';
import { t } from './i18n.ts';

// 已保存套餐对应的表单值；本周期校准每次保存后清空。
export function planForm(status: PlanStatus | null): Record<string, string> {
  const plan = status?.data;
  return { planMode: planMode(status), node: plan?.node ?? '', name: plan?.name ?? '', resources: plan?.resources ?? '', quotaGB: String(plan?.quotaGB ?? 0), resetDay: String(plan?.resetDay ?? 1), billing: plan?.billing ?? 'both', interfaces: plan?.interfaces.join(', ') ?? '', usedGB: '', providerURL: plan?.providerURL ?? '', providerVEID: plan?.providerVEID ?? '', providerKey: '' };
}

// “常规”页的服务器套餐分组：先选方式（关闭/服务商拉取/手动输入），下面的设置行随方式变化，取值与校验沿用 parsePlanForm。
export function planRows(values: Record<string, string>, status: PlanStatus | null, interfaces: string[]): string {
  return settingRow(t('套餐信息来源', 'Plan source'), t('关闭：首页不显示套餐；服务商拉取：从 VPS 商家接口读取套餐、流量与节点（参照 OUI）；手动输入：自己填写并按网卡统计月流量', 'Off: no plan on the dashboard; Pull from provider: read plan, traffic and node from your VPS provider (like OUI); Manual: fill it in and count monthly traffic on the NICs'), selectControl('planMode', values.planMode, Object.entries(planModeLabels), t('套餐信息来源', 'Plan source')))
    + `<div data-plan-fields>${planFields(values, status, interfaces)}</div>`;
}

export function planFields(values: Record<string, string>, status: PlanStatus | null, interfaces: string[]): string {
  const mode = values.planMode as PlanMode;
  if (mode === 'off') return '';
  if (mode === 'provider') return [
    settingRow(t('拉取链接', 'Provider URL'), t('留空使用搬瓦工（64clouds）接口；也可填兼容的接口，会自动附加 veid 和 api_key，或用 {veid}、{api_key} 占位', 'Empty uses the BandwagonHost (64clouds) API; a compatible API also works. veid and api_key are appended automatically, or use {veid} and {api_key} placeholders'), textControl('providerURL', values.providerURL, t('拉取链接', 'Provider URL'), 'maxlength="500" placeholder="https://api.64clouds.com/v1/getServiceInfo"')),
    settingRow('VEID', '', textControl('providerVEID', values.providerVEID, 'VEID', 'maxlength="64"')),
    settingRow('API KEY', status?.hasProviderKey ? t('已保存；留空保存会沿用当前密钥，输入新值才会替换', 'Saved; leave empty to keep it, or enter a new one to replace it') : t('服务商提供的 API KEY', 'The API KEY from your provider'), textControl('providerKey', values.providerKey, 'API KEY', `type="password" maxlength="200" autocomplete="new-password" placeholder="${status?.hasProviderKey ? '••••••••' : ''}"`)),
  ].join('') + (status?.providerError ? `<p class="form-hint danger-text">${esc(status.providerError)}</p>` : '');
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
