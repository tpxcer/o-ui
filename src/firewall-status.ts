import { t } from './i18n.ts';

export type FirewallEndpoint = { state: 'allowed' | 'failed' | 'unverified'; message: string; checkedAt: string };
export type FirewallReport = { workspaceRevision: number; endpoints: Record<string, FirewallEndpoint>; message?: string };

export function firewallStatusText(status?: FirewallEndpoint): string {
  if (!status || status.state === 'unverified') return t('防火墙未确认', 'Firewall unverified');
  if (status.state === 'failed') return t('防火墙操作失败', 'Firewall operation failed');
  return t('上次放行成功', 'Last firewall update succeeded');
}
