import { calendarDays, chineseDate, escapeHtml } from './workspace.ts';
import { t } from './i18n.ts';

const months = t('一月,二月,三月,四月,五月,六月,七月,八月,九月,十月,十一月,十二月', 'January,February,March,April,May,June,July,August,September,October,November,December').split(',');
const weekdays = t('日,一,二,三,四,五,六', 'Su,Mo,Tu,We,Th,Fr,Sa').split(',');
const monthLabel = (year: number, month: number) => t(`${year}年${month + 1}月`, `${months[month]} ${year}`);
const pad = (value: number) => String(value).padStart(2, '0');

export function initCalendar(root: HTMLElement, initial: number | null, refreshIcons: () => void): { value: () => number | null } {
  let selected = initial;
  let cursor = new Date(initial ?? Date.now());
  let open = false;
  const render = () => {
    const date = new Date(selected ?? Date.now());
    const today = new Date();
    const year = cursor.getFullYear(), month = cursor.getMonth();
    root.innerHTML = `<div class="expiry-controls"><button type="button" class="secondary-button expiry-trigger" aria-expanded="${open}" aria-controls="expiry-calendar"><i data-lucide="calendar-days" aria-hidden="true"></i><span>${escapeHtml(chineseDate(selected))}</span></button>
      <button type="button" class="text-button" data-date="clear">${t('永不过期', 'Never expires')}</button></div>
      <small class="form-hint">${t('按当前浏览器本机时间显示和保存。', 'Shown and saved in this browser\'s local time.')}</small>
      <div id="expiry-calendar" class="calendar"${open ? '' : ' hidden'} aria-label="${t('中文到期日历', 'Expiry calendar')}">
        <div class="calendar-heading"><button type="button" class="icon-button" data-date="prev" aria-label="${t('上个月', 'Previous month')}">‹</button><strong aria-live="polite">${t(`${year}年 ${months[month]}`, `${months[month]} ${year}`)}</strong><button type="button" class="icon-button" data-date="next" aria-label="${t('下个月', 'Next month')}">›</button></div>
        <div class="calendar-grid" aria-label="${monthLabel(year, month)}">${weekdays.map(day => `<span class="weekday">${day}</span>`).join('')}
          ${calendarDays(year, month).map(day => day === null ? '<span></span>' : `<button type="button" data-day="${day}"${year === today.getFullYear() && month === today.getMonth() && day === today.getDate() ? ' aria-current="date"' : ''} aria-label="${t(`${year}年${month + 1}月${day}日`, `${months[month]} ${day}, ${year}`)}" aria-pressed="${selected !== null && year === date.getFullYear() && month === date.getMonth() && day === date.getDate()}">${day}</button>`).join('')}</div>
        <div class="calendar-time"><label for="expiry-hour">${t('时间', 'Time')}</label><select id="expiry-hour" aria-label="${t('到期小时', 'Expiry hour')}">${Array.from({ length: 24 }, (_, hour) => `<option value="${hour}"${hour === date.getHours() ? ' selected' : ''}>${pad(hour)} ${t('时', 'h')}</option>`).join('')}</select><select id="expiry-minute" aria-label="${t('到期分钟', 'Expiry minute')}">${Array.from({ length: 60 }, (_, minute) => `<option value="${minute}"${minute === date.getMinutes() ? ' selected' : ''}>${pad(minute)} ${t('分', 'min')}</option>`).join('')}</select></div>
        <div class="date-shortcuts">${[7, 30, 90].map(days => `<button type="button" class="text-button" data-days="${days}">${t(`${days}天后`, `In ${days} days`)}</button>`).join('')}<button type="button" class="text-button" data-date="done">${t('完成', 'Done')}</button></div>
      </div>`;
    refreshIcons();
  };
  root.addEventListener('click', event => {
    const button = (event.target as Element).closest<HTMLButtonElement>('button');
    if (!button) return;
    if (button.classList.contains('expiry-trigger')) {
      open = !open;
      if (open) cursor = new Date(selected ?? Date.now());
      render(); root.querySelector<HTMLButtonElement>(open ? selected === null ? '[aria-current="date"]' : '[aria-pressed="true"]' : '.expiry-trigger')!.focus(); return;
    }
    if (button.dataset.day) {
      const date = new Date(selected ?? Date.now());
      selected = new Date(cursor.getFullYear(), cursor.getMonth(), Number(button.dataset.day), date.getHours(), date.getMinutes()).getTime();
      render(); root.querySelector<HTMLButtonElement>(`[data-day="${button.dataset.day}"]`)!.focus(); return;
    }
    if (button.dataset.days) {
      const date = new Date(); date.setDate(date.getDate() + Number(button.dataset.days)); date.setSeconds(0, 0);
      selected = date.getTime(); cursor = date;
    }
    switch (button.dataset.date) {
      case 'prev': cursor = new Date(cursor.getFullYear(), cursor.getMonth() - 1, 1); break;
      case 'next': cursor = new Date(cursor.getFullYear(), cursor.getMonth() + 1, 1); break;
      case 'clear': selected = null; cursor = new Date(); open = false; break;
      case 'done': open = false; break;
    }
    render();
    root.querySelector<HTMLButtonElement>(open && button.dataset.date && ['prev', 'next'].includes(button.dataset.date) ? `[data-date="${button.dataset.date}"]` : '.expiry-trigger')!.focus();
  });
  root.addEventListener('change', event => {
    if (!(event.target instanceof HTMLSelectElement)) return;
    const date = new Date(selected ?? new Date(cursor.getFullYear(), cursor.getMonth(), cursor.getDate()).getTime());
    date.setHours(Number(root.querySelector<HTMLSelectElement>('#expiry-hour')!.value), Number(root.querySelector<HTMLSelectElement>('#expiry-minute')!.value), 0, 0);
    selected = date.getTime();
    root.querySelector('.expiry-trigger span')!.textContent = chineseDate(selected);
  });
  render();
  return { value: () => selected };
}
