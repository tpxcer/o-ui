import { t } from './i18n.ts';

// 长说明收进ⓘ：桌面悬停或键盘聚焦即显示，触屏点按切换（用户2026-10-27要求）。说明框铺满宿主行（宿主加info-tip-host），手机上不溢出。
// html由调用方转义好；同一页可放多个。
let count = 0;
export function infoTip(html: string, label = t('说明', 'Details')): string {
  const id = `info-tip-${++count}`;
  return `<span class="info-tip"><button type="button" class="info-tip-button" aria-label="${label}" aria-expanded="false" aria-describedby="${id}"><span aria-hidden="true">i</span></button><span class="info-tip-text" id="${id}" role="tooltip">${html}</span></span>`;
}

export function installInfoTips(): void {
  document.addEventListener('click', event => {
    const button = (event.target as Element).closest<HTMLButtonElement>('.info-tip-button');
    document.querySelectorAll<HTMLButtonElement>('.info-tip-button[aria-expanded="true"]').forEach(open => { if (open !== button) open.setAttribute('aria-expanded', 'false'); });
    button?.setAttribute('aria-expanded', String(button.getAttribute('aria-expanded') !== 'true'));
  });
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape') document.querySelectorAll('.info-tip-button[aria-expanded="true"]').forEach(open => open.setAttribute('aria-expanded', 'false'));
  });
}
