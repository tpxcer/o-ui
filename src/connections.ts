import { api, jsonBody } from './api.ts';
import { mountConnectionCard } from './connection-card.ts';
import { mountSubscription } from './subscriptions.ts';
import { certificateNotice } from './connection-notice.ts';
import { infoTip } from './info-tip.ts';
import { escapeHtml as esc, presets } from './workspace.ts';
import type { Inbound } from './workspace.ts';
import './connections.css';
import { t } from './i18n.ts';

interface ConnectionResult { nodes?: ConnectionResult[]; inboundId?: string; name: string; preset: string; version: string; workspaceRevision: number; settingsRevision: number; link: string; certificateTrusted?: boolean }

export function initConnections(refreshIcons: () => void): (id: string, name: string, revision: number, inbounds?: Inbound[]) => void {
  const dialog = document.createElement('dialog');
  dialog.className = 'connection-dialog'; dialog.setAttribute('aria-labelledby', 'connection-title');
  document.body.append(dialog);
  let returnFocus: HTMLElement | null = null, generation = 0;
  function clear(): void { generation++; dialog.replaceChildren(); }
  dialog.addEventListener('close', () => { clear(); if (returnFocus?.isConnected) returnFocus.focus(); returnFocus = null; });
  window.addEventListener('panel-session-expired', () => { if (dialog.open) dialog.close(); clear(); });
  document.addEventListener('visibilitychange', () => { if (document.hidden && dialog.open) dialog.close(); });
  window.addEventListener('hashchange', () => { if (dialog.open) dialog.close(); });
  window.addEventListener('panel-navigate', () => { if (dialog.open) dialog.close(); });
  return (id, name, revision, inbounds = []) => {
    clear(); returnFocus = document.activeElement as HTMLElement;
    dialog.innerHTML = `<header class="dialog-heading"><h2 id="connection-title">${t('客户端连接信息', 'Client connection info')}</h2><button class="icon-button" data-close aria-label="${t('关闭连接信息', 'Close connection info')}"><i data-lucide="x" aria-hidden="true"></i></button></header>
      <div class="connection-body"><div class="connection-security info-tip-host"><strong>${t('含认证凭据，勿公开截图或转发', 'Credentials: do not share or screenshot.')}</strong>${infoTip(t('仅导出当前已应用且正在运行的配置，使用入站的分享主机；未填写时仅回环入站使用监听 IP。分享主机与 SNI 分别保存，外部可达性尚需部署验证；订阅访问范围单独由面板设置决定。关闭弹窗或离开页面后清除显示，不自动复制；剪贴板与已下载的图片不会自动清除。二维码在本机生成，扫码设备必须能访问链接地址；第三方客户端兼容性需实测。', 'Only the applied, running configuration is exported, using the inbound share host (loopback inbounds without one use the listen IP). Share host and SNI are stored separately; external reachability requires deployment testing. Subscription access is controlled in Panel settings. Closing the dialog or leaving the page clears the display; nothing is copied automatically. Clipboard contents and downloaded images are not cleared. QR codes are generated locally; scanning devices must be able to reach the address. Third-party compatibility requires testing.'))}</div>
      <section data-subscription></section><details class="connection-section" data-node-section open><summary>${t('节点链接', 'Node links')}<small class="node-count" data-node-count hidden></small></summary><div class="connection-section-body"><p class="form-hint" data-loading role="status">${t('正在读取已生效配置…', 'Reading the active configuration…')}</p><p class="form-error" data-connection-error role="alert" tabindex="-1" hidden></p><button class="secondary-button" data-retry hidden>${t('重新读取连接信息', 'Reload connection info')}</button><div class="node-tabs" role="tablist" aria-label="${t('关联入站', 'Linked inbounds')}" data-node-tabs hidden></div><div class="node-picker" data-node-picker hidden><button type="button" class="icon-button" data-node-step="-1" aria-label="${t('上一个入站', 'Previous inbound')}" title="${t('上一个入站', 'Previous inbound')}"><i data-lucide="chevron-left" aria-hidden="true"></i></button><select data-node-select aria-label="${t('选择入站', 'Choose inbound')}"></select><button type="button" class="icon-button" data-node-step="1" aria-label="${t('下一个入站', 'Next inbound')}" title="${t('下一个入站', 'Next inbound')}"><i data-lucide="chevron-right" aria-hidden="true"></i></button></div><div data-node-panels></div></div></details><section class="subscription-controls" data-sub-actions></section></div><footer><button class="secondary-button" data-close>${t('关闭', 'Close')}</button></footer>`;
    dialog.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => dialog.close()));
    const retry = dialog.querySelector<HTMLButtonElement>('[data-retry]')!;
    const error = dialog.querySelector<HTMLElement>('[data-connection-error]')!;
    const loading = dialog.querySelector<HTMLElement>('[data-loading]')!;
    const tabs = dialog.querySelector<HTMLElement>('[data-node-tabs]')!, panels = dialog.querySelector<HTMLElement>('[data-node-panels]')!;
    const picker = dialog.querySelector<HTMLElement>('[data-node-picker]')!, chooser = picker.querySelector('select')!;
    let current = 0;
    // 标签页（4个以内）与下拉选择（更多时）共用同一个切换逻辑，每次只显示一个入站的链接与二维码。
    const select = (index: number, focus = false) => {
      const count = panels.children.length;
      current = (index + count) % count; chooser.value = String(current);
      [...panels.children].forEach((panel, position) => { (panel as HTMLElement).hidden = position !== current; });
      tabs.querySelectorAll<HTMLButtonElement>('[role="tab"]').forEach((tab, position) => {
        tab.setAttribute('aria-selected', String(position === current)); tab.tabIndex = position === current ? 0 : -1;
        if (position === current && focus) tab.focus();
      });
    };
    tabs.addEventListener('click', event => {
      const tab = (event.target as Element).closest<HTMLElement>('[role="tab"]');
      if (tab) select(Number(tab.dataset.index));
    });
    tabs.addEventListener('keydown', event => {
      const next = { ArrowRight: current + 1, ArrowLeft: current - 1, Home: 0, End: tabs.children.length - 1 }[event.key];
      if (next === undefined) return;
      event.preventDefault(); select(next, true);
    });
    chooser.addEventListener('change', () => select(Number(chooser.value)));
    picker.querySelectorAll<HTMLButtonElement>('[data-node-step]').forEach(button => button.addEventListener('click', () => select(current + Number(button.dataset.nodeStep))));
    const ticket = generation, alive = () => dialog.open && generation === ticket;
    async function load(): Promise<void> {
      retry.hidden = true; loading.hidden = false; error.hidden = true;
      try {
        const result = await api<ConnectionResult>('runtime/connection', jsonBody('POST', { clientId: id, workspaceRevision: revision }));
        if (!alive()) return;
        // 一个客户端关联多个入站时，4个以内用标签页、更多时用一行下拉切换，弹窗高度不随入站数增长；标题写入站名而不是序号。
        const nodes = result.nodes?.length ? result.nodes : [result], multiple = nodes.length > 1, compact = nodes.length > 4;
        tabs.replaceChildren(); panels.replaceChildren(); chooser.replaceChildren(); tabs.hidden = !multiple || compact; picker.hidden = !compact;
        const count = dialog.querySelector<HTMLElement>('[data-node-count]')!;
        count.textContent = `${nodes.length} ${t('个入站', 'inbounds')}`; count.hidden = !multiple;
        nodes.forEach((node, index) => {
          const inbound = inbounds.find(item => item.id === node.inboundId), title = inbound?.name ?? `${t('链接', 'Link')} ${index + 1}`;
          const panel = document.createElement('div');
          panel.className = 'node-panel'; panel.id = `connection-node-${index}`; panel.hidden = index > 0;
          panel.innerHTML = '<div class="connection-card"></div><div class="connection-details info-tip-host"></div>';
          panels.append(panel);
          const kind = presets[node.preset as keyof typeof presets]?.label ?? node.preset;
          if (compact) chooser.append(new Option(`${index + 1}. ${title} · ${kind}${inbound ? ` · ${inbound.port}` : ''}`, String(index)));
          else if (multiple) {
            const tab = document.createElement('button');
            tab.type = 'button'; tab.className = 'node-tab'; tab.id = `connection-tab-${index}`; tab.dataset.index = String(index); tab.title = title;
            tab.setAttribute('role', 'tab'); tab.setAttribute('aria-controls', panel.id);
            tab.innerHTML = `<i class="preset-dot preset-${esc(node.preset)}" aria-hidden="true"></i><span class="node-tab-name">${esc(title)}</span><small>${esc(kind)}${inbound ? ` · ${inbound.port}` : ''}</small>`;
            tabs.append(tab);
            panel.setAttribute('role', 'tabpanel'); panel.setAttribute('aria-labelledby', tab.id);
          }
          mountConnectionCard(panel.querySelector('.connection-card')!, inbound ? `${name} · ${title}` : name, node.link, alive);
          panel.querySelector('.connection-details')!.innerHTML = `${t('节点说明', 'Node details')}${infoTip(`<p>${t('运行版本', 'Running version')} ${esc(node.version)} ${t('· 入站修订', '· inbound revision')} ${node.workspaceRevision} ${t('· 设置修订', '· settings revision')} ${node.settingsRevision}</p>${node.preset === 'hy2' || node.preset === 'xhttp-tls' ? certificateNotice(node.certificateTrusted) : t('<p>REALITY 需要兼容客户端；服务端默认要求 Xray 26.3.27 或更高版本。此处只有公钥，没有服务端私钥。</p>', '<p>REALITY needs a compatible client; by default the server requires Xray 26.3.27 or later. Only the public key is shown here, never the server\'s private key.</p>')}`)}`;
        });
        if (multiple) select(0);
      } catch (reason) {
        if (!alive()) return;
        error.textContent = (reason as Error).message; error.hidden = false; retry.hidden = false;
      } finally { if (alive()) loading.hidden = true; }
    }
    retry.addEventListener('click', () => { void load(); });
    refreshIcons(); dialog.showModal(); dialog.querySelector<HTMLButtonElement>('[data-close]')!.focus();
    mountSubscription(dialog.querySelector<HTMLElement>('[data-subscription]')!, dialog.querySelector<HTMLElement>('[data-sub-actions]')!, id, name, revision, alive);
    void load();
  };
}
