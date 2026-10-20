import { t } from './i18n.ts';

// 订阅全局设置模型（参照OUI订阅页）；单个客户端的订阅链接仍在连接信息中生成。
export interface SubscriptionSettings { enabled: boolean; updateHours: number; title: string; showInfo: boolean; remarkClientName: boolean }
export interface SubscriptionStatus { revision: number; data: SubscriptionSettings }
export interface SubscriptionForm { enabled: boolean; title: string; updateHours: string; showInfo: boolean; remarkClientName: boolean }

export function subscriptionForm(data: SubscriptionSettings): SubscriptionForm {
  return { enabled: data.enabled, title: data.title, updateHours: String(data.updateHours), showInfo: data.showInfo, remarkClientName: data.remarkClientName };
}

export function subscriptionRequest(form: SubscriptionForm): SubscriptionSettings {
  const hoursText = form.updateHours.trim(), hours = Number(hoursText);
  if (!/^\d+$/.test(hoursText) || hours < 1 || hours > 168) throw new Error(t('订阅更新间隔须为1至168小时', 'Subscription update interval must be 1–168 hours'));
  const title = form.title.trim();
  if ([...title].length > 64 || /[\u0000-\u001f\u007f]/.test(title)) throw new Error(t('订阅标题最多64个字符，不能包含控制字符', 'Subscription title must be at most 64 characters without control characters'));
  return { enabled: form.enabled, updateHours: hours, title, showInfo: form.showInfo, remarkClientName: form.remarkClientName };
}
