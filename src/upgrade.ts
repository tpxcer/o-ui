import { api, jsonBody } from './api.ts';
import { t } from './i18n.ts';
import './upgrade.css';

export type PanelVersion = { current: string; latest?: string; url?: string; notes?: { zh: string; en: string }[]; updateAvailable: boolean; issue?: string; canUpgrade?: boolean; update?: { state: string; from: string; to: string; code: number; at: number } };

// 下载图标（参照OUI）：圆底白箭头落入托盘，升级进行中箭头循环落下。
export const downloadIcon = (active: boolean) => `<span class="download-icon${active ? ' is-active' : ''}" aria-hidden="true"><svg class="download-arrow" viewBox="0 0 24 24"><path d="M12 4v12M6.5 10.5 12 16l5.5-5.5"/></svg><span class="download-tray"></span></span>`;

// 进度参照OUI：从8%起每0.7秒走完到95%剩余距离的12%（至少1%），确认新版本已运行后才到100%。
export function nextProgress(value: number): number {
  return value >= 95 ? value : Math.min(95, value + Math.max(1, Math.round((95 - value) * .12)));
}

// 参照OUI的网页更新：服务器在独立的临时服务里升级，面板会重启；升级期间锁定页面显示进度，
// 轮询版本号直到换成新版本后刷新。onProgress收到当前百分比，失败或超时时收到null。
export async function startUpgrade(target: string, onProgress: (percent: number | null) => void): Promise<void> {
  const dialog = document.createElement('dialog');
  dialog.className = 'upgrade-dialog';
  dialog.setAttribute('aria-labelledby', 'upgrade-title');
  dialog.innerHTML = `${downloadIcon(true)}<strong id="upgrade-title"></strong><div class="upgrade-progress" role="progressbar" aria-labelledby="upgrade-title" aria-valuemin="0" aria-valuemax="100"><span></span></div><small aria-hidden="true"></small><p role="status"></p><button type="button" class="secondary-button" hidden>${t('关闭', 'Close')}</button>`;
  const title = dialog.querySelector('strong')!, bar = dialog.querySelector<HTMLElement>('.upgrade-progress')!, percent = dialog.querySelector('small')!, note = dialog.querySelector('p')!, close = dialog.querySelector('button')!;
  let progress = 8, running = true;
  const paint = () => {
    bar.firstElementChild!.setAttribute('style', `width:${progress}%`);
    bar.setAttribute('aria-valuenow', String(progress));
    percent.textContent = `${progress}%`;
    onProgress(progress);
  };
  const timer = setInterval(() => { progress = nextProgress(progress); paint(); }, 700);
  const fail = (text: string) => {
    running = false; clearInterval(timer);
    dialog.classList.add('is-failed');
    dialog.querySelector('.download-icon')!.classList.remove('is-active');
    title.textContent = t('升级没有完成', 'The update did not finish');
    note.textContent = text; close.hidden = false; close.focus();
    onProgress(null);
  };
  close.onclick = () => dialog.close();
  dialog.addEventListener('cancel', event => { if (running) event.preventDefault(); });
  dialog.addEventListener('close', () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
  title.textContent = `${t('正在升级到 ', 'Updating to ')}${target}`;
  note.textContent = t('正在启动升级…', 'Starting the update…');
  paint();
  const since = Math.floor(Date.now() / 1000) - 5;
  try { await api('panel/update', jsonBody('POST', { confirm: true })); }
  catch (reason) { fail((reason as Error).message); return; }
  note.textContent = t('面板与代理会重启，约需 1 分钟，请勿关闭页面', 'The panel and proxies restart; this takes about a minute. Keep this page open');
  const started = Date.now();
  const poll = async (): Promise<void> => {
    try {
      const v = await api<PanelVersion>('panel/version');
      if (v.current === target) {
        running = false; clearInterval(timer); progress = 100; paint();
        title.textContent = `${t('已升级到 ', 'Updated to ')}${target}`;
        note.textContent = t('正在刷新页面…', 'Reloading…');
        setTimeout(() => location.reload(), 800);
        return;
      }
      if (v.update && v.update.state === 'failed' && v.update.at >= since) { fail(`${t('升级失败（代码 ', 'The update failed (code ')}${v.update.code}${t('），仍在使用 ', '); still on ')}${v.current}${t('。请在服务器上查看 /var/lib/o-ui-update/output.log，或运行 o-ui update。', '. Check /var/lib/o-ui-update/output.log on the server, or run o-ui update.')}`); return; }
    } catch { /* 面板重启期间暂时不可达 */ }
    if (Date.now() - started < 5 * 60_000) setTimeout(() => { void poll(); }, 3000);
    else fail(t('5 分钟内没有完成升级。请在服务器上查看 /var/lib/o-ui-update/output.log，或运行 o-ui update。', 'The update did not finish within 5 minutes. Check /var/lib/o-ui-update/output.log on the server, or run o-ui update.'));
  };
  setTimeout(() => { void poll(); }, 3000);
}
