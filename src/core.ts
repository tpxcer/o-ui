import { escapeHtml as esc } from './workspace.ts';
import { api } from './api.ts';
import { coreCompatibilityWarning } from './core-compatibility.ts';
import './core.css';
import { t } from './i18n.ts';

export const DEFAULT_CORE_VERSION = '26.7.28';
export interface CoreStatus {
  defaultVersion: string; selected: string; previous?: string; installed: string[];
  platform: string; runtime: string; busy: boolean;
}
export interface CoreRelease { version: string; prerelease: boolean; url: string }

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  return api<T>(`core/${path}`, options);
}

export function initCore(refreshIcons: () => void, onStatus: (status: CoreStatus | null) => void): { openPicker: () => void } {
  let status: CoreStatus | null = null;
  let releases: CoreRelease[] = [];
  let selected = DEFAULT_CORE_VERSION;
  let error = '', listError = '', message = '';
  let loading = false, installing = false, checking = false, edited = false;
  const picker = document.createElement('dialog');
  picker.id = 'core-update-dialog'; picker.className = 'core-update-dialog';
  picker.setAttribute('aria-labelledby', 'core-update-title');
  document.body.append(picker);
  let pickerReturnFocus: HTMLElement | null = null;
  picker.addEventListener('close', () => {
    if (pickerReturnFocus?.isConnected && pickerReturnFocus.getClientRects().length) pickerReturnFocus.focus();
  });
  const modal = document.createElement('dialog');
  modal.id = 'core-confirm'; modal.setAttribute('aria-labelledby', 'core-confirm-title');
  document.body.append(modal);
  let pendingVersion = '';
  let returnFocus: HTMLElement | null = null;
  modal.addEventListener('close', () => {
    if (returnFocus?.isConnected && returnFocus.getClientRects().length) returnFocus.focus();
    else if (picker.open) picker.querySelector<HTMLElement>('#core-picker-submit')?.focus();
    else pickerReturnFocus?.focus();
  });

  function paint(): void {
    if (!picker.open) return;
    const active = document.activeElement as HTMLElement | null;
    const focusId = active && picker.contains(active) ? active.id : '';
    const list = releases.map(item => item.version);
    const versions = [...new Set([...list, DEFAULT_CORE_VERSION, ...(status?.installed ?? [])])];
    const busy = installing || status?.busy;
    const scrollTop = picker.querySelector('.core-release-list')?.scrollTop ?? 0;
    picker.innerHTML = `<div class="dialog-heading"><h2 id="core-update-title">${t('Xray 更新', 'Xray update')}</h2><button class="icon-button" id="core-picker-close" aria-label="${t('关闭Xray更新', 'Close Xray update')}">×</button></div>
      <div class="core-picker-body"><section class="core-picker-group" aria-labelledby="core-picker-heading"><h3 id="core-picker-heading"><i data-lucide="wrench" aria-hidden="true"></i>Xray</h3>
      <div class="core-picker-content"><p class="core-warning">${t('请谨慎选择，切换版本可能影响配置及客户端兼容性。预发布版会单独标明。', 'Choose carefully: switching versions can affect configuration and client compatibility. Pre-releases are labeled.')}</p>
      <div class="core-picker-toolbar"><span>${status ? `${t('当前选择 v', 'Selected v')}${esc(status.selected)} ${t('· 运行状态见首页', '· runtime status on the dashboard')}` : t('本机核心服务未连接', 'Core service not connected')}</span><button class="text-button" id="core-picker-refresh" ${loading || checking || installing ? 'disabled' : ''}>${loading ? t('读取官方版本中…', 'Reading official versions…') : t('刷新版本', 'Refresh versions')}</button></div>
      <div class="core-release-list" role="radiogroup" aria-label="${t('Xray官方版本', 'Official Xray versions')}">${versions.map(version => {
        const release = releases.find(item => item.version === version);
        const labels = [version === DEFAULT_CORE_VERSION ? t('默认', 'Default') : '', release ? (release.prerelease ? t('预发布', 'Pre-release') : t('正式版', 'Release')) : '', status?.installed.includes(version) ? t('已安装', 'Installed') : ''].filter(Boolean).join(' · ');
        return `<label class="core-release-row"><span class="core-release-copy"><strong class="core-release-tag ${release?.prerelease ? 'is-prerelease' : ''}">v${esc(version)}</strong><small>${esc(labels)}</small></span><input type="radio" name="core-picker-version" id="core-picker-${esc(version.replaceAll('.', '-'))}" value="${esc(version)}" ${selected === version ? 'checked' : ''} ${busy ? 'disabled' : ''} aria-label="${t('选择v', 'Select v')}${esc(version)}"></label>`;
      }).join('')}</div>
      <label class="form-field core-exact-version"><span id="core-exact-label">${t('其他官方精确版本', 'Other exact official version')}</span><input id="core-exact-version" type="text" maxlength="12" value="${esc(selected)}" aria-labelledby="core-exact-label" aria-describedby="core-exact-hint core-compatibility-warning" autocomplete="off" ${busy ? 'disabled' : ''}></label><p class="form-hint" id="core-exact-hint">${t('列表来自官方最近50项发布记录；也可输入精确版本，安装前再次核实。不自动更新。', 'The list comes from the latest 50 official releases; you can also enter an exact version, which is verified again before install. No automatic updates.')}</p>
      <p class="core-warning" id="core-compatibility-warning" role="status" aria-live="polite" hidden></p>
      ${listError ? `<p class="form-error" role="alert">${esc(listError)}</p>` : ''}${error ? `<p class="form-error" role="alert">${esc(error)}</p>` : ''}
      <p class="core-message" role="status">${esc(message || (checking ? t('正在读取本机核心状态…', 'Reading core status…') : status?.busy ? t('核心版本操作进行中，请稍后刷新。', 'A core version operation is in progress; refresh later.') : ''))}</p>
      </div></section><p class="form-hint">${t('官方来源：XTLS / Xray-core。只管理本机独立核心包，不启动代理、不更新 VPS，保留旧版本。', 'Official source: XTLS / Xray-core. Only o-ui\'s own core packages are managed; no proxy is started and old versions are kept.')}</p></div>
      <footer>${status?.previous && status.installed.includes(status.previous) ? `<button class="text-button" id="core-picker-previous" ${checking || busy ? 'disabled' : ''}>${t('切回 v', 'Switch back to v')}${esc(status.previous)}</button>` : ''}<button class="secondary-button" id="core-picker-cancel">${t('关闭', 'Close')}</button><button class="primary-button" id="core-picker-submit" ${!status || checking || busy ? 'disabled' : ''}>${installing ? t('下载校验中…', 'Downloading and verifying…') : `${t('选择 v', 'Select v')}${esc(selected)}`}</button></footer>`;
    picker.querySelectorAll('#core-picker-close, #core-picker-cancel').forEach(button => button.addEventListener('click', () => picker.close()));
    picker.querySelector('#core-picker-refresh')!.addEventListener('click', () => { void loadReleases(); void loadStatus(); });
    picker.querySelectorAll<HTMLInputElement>('[name="core-picker-version"]').forEach(radio => radio.addEventListener('change', () => {
      selected = radio.value; edited = true; paint();
    }));
    picker.querySelector<HTMLInputElement>('#core-exact-version')!.addEventListener('input', event => {
      selected = (event.target as HTMLInputElement).value; edited = true;
      picker.querySelectorAll<HTMLInputElement>('[name="core-picker-version"]').forEach(radio => { radio.checked = radio.value === selected.replace(/^v/, ''); });
      picker.querySelector('#core-picker-submit')!.textContent = `${t('选择 v', 'Select v')}${selected.replace(/^v/, '')}`;
      updateCompatibility();
    });
    picker.querySelector('#core-picker-submit')!.addEventListener('click', () => confirm(selected));
    picker.querySelector('#core-picker-previous')?.addEventListener('click', () => confirm(status!.previous!));
    picker.querySelector('.core-release-list')!.scrollTop = scrollTop;
    updateCompatibility();
    refreshIcons();
    if (focusId) picker.querySelector<HTMLElement>(`#${focusId}`)?.focus({ preventScroll: true });
  }

  function updateCompatibility(): void {
    const warning = picker.querySelector<HTMLElement>('#core-compatibility-warning')!;
    warning.textContent = coreCompatibilityWarning(selected);
    warning.hidden = !warning.textContent;
  }

  async function loadStatus(): Promise<void> {
    if (checking || installing) return;
    checking = true; error = ''; message = ''; paint();
    try { status = await request<CoreStatus>('status'); if (!edited) selected = status.selected; onStatus(status); }
    catch (reason) { status = null; onStatus(null); error = (reason as Error).message; }
    finally { checking = false; paint(); }
  }

  async function loadReleases(): Promise<void> {
    if (loading) return;
    loading = true; listError = ''; paint();
    try { releases = (await request<{ releases: CoreRelease[] }>('releases')).releases; }
    catch (reason) { listError = `${(reason as Error).message} ${t('仍可输入精确版本重试。', 'You can still enter an exact version and retry.')}`; }
    finally { loading = false; paint(); }
  }

  function confirm(version: string): void {
    if (!status || checking || installing || status.busy) return;
    pendingVersion = version.trim().replace(/^v/, '');
    if (!/^[0-9]{1,3}\.[0-9]{1,2}\.[0-9]{1,2}$/.test(pendingVersion)) { error = t('版本格式应为26.7.28或v26.7.28。', 'Version should look like 26.7.28 or v26.7.28.'); paint(); return; }
    returnFocus = document.activeElement as HTMLElement;
    const release = releases.find(item => item.version === pendingVersion);
    const compatibility = coreCompatibilityWarning(pendingVersion);
    modal.innerHTML = `<div class="dialog-heading"><h2 id="core-confirm-title">${t('确认选择 v', 'Confirm selecting v')}${esc(pendingVersion)}</h2><button class="icon-button" data-cancel aria-label="${t('关闭确认', 'Close confirmation')}">×</button></div><div class="dialog-body"><p>${t('将从 Xray 官方获取匹配本机的版本，校验后写入本项目独立目录并保存选择。已安装版本可直接复用。', 'The matching official Xray package will be downloaded, verified, written to o-ui\'s own directory and selected. Installed versions are reused directly.')}</p><p>${release?.prerelease ? t('此版本被官方标为预发布版。', 'This version is marked as a pre-release by the Xray project. ') : ''}${t('不会启动代理，不会更新 VPS，也不会删除旧版本。', 'No proxy is started and old versions are not deleted.')}</p>${compatibility ? `<p class="core-warning" role="note">${esc(compatibility)}</p>` : ''}</div><footer><button class="secondary-button" data-cancel>${t('取消', 'Cancel')}</button><button class="primary-button" id="core-install-confirm">${t('确认安装并选择', 'Confirm install and select')}</button></footer>`;
    modal.querySelectorAll('[data-cancel]').forEach(button => button.addEventListener('click', () => modal.close()));
    modal.querySelector('#core-install-confirm')!.addEventListener('click', () => { modal.close(); void install(pendingVersion); });
    modal.showModal();
  }

  async function install(version: string): Promise<void> {
    if (!status || checking || installing || status.busy) return;
    installing = true; error = ''; message = t('正在下载官方包、校验SHA256并检查核心版本，请勿重复提交。', 'Downloading the official package, verifying SHA256 and checking the core version; do not submit again.'); paint();
    try {
      status = await request<CoreStatus>('install', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ version }) });
      selected = status.selected; onStatus(status);
      message = `${t('已安装并选择 v', 'Installed and selected v')}${status.selected}${t('。未改变当前运行的核心，需另行应用已保存配置。', '. The running core is unchanged; apply the saved configuration separately.')}`;
    } catch (reason) {
      error = (reason as Error).message; message = t('未确认切换成功，请刷新本机状态。', 'The switch could not be confirmed; refresh the status.');
      try { status = await request<CoreStatus>('status'); onStatus(status); } catch { status = null; onStatus(null); }
    } finally { installing = false; paint(); }
  }

  void loadStatus();
  return {
    openPicker: () => {
      pickerReturnFocus = document.activeElement as HTMLElement;
      if (!installing) { selected = status?.selected ?? DEFAULT_CORE_VERSION; edited = false; error = ''; message = ''; }
      picker.showModal(); paint();
      picker.querySelector<HTMLElement>('#core-picker-close')?.focus();
      void loadStatus(); if (!installing && !releases.length) void loadReleases();
    },
  };
}
