import { t } from './i18n.ts';

export type FirewallEndpoint = { state: 'allowed' | 'failed' | 'unverified'; message: string; checkedAt: string; operation?: 'allow' | 'delete' | 'redirect-add' | 'redirect-delete' };
export type FirewallReport = { workspaceRevision: number; endpoints: Record<string, FirewallEndpoint>; message?: string };

export function oldFirewallCleanupAllowed(status: FirewallEndpoint): boolean {
  // 旧版未记录操作类型；只用于拒绝转发记录，不从历史文案推导任何删除参数。
  return (!status.operation || status.operation === 'delete') && !status.message.includes('\u7aef\u53e3\u8df3\u8dc3') && !status.message.includes('Port hopping');
}

export function firewallStatusText(status?: FirewallEndpoint): string {
  if (!status || status.state === 'unverified') return t('防火墙未确认', 'Firewall unverified');
  if (status.state === 'failed') return t('防火墙操作失败', 'Firewall operation failed');
  return t('上次放行成功', 'Last firewall update succeeded');
}
