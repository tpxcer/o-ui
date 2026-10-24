import { escapeHtml as esc, type Workspace } from './workspace.ts';
import { buildSettings, defaultSettings, freedomStrategies, routingStrategies } from './xray-settings-model.ts';
import type { SettingsDraft } from './xray-settings-model.ts';
import { api, APIError, confirmSaved, jsonBody, uncertainSave } from './api.ts';
import type { RuntimeStatus } from './runtime.ts';
import { settingsStatusText } from './settings-status.ts';
import { settingRow, settingSection, settingsToolbar, selectControl, textControl, toggle } from './settings-ui.ts';
import {
  balancerForm, balancerFromForm, balancerStrategies, blockDomainOptions, blockIPOptions, directDomainOptions, directIPOptions, dnsPresets, dnsServerForm,
  dnsServerFromForm, emptyBalancer, emptyDNSServer, emptyOutbound, emptyRule, ensureOutbound, outboundAddress, outboundForm, outboundFromForm, outboundFromLink, outboundReverseTag,
  lines, outboundProtocolOptions, parseParts, proxyProtocols, ruleForm, ruleFromForm, ruleSummary, ruleValues, setRuleValues, ssMethods, streamProtocols, syncObservatory, writeParts,
  type BalancerForm, type DNSServerForm, type Json, type OutboundForm, type Parts,
} from './xray-config.ts';
import './xray-settings.css';
import { t } from './i18n.ts';
import { confirmAction } from './confirm.ts';

type Tab = 'basic' | 'routing' | 'outbounds' | 'balancers' | 'dns' | 'advanced';
type AdvancedPart = 'rules' | 'outbounds' | 'balancers' | 'dns' | 'advanced' | 'preview';
type Traffic = Record<string, { uplink: string; downlink: string }>;

const tabs: [Tab, string][] = [['basic', t('基础配置', 'Basics')], ['routing', t('路由规则', 'Routing rules')], ['outbounds', t('出站', 'Outbounds')], ['balancers', t('负载均衡', 'Balancers')], ['dns', 'DNS'], ['advanced', t('高级', 'Advanced')]];
const advancedParts: [AdvancedPart, string][] = [['rules', t('路由规则', 'Routing rules')], ['outbounds', t('出站', 'Outbounds')], ['balancers', t('负载均衡', 'Balancers')], ['dns', 'DNS'], ['advanced', t('其他', 'Other')], ['preview', t('完整配置预览', 'Full config preview')]];
const button = (label: string, attrs: string, kind = 'secondary-button') => `<button type="button" class="${kind}" ${attrs}>${label}</button>`;
const bytes = (value: string | number | undefined) => { let n = Number(value ?? 0), unit = 0; while (n >= 1024 && unit < 4) { n /= 1024; unit++; } return `${n.toFixed(unit ? 2 : 0)} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][unit]}`; };

export function initXraySettings(root: HTMLElement, icons: () => void, persistent = false, onApply: () => void = () => {}, onSaved: () => void = () => {}): { render: () => void; setRuntime: (status: RuntimeStatus | null, at: number) => void } {
  let draft = defaultSettings(), saved = structuredClone(draft), revision = 0, tab: Tab = 'basic', part: AdvancedPart = 'rules';
  let loaded = !persistent, loading = false, saving = false, reloadRequired = false, visible = false, error = '', feedback = '', jsonError = '';
  let runtime: RuntimeStatus | null = null, runtimeAt = 0, traffic: Traffic = {};
  let workspace: Workspace = { inbounds: [], clients: [] };
  const dialog = document.createElement('dialog');
  dialog.className = 'edit-dialog xray-dialog';
  document.body.append(dialog);
  const dirty = () => JSON.stringify(draft) !== JSON.stringify(saved);
  const reverseTags = () => workspace.clients.flatMap(client => client.reverseTag ? [client.reverseTag] : []);
  const parts = (): Parts | null => { try { return parseParts(draft); } catch { return null; } };
  function change(mutate: (current: Parts) => Parts): void {
    const current = parts();
    if (!current) return;
    draft = writeParts(draft, mutate(current));
    feedback = ''; error = '';
    paint();
  }
  function statusText(): string {
    if (loading) return t('正在读取…', 'Loading…');
    if (!loaded) return t('设置尚未读取。', 'Settings are not loaded yet.');
    if (saving) return t('正在保存并应用…', 'Saving and applying…');
    return [dirty() ? t('有未保存修改，点“保存”后生效。', 'There are unsaved changes; click Save to apply.') : '', feedback, settingsStatusText(persistent, revision, runtime, runtimeAt)].filter(Boolean).join(' ');
  }
  function updateStatus(): void {
    const label = root.querySelector<HTMLElement>('#xray-settings-status');
    if (label && label.textContent !== statusText()) label.textContent = statusText();
    const save = root.querySelector<HTMLButtonElement>('#xray-save');
    if (save) save.disabled = !loaded || saving || loading || reloadRequired || !dirty();
  }

  // ---- 基础配置 ----
  function multi(key: string, options: [string, string][], values: string[]): string {
    const known = new Set(options.map(([value]) => value)), custom = values.filter(value => !known.has(value));
    const chosen = options.filter(([value]) => values.includes(value)).map(([, label]) => label).concat(custom);
    return `<details class="multi-select"><summary>${chosen.length ? esc(chosen.join(t('、', ', '))) : t('未选择', 'None')}</summary><div class="multi-options">${options.map(([value, label]) => `<label class="check-field"><input type="checkbox" data-multi="${esc(key)}" value="${esc(value)}" ${values.includes(value) ? 'checked' : ''}><span>${esc(label)}</span></label>`).join('')}<label class="form-field">${t('自定义（每行一项）', 'Custom (one per line)')}<textarea data-multi-custom="${esc(key)}" rows="2">${esc(custom.join('\n'))}</textarea></label></div></details>`;
  }
  function basicTab(p: Parts): string {
    const values = (tag: string, property: string) => ruleValues(p.rules, tag, property);
    return settingSection(t('常规配置', 'General'), [
      settingRow(t('Freedom 策略', 'Freedom strategy'), t('直连出站的域名解析策略', 'Domain resolution strategy of direct outbounds'), selectControl('freedomStrategy', draft.freedomStrategy, freedomStrategies.map(value => [value, value]), 'Freedom')),
      settingRow(t('路由域名策略', 'Routing domain strategy'), t('路由匹配时的域名解析方式', 'How domains are resolved when matching routes'), selectControl('routingStrategy', draft.routingStrategy, routingStrategies.map(value => [value, value]), t('路由域名策略', 'Routing domain strategy'))),
      settingRow(t('出站测试 URL', 'Outbound test URL'), t('负载均衡测速使用的地址', 'URL used by balancer health checks'), textControl('probeURL', draft.probeURL, t('出站测试 URL', 'Outbound test URL'), 'type="url"')),
    ].join('')) + settingSection(t('日志', 'Logs'), [
      settingRow(t('日志级别', 'Log level'), t('调试级别可能包含连接信息', 'Debug may include connection details'), selectControl('logLevel', draft.logLevel, ['none', 'error', 'warning', 'info', 'debug'].map(value => [value, value]), t('日志级别', 'Log level'))),
      settingRow(t('访问日志', 'Access log'), t('记录每个连接，输出到核心日志', 'Record each connection in the core log'), toggle('accessLog', draft.accessLog, t('访问日志', 'Access log'))),
      settingRow(t('DNS 日志', 'DNS log'), t('记录 DNS 查询', 'Record DNS queries'), toggle('dnsLog', draft.dnsLog, t('DNS 日志', 'DNS log'))),
    ].join('')) + settingSection(t('基础路由', 'Basic routing'), [
      settingRow(t('屏蔽 BitTorrent', 'Block BitTorrent'), t('阻止 BT 下载流量', 'Block BitTorrent traffic'), toggle('torrent', values('block', 'protocol').includes('bittorrent'), 'BitTorrent', 'data-basic-torrent')),
      settingRow(t('屏蔽 IP', 'Blocked IPs'), t('匹配的目标 IP 直接阻断', 'Matching destination IPs are blocked'), multi('block:ip', blockIPOptions, values('block', 'ip'))),
      settingRow(t('屏蔽域名', 'Blocked domains'), t('匹配的域名直接阻断（如广告）', 'Matching domains are blocked (e.g. ads)'), multi('block:domain', blockDomainOptions, values('block', 'domain'))),
      settingRow(t('直连 IP', 'Direct IPs'), t('匹配的目标 IP 走直连', 'Matching destination IPs go direct'), multi('direct:ip', directIPOptions, values('direct', 'ip'))),
      settingRow(t('直连域名', 'Direct domains'), t('匹配的域名走直连', 'Matching domains go direct'), multi('direct:domain', directDomainOptions, values('direct', 'domain'))),
    ].join(''));
  }

  // ---- 表格页 ----
  const actions = (kind: string, index: number, count: number) => `<div class="row-actions">${button('↑', `data-op="${kind}:up" data-index="${index}" aria-label="${t('上移', 'Move up')}" ${index === 0 ? 'disabled' : ''}`, 'icon-button')}${button('↓', `data-op="${kind}:down" data-index="${index}" aria-label="${t('下移', 'Move down')}" ${index === count - 1 ? 'disabled' : ''}`, 'icon-button')}${button(t('编辑', 'Edit'), `data-op="${kind}:edit" data-index="${index}"`, 'text-button')}${button(t('删除', 'Delete'), `data-op="${kind}:delete" data-index="${index}"`, 'text-button danger-text')}</div>`;
  const table = (heads: string[], rows: string[], empty: string) => rows.length ? `<div class="table-scroll"><table class="xray-table"><thead><tr>${heads.map(head => `<th scope="col">${head}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table></div>` : `<p class="form-intro">${empty}</p>`;
  function routingTab(p: Parts): string {
    return `<div class="settings-table-actions">${button(t('添加规则', 'Add rule'), 'data-op="rule:add"', 'primary-button')}</div><p class="form-hint">${t('从上到下匹配，先命中的规则生效；都不匹配时走第一个出站。', 'Rules match top to bottom; the first match wins. Unmatched traffic uses the first outbound.')}</p>` +
      table(['#', t('匹配条件', 'Conditions'), t('目标', 'Target'), t('操作', 'Actions')], p.rules.map((rule, index) => `<tr><td>${index + 1}</td><td>${esc(ruleSummary(rule))}</td><td><span class="tag-chip">${esc(String(rule.balancerTag ? `${t('均衡', 'Balancer')} · ${rule.balancerTag}` : rule.outboundTag ?? ''))}</span></td><td>${actions('rule', index, p.rules.length)}</td></tr>`), t('还没有路由规则，所有流量走第一个出站。', 'No routing rules yet; all traffic uses the first outbound.'));
  }
  function outboundsTab(p: Parts): string {
    return `<div class="settings-table-actions">${button(t('添加出站', 'Add outbound'), 'data-op="outbound:add"', 'primary-button')}${persistent ? button('WARP', 'data-op="warp:open"', 'secondary-button') : ''}</div><p class="form-hint">${t('第一个出站是默认出站。', 'The first outbound is the default.')}</p>` +
      table(['#', t('标识', 'Tag'), t('协议', 'Protocol'), t('地址', 'Address'), t('流量', 'Traffic'), t('操作', 'Actions')], p.outbounds.map((item, index) => {
        const used = traffic[String(item.tag)];
        return `<tr><td>${index + 1}${index === 0 ? ` <small class="table-secondary">${t('默认', 'default')}</small>` : ''}</td><td>${esc(String(item.tag ?? ''))}</td><td><span class="tag-chip">${esc(String(item.protocol ?? ''))}</span></td><td>${esc(outboundAddress(item))}</td><td>${used ? `↑ ${bytes(used.uplink)} / ↓ ${bytes(used.downlink)}` : '—'}</td><td>${actions('outbound', index, p.outbounds.length)}</td></tr>`;
      }), t('至少需要一个出站。', 'At least one outbound is required.'));
  }
  function balancersTab(p: Parts): string {
    const strategy = (item: Json) => balancerStrategies.find(([key]) => key === balancerForm(item).strategy)?.[1] ?? balancerForm(item).strategy;
    return `<div class="settings-table-actions">${button(t('添加负载均衡', 'Add balancer'), `data-op="balancer:add" ${p.outbounds.length ? '' : 'disabled'}`, 'primary-button')}</div><p class="form-hint">${t('在路由规则里选择负载均衡作为目标才会生效。', 'A balancer takes effect once a routing rule targets it.')}</p>` +
      table([t('标识', 'Tag'), t('策略', 'Strategy'), t('出站', 'Outbounds'), t('回退', 'Fallback'), t('操作', 'Actions')], p.balancers.map((item, index) => `<tr><td>${esc(String(item.tag ?? ''))}</td><td>${esc(strategy(item))}</td><td>${esc(balancerForm(item).selector.join(', '))}</td><td>${esc(balancerForm(item).fallbackTag || '—')}</td><td>${actions('balancer', index, p.balancers.length)}</td></tr>`), t('还没有负载均衡。', 'No balancers yet.'));
  }
  function dnsTab(p: Parts): string {
    const servers = Array.isArray(p.dns.servers) ? p.dns.servers : [];
    return settingSection(t('常规', 'General'), [
      settingRow(t('查询策略', 'Query strategy'), t('UseIP 同时查询 IPv4 与 IPv6', 'UseIP queries both IPv4 and IPv6'), selectControl('dns-queryStrategy', String(p.dns.queryStrategy ?? 'UseIP'), [['UseIP', 'UseIP'], ['UseIPv4', 'UseIPv4'], ['UseIPv6', 'UseIPv6']], t('查询策略', 'Query strategy'))),
      settingRow(t('禁用缓存', 'Disable cache'), t('每次都向上游查询', 'Always query upstream'), toggle('dns-disableCache', p.dns.disableCache === true, t('禁用缓存', 'Disable cache'))),
      settingRow(t('禁用回退', 'Disable fallback'), t('不回退到其他服务器查询', 'Do not fall back to other servers'), toggle('dns-disableFallback', p.dns.disableFallback === true, t('禁用回退', 'Disable fallback'))),
    ].join('')) + `<div class="settings-table-actions">${button(t('添加 DNS 服务器', 'Add DNS server'), 'data-op="dns:add"', 'primary-button')}<label class="filter-control"><span class="sr-only">${t('添加预设', 'Add preset')}</span><select data-dns-preset><option value="">${t('添加常用 DNS…', 'Add a common DNS…')}</option>${dnsPresets.map(([label, value]) => `<option value="${esc(value)}">${esc(label)}</option>`).join('')}</select></label></div>` +
      table([t('地址', 'Address'), t('端口', 'Port'), t('域名', 'Domains'), t('期望 IP', 'Expected IPs'), t('操作', 'Actions')], servers.map((server, index) => { const form = dnsServerForm(server); return `<tr><td>${esc(form.address)}</td><td>${esc(form.port || '—')}</td><td>${esc(form.domains.split('\n').filter(Boolean).join(', ') || '—')}</td><td>${esc(form.expectIPs.split('\n').filter(Boolean).join(', ') || '—')}</td><td>${actions('dns', index, servers.length)}</td></tr>`; }), t('至少需要一个 DNS 服务器。', 'At least one DNS server is required.'));
  }
  function advancedTab(): string {
    const key = part === 'preview' ? '' : part;
    let text = key ? draft[key] : '';
    if (!key) { try { text = JSON.stringify(buildSettings(draft, reverseTags()), null, 2); } catch (reason) { text = (reason as Error).message; } }
    return `<div class="segmented" role="radiogroup" aria-label="${t('配置部分', 'Config part')}">${advancedParts.map(([id, label]) => `<label><input type="radio" name="advanced-part" value="${id}" ${id === part ? 'checked' : ''}><span>${label}</span></label>`).join('')}</div>
      <p class="form-hint">${part === 'preview' ? t('根据当前设置生成的配置片段（不含入站），只读。', 'Generated config fragment (inbounds excluded), read-only.') : t('直接编辑这一部分的 JSON；其他标签页会同步显示。', 'Edit this part as JSON; the other tabs show the same data.')}</p>
      <textarea id="xray-json-editor" class="json-editor" rows="22" spellcheck="false" ${part === 'preview' ? 'readonly' : ''} aria-label="JSON">${esc(text)}</textarea>${jsonError ? `<p class="form-error" role="alert">${esc(jsonError)}</p>` : ''}`;
  }

  function paint(): void {
    if (!visible) return;
    const p = parts();
    const body = tab === 'advanced' ? advancedTab() : !p ? `<p class="form-error" role="alert">${t('当前 JSON 无效，请在“高级”中修正。', 'The JSON is invalid; fix it under Advanced.')}</p>` : { basic: basicTab, routing: routingTab, outbounds: outboundsTab, balancers: balancersTab, dns: dnsTab }[tab](p);
    root.innerHTML = `<header class="page-heading"><div><h1 class="page-title" tabindex="-1">${t('Xray 设置', 'Xray settings')}</h1><p>${t('路由、出站、负载均衡与 DNS', 'Routing, outbounds, balancers and DNS')}</p></div></header>
      ${settingsToolbar(`${button(saving ? t('正在保存…', 'Saving…') : t('保存', 'Save'), `id="xray-save" ${!loaded || saving || loading || reloadRequired || !dirty() ? 'disabled' : ''}`, 'primary-button')}${persistent ? button(t('重启 Xray', 'Restart Xray'), `id="xray-restart" ${saving || loading ? 'disabled' : ''}`, 'danger-button') + button(t('刷新', 'Refresh'), `id="xray-reload" ${loading || saving ? 'disabled' : ''}`) : ''}`, persistent ? t('修改需点“保存”，保存后立即生效（自动预检并重启 Xray）。', 'Click Save to apply; saving runs a preflight and restarts Xray.') : t('离线演示：修改只保留到页面刷新。', 'Offline demo: changes last until the page refreshes.'))}
      <p class="core-message" id="xray-settings-status" role="status">${esc(statusText())}</p>
      ${error ? `<p class="form-error" id="xray-settings-error" role="alert" tabindex="-1">${esc(error)}</p>` : ''}
      ${reloadRequired ? `<p class="form-error" role="alert">${t('上次保存失败或结果未确认，请先刷新再修改。', 'The last save failed or was not confirmed; refresh before editing.')}</p>` : ''}
      <section class="card xray-settings-card"><div class="xray-settings-tabs" role="tablist" aria-label="${t('Xray 设置分类', 'Xray setting categories')}">${tabs.map(([id, label]) => `<button type="button" role="tab" id="xray-tab-${id}" aria-selected="${id === tab}" data-settings-tab="${id}">${label}</button>`).join('')}</div>
      <form id="xray-settings-form" novalidate><fieldset class="xray-edit-fields" ${!loaded || saving || loading ? 'disabled' : ''}>${body}</fieldset></form></section>`;
    icons();
  }

  // ---- 弹窗 ----
  function openDialog(title: string, fields: string, submit: (form: HTMLFormElement) => void, onInput?: (form: HTMLFormElement) => void, labels: [string, string] = [t('取消', 'Cancel'), t('确定', 'OK')]): void {
    dialog.innerHTML = `<form novalidate><header class="dialog-heading"><h2>${esc(title)}</h2><button type="button" class="icon-button" data-close aria-label="${t('关闭弹窗', 'Close dialog')}">×</button></header><div class="edit-body">${fields}<p class="form-error" role="alert" hidden></p></div><footer>${button(labels[0], 'data-close')}<button class="primary-button">${labels[1]}</button></footer></form>`;
    const form = dialog.querySelector('form')!;
    dialog.querySelectorAll('[data-close]').forEach(item => item.addEventListener('click', () => dialog.close()));
    if (onInput) form.addEventListener('change', () => onInput(form));
    form.addEventListener('submit', event => {
      event.preventDefault();
      const message = form.querySelector<HTMLElement>('.form-error')!;
      try { submit(form); dialog.close(); paint(); }
      catch (reason) { message.textContent = (reason as Error).message; message.hidden = false; }
    });
    dialog.showModal();
  }
  const value = (form: HTMLFormElement, name: string) => String(new FormData(form).get(name) ?? '').trim();
  const checked = (form: HTMLFormElement, name: string) => new FormData(form).getAll(name).map(String);
  const field = (label: string, control: string, hint = '') => `<label class="form-field">${label}${control}${hint ? `<small class="form-hint">${hint}</small>` : ''}</label>`;
  const input = (name: string, current: string, attrs = '') => `<input name="${name}" value="${esc(current)}" autocomplete="off" ${attrs}>`;
  const area = (name: string, current: string, rows = 3) => `<textarea name="${name}" rows="${rows}">${esc(current)}</textarea>`;
  const select = (name: string, current: string, options: [string, string][]) => `<select name="${name}">${options.map(([key, label]) => `<option value="${esc(key)}" ${key === current ? 'selected' : ''}>${esc(label)}</option>`).join('')}</select>`;
  const checks = (name: string, current: string[], options: [string, string][]) => `<div class="check-grid">${options.map(([key, label]) => `<label class="check-field"><input type="checkbox" name="${name}" value="${esc(key)}" ${current.includes(key) ? 'checked' : ''}><span>${esc(label)}</span></label>`).join('')}</div>`;
  function jsonDialog(title: string, item: unknown, save: (value: Json) => void): void {
    openDialog(title, field(t('该条目含表单不支持的字段，请直接编辑 JSON', 'This entry has fields the form does not support; edit the JSON'), `<textarea name="json" rows="14" class="json-editor" spellcheck="false">${esc(JSON.stringify(item, null, 2))}</textarea>`), form => {
      let parsed: unknown;
      try { parsed = JSON.parse(value(form, 'json')); } catch { throw new Error(t('JSON 格式无效。', 'Invalid JSON.')); }
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error(t('必须是 JSON 对象。', 'Must be a JSON object.'));
      save(parsed as Json);
    });
  }

  async function ruleDialog(index: number | null): Promise<void> {
    // 每次打开都重新读取入站与客户端，保证可选项是最新的。
    if (persistent) { try { workspace = (await api<{ data: Workspace | null }>('workspace')).data ?? workspace; } catch { /* 读取失败时沿用已有列表。 */ } }
    const p = parts(); if (!p) return;
    const existing = index === null ? emptyRule() : ruleForm(p.rules[index]);
    // 入站标签按用户要求只能选一个；已有多个入站标签的规则改用JSON编辑，避免保存时丢掉其余标签。
    if (!existing || existing.inboundTag.length > 1) { jsonDialog(t('编辑路由规则', 'Edit routing rule'), p.rules[index!], next => change(current => ({ ...current, rules: current.rules.map((rule, i) => i === index ? next : rule) }))); return; }
    const inbounds: [string, string][] = [...workspace.inbounds.map(item => [`in-${item.id}`, item.name] as [string, string]), ...p.outbounds.map(outboundReverseTag).filter(Boolean).map(tag => [tag, `${t('反向代理', 'Reverse')} · ${tag}`] as [string, string])];
    const users: [string, string][] = workspace.clients.map(item => [`client-${item.id}`, item.name]);
    const none = (label: string): [string, string] => ['', `(${label})`];
    // 布局与字段顺序参照OUI的路由规则弹窗：标签在左、输入在右（手机上下排列），多项用逗号分隔。
    const comma = t('多个项目请用逗号分隔', 'Separate multiple items with commas');
    const help = (text: string) => `<span class="rule-help" title="${esc(text)}" aria-label="${esc(text)}">?</span>`;
    const row = (label: string, control: string, hint = '') => `<label class="rule-row"><span class="rule-label">${label}${hint ? help(hint) : ''}</span>${control}</label>`;
    const group = (label: string, control: string) => `<div class="rule-row is-top" role="group" aria-label="${esc(label)}"><span class="rule-label">${label}</span>${control}</div>`;
    const chips = (name: string, current: string[], options: [string, string][]) => `<div class="rule-chips">${options.map(([key, label]) => `<label class="check-field"><input type="checkbox" name="${name}" value="${esc(key)}" ${current.includes(key) ? 'checked' : ''}><span>${esc(label)}</span></label>`).join('')}</div>`;
    const attrRow = ([name, value]: [string, string]) => `<div class="attr-row"><span class="attr-index"></span><input name="attrName" value="${esc(name)}" placeholder="${t('名称', 'Name')}" aria-label="${t('属性名称', 'Attribute name')}" autocomplete="off"><input name="attrValue" value="${esc(value)}" placeholder="${t('值', 'Value')}" aria-label="${t('属性值', 'Attribute value')}" autocomplete="off">${button('−', `data-attr-remove aria-label="${t('删除属性', 'Remove attribute')}"`, 'icon-button bordered')}</div>`;
    const ip = 'placeholder="0.0.0.0/8, fc00::/7, geoip:cn"', ports = 'placeholder="53,443,1000-2000"';
    openDialog(index === null ? t('+ 路由规则', '+ Routing rule') : t('编辑路由规则', 'Edit routing rule'), `<div class="rule-form">${[
      row(t('源 IP', 'Source IP'), input('sourceIP', existing.sourceIP, ip), comma),
      row(t('源端口', 'Source port'), input('sourcePort', existing.sourcePort, ports), comma),
      row(t('VLESS 路由', 'VLESS route'), input('vlessRoute', existing.vlessRoute, ports), comma),
      row(t('网络', 'Network'), select('network', existing.network, [none(t('任意', 'Any')), ['tcp', 'TCP'], ['udp', 'UDP'], ['tcp,udp', 'TCP,UDP']])),
      group(t('协议', 'Protocol'), chips('protocol', existing.protocol, [['http', 'http'], ['tls', 'tls'], ['bittorrent', 'bittorrent'], ['quic', 'quic']])),
      group(t('属性', 'Attributes'), `<div class="attr-rows" data-attr-rows>${existing.attrs.map(attrRow).join('')}${button('+', `data-attr-add aria-label="${t('添加属性', 'Add attribute')}"`, 'icon-button bordered')}</div>`),
      row('IP', input('ip', existing.ip, ip), comma),
      row(t('域名', 'Domain'), input('domain', existing.domain, 'placeholder="google.com, geosite:cn"'), comma),
      users.length ? group(t('用户', 'User'), chips('userTag', lines(existing.user), users)) : '',
      row(t('端口', 'Port'), input('port', existing.port, ports), comma),
      inbounds.length ? row(t('入站标签', 'Inbound tag'), select('inboundTag', existing.inboundTag[0] ?? '', [none(t('无', 'None')), ...inbounds])) : '',
      row(t('出站标签', 'Outbound tag'), select('outboundTag', existing.outboundTag, [none(t('无', 'None')), ...p.outbounds.map(item => [String(item.tag), String(item.tag)] as [string, string]), ...reverseTags().map(tag => [tag, `${t('反向代理', 'Reverse')} · ${tag}`] as [string, string])])),
      row(t('负载均衡标签', 'Balancer tag'), select('balancerTag', existing.balancerTag, [none(t('无', 'None')), ...p.balancers.map(item => [String(item.tag), String(item.tag)] as [string, string])]), t('通过已配置的负载均衡转发流量', 'Route traffic through a configured balancer')),
    ].join('')}</div>`, form => {
      const data = new FormData(form), names = data.getAll('attrName').map(String), values = data.getAll('attrValue').map(String);
      const rule = ruleFromForm({ sourceIP: value(form, 'sourceIP'), sourcePort: value(form, 'sourcePort'), vlessRoute: value(form, 'vlessRoute'), network: value(form, 'network'), protocol: checked(form, 'protocol'), attrs: names.map((name, i) => [name, values[i] ?? ''] as [string, string]), ip: value(form, 'ip'), domain: value(form, 'domain'), user: checked(form, 'userTag').join(','), port: value(form, 'port'), inboundTag: value(form, 'inboundTag') ? [value(form, 'inboundTag')] : [], outboundTag: value(form, 'outboundTag'), balancerTag: value(form, 'balancerTag') });
      change(current => ({ ...current, rules: index === null ? [...current.rules, rule] : current.rules.map((item, i) => i === index ? rule : item) }));
    }, undefined, [t('关闭', 'Close'), index === null ? t('创建', 'Create') : t('保存', 'Save')]);
    // 属性行可增删，序号随之重排。
    const rows = dialog.querySelector<HTMLElement>('[data-attr-rows]')!;
    const renumber = () => rows.querySelectorAll('.attr-index').forEach((cell, i) => { cell.textContent = String(i + 1); });
    renumber();
    rows.addEventListener('click', event => {
      const target = event.target as HTMLElement;
      if (target.closest('[data-attr-add]')) { target.closest('[data-attr-add]')!.insertAdjacentHTML('beforebegin', attrRow(['', ''])); renumber(); rows.querySelectorAll<HTMLInputElement>('[name="attrName"]').item(rows.querySelectorAll('.attr-row').length - 1).focus(); }
      if (target.closest('[data-attr-remove]')) { target.closest('.attr-row')!.remove(); renumber(); }
    });
  }

  function outboundFields(form: OutboundForm): string {
    const proxy = proxyProtocols.includes(form.protocol), stream = streamProtocols.includes(form.protocol), hy2 = form.protocol === 'hysteria';
    return [
      `<div class="import-row">${field(t('从分享链接导入', 'Import from share link'), input('link', '', 'placeholder="vless:// vmess:// trojan:// ss:// hysteria2://"'))}${button(t('导入', 'Import'), 'data-import')}</div>`,
      field(t('协议', 'Protocol'), select('protocol', form.protocol, outboundProtocolOptions)),
      field(t('标识（tag）', 'Tag'), input('tag', form.tag, 'required maxlength="128"')),
      form.protocol === 'freedom' ? field(t('域名策略', 'Domain strategy'), select('domainStrategy', form.domainStrategy, [['', t('跟随基础配置', 'Follow Basics')], ...freedomStrategies.map(item => [item, item] as [string, string])])) : '',
      proxy ? `<div class="field-row">${field(t('地址', 'Address'), input('address', form.address, 'required'))}${field(t('端口', 'Port'), input('port', form.port, 'type="number" min="1" max="65535" required'))}</div>` : '',
      form.protocol === 'vless' || form.protocol === 'vmess' ? field('UUID', input('id', form.id, 'required')) : '',
      form.protocol === 'vless' ? field('Flow', select('flow', form.flow, [['', t('无', 'None')], ['xtls-rprx-vision', 'xtls-rprx-vision']])) : '',
      form.protocol === 'vless' ? field(t('反向代理标签（可选）', 'Reverse tag (optional)'), input('reverseTag', form.reverseTag, 'maxlength="64" placeholder="r-home"'), t('填写后本机作为反向代理桥接端：服务器经此连接把流量送回本机，在路由规则里以该标签作为入站转发（如转到直连）。服务器上对应客户端需填同一 UUID 并设置反向代理标签。', 'This machine becomes a reverse-proxy bridge: the server sends traffic back through this connection, which enters routing with this tag as the inbound (e.g. route it to direct). The matching client on the server needs the same UUID and its own reverse tag.')) : '',
      ['trojan', 'shadowsocks', 'hysteria'].includes(form.protocol) ? field(hy2 ? t('认证密码', 'Auth password') : t('密码', 'Password'), input('password', form.password, 'required')) : '',
      form.protocol === 'shadowsocks' ? field(t('加密方式', 'Method'), select('method', form.method, ssMethods.map(item => [item, item]))) : '',
      form.protocol === 'socks' || form.protocol === 'http' ? `<div class="field-row">${field(t('用户名（可选）', 'Username (optional)'), input('user', form.user))}${field(t('密码（可选）', 'Password (optional)'), input('pass', form.pass, 'type="password"'))}</div>` : '',
      stream ? `<div class="field-row">${field(t('传输', 'Transport'), select('network', form.network, [['tcp', 'TCP'], ['ws', 'WebSocket'], ['grpc', 'gRPC'], ['xhttp', 'XHTTP'], ['httpupgrade', 'HTTPUpgrade']]))}${field(t('安全', 'Security'), select('security', form.security, [['none', t('无', 'None')], ['tls', 'TLS'], ['reality', 'REALITY']]))}</div>` : '',
      stream && ['ws', 'xhttp', 'httpupgrade'].includes(form.network) ? `<div class="field-row">${field(t('路径', 'Path'), input('path', form.path))}${field('Host', input('host', form.host))}</div>` : '',
      stream && form.network === 'xhttp' ? field(t('XHTTP 模式', 'XHTTP mode'), select('mode', form.mode, ['auto', 'packet-up', 'stream-up', 'stream-one'].map(item => [item, item]))) : '',
      stream && form.network === 'grpc' ? field('Service name', input('serviceName', form.serviceName)) : '',
      (stream && form.security !== 'none') || hy2 ? `<div class="field-row">${field('SNI', input('serverName', form.serverName))}${field(t('指纹', 'Fingerprint'), select('fingerprint', form.fingerprint, ['chrome', 'firefox', 'safari', 'ios', 'edge', 'random'].map(item => [item, item])))}</div>` : '',
      (stream && form.security === 'tls') || hy2 ? `${field('ALPN', input('alpn', form.alpn, 'placeholder="h2,http/1.1"'))}<label class="check-field"><input type="checkbox" name="allowInsecure" ${form.allowInsecure ? 'checked' : ''}><span>${t('允许不安全证书（自签证书时使用）', 'Allow insecure certificates (for self-signed)')}</span></label>` : '',
      stream && form.security === 'reality' ? `${field(t('公钥', 'Public key'), input('publicKey', form.publicKey, 'required'))}<div class="field-row">${field('Short ID', input('shortId', form.shortId))}${field('SpiderX', input('spiderX', form.spiderX))}</div>` : '',
    ].join('');
  }
  function readOutbound(form: HTMLFormElement, base: OutboundForm): OutboundForm {
    const next = { ...base };
    for (const key of Object.keys(base) as (keyof OutboundForm)[]) {
      if (key === 'allowInsecure') next.allowInsecure = new FormData(form).has('allowInsecure');
      else if (form.elements.namedItem(key)) (next[key] as string) = value(form, key);
    }
    return next;
  }
  function outboundDialog(index: number | null): void {
    const p = parts(); if (!p) return;
    const initial = index === null ? { ...emptyOutbound(), tag: `proxy-${p.outbounds.length + 1}` } : outboundForm(p.outbounds[index]);
    if (!initial) { jsonDialog(t('编辑出站', 'Edit outbound'), p.outbounds[index!], next => change(current => ({ ...current, outbounds: current.outbounds.map((item, i) => i === index ? next : item) }))); return; }
    let form = initial;
    const oldTag = index === null ? '' : String(p.outbounds[index].tag);
    const refresh = (element: HTMLFormElement) => { form = readOutbound(element, form); element.querySelector('.edit-body')!.innerHTML = `${outboundFields(form)}<p class="form-error" role="alert" hidden></p>`; wireImport(element); };
    const wireImport = (element: HTMLFormElement) => element.querySelector('[data-import]')?.addEventListener('click', () => {
      const message = element.querySelector<HTMLElement>('.form-error')!;
      try { form = { ...outboundFromLink(value(element, 'link')) }; element.querySelector('.edit-body')!.innerHTML = `${outboundFields(form)}<p class="form-error" role="alert" hidden></p>`; wireImport(element); }
      catch (reason) { message.textContent = (reason as Error).message; message.hidden = false; }
    });
    openDialog(index === null ? t('添加出站', 'Add outbound') : t('编辑出站', 'Edit outbound'), outboundFields(form), element => {
      const outbound = outboundFromForm(readOutbound(element, form));
      const current = parts()!;
      if (current.outbounds.some((item, i) => i !== index && item.tag === outbound.tag)) throw new Error(t('出站标识重复。', 'Duplicate outbound tag.'));
      if (index !== null && oldTag !== outbound.tag && referenced(current, oldTag)) throw new Error(t('该出站正被路由或负载均衡使用，不能改名。', 'This outbound is used by routing or balancers and cannot be renamed.'));
      change(now => ({ ...now, outbounds: index === null ? [...now.outbounds, outbound] : now.outbounds.map((item, i) => i === index ? outbound : item) }));
    }, element => { const before = `${form.protocol}|${form.network}|${form.security}`; form = readOutbound(element, form); if (`${form.protocol}|${form.network}|${form.security}` !== before) refresh(element); });
    wireImport(dialog.querySelector('form')!);
  }
  // Cloudflare WARP（参照OUI）：服务器注册免费设备并生成tag为warp的WireGuard出站，加入草稿后需“保存”才生效。
  async function warpDialog(): Promise<void> {
    type WarpView = { registered: boolean; accountType?: string; premiumData: number; quota: number; usage: number; licenseSuffix?: string; outbound?: Json };
    let view: WarpView = { registered: false, premiumData: 0, quota: 0, usage: 0 }, busy = true, message = t('正在读取…', 'Loading…'), failure = '';
    const render = () => {
      const exists = parts()?.outbounds.some(item => item.tag === 'warp') ?? false;
      const info = (label: string, text: string) => `<div class="setting-row"><div class="setting-copy"><strong>${label}</strong></div><div class="setting-control">${esc(text)}</div></div>`;
      dialog.innerHTML = `<form novalidate><header class="dialog-heading"><h2>Cloudflare WARP</h2><button type="button" class="icon-button" data-close aria-label="${t('关闭弹窗', 'Close dialog')}">×</button></header><div class="edit-body">
        ${view.registered ? `<div class="setting-list">${info(t('账户类型', 'Account type'), view.accountType || '—')}${info(t('WARP+ 流量', 'WARP+ data'), bytes(view.premiumData))}${info(t('配额 / 已用', 'Quota / used'), `${bytes(view.quota)} / ${bytes(view.usage)}`)}${info(t('许可证', 'License'), view.licenseSuffix ? `••••${view.licenseSuffix}` : '—')}</div>
          ${field(t('WARP+ 许可证（可选）', 'WARP+ license (optional)'), input('license', '', 'placeholder="xxxxxxxx-xxxxxxxx-xxxxxxxx" maxlength="26"'), t('填入 WARP+ 许可证可升级为 WARP+ 账户。', 'Enter a WARP+ license to upgrade the account.'))}
          <div class="settings-table-actions">${button(exists ? t('更新 warp 出站', 'Update warp outbound') : t('添加 warp 出站', 'Add warp outbound'), 'data-warp="outbound"', 'primary-button')}${button(t('更新许可证', 'Update license'), 'data-warp="license"')}${button(t('刷新账户信息', 'Refresh account'), 'data-warp="refresh"')}${button(t('删除 WARP 账户', 'Delete WARP account'), 'data-warp="delete"', 'text-button danger-text')}</div>`
        : `<p class="form-hint">${t('由服务器向 Cloudflare（api.cloudflareclient.com）注册一个免费 WARP 设备，生成 WireGuard 出站（tag 为 warp，只用用户态网络栈，不创建系统网卡）。私钥与设备令牌只保存在面板数据库。之后在路由规则里把目标选为 warp，匹配的流量就经 WARP 出去（常用于解锁 ChatGPT、Netflix 等）。', 'The server registers a free WARP device with Cloudflare (api.cloudflareclient.com) and builds a WireGuard outbound tagged warp (userspace stack only, no system interface). The private key and device token stay in the panel database. Then choose warp as a routing rule target to send matching traffic through WARP (often used to unlock ChatGPT, Netflix and others).')}</p>
          <div class="settings-table-actions">${button(t('创建 WARP 账户', 'Create WARP account'), 'data-warp="register"', 'primary-button')}</div>`}
        <p class="form-hint" role="status">${esc(message)}</p><p class="form-error" role="alert" ${failure ? '' : 'hidden'}>${esc(failure)}</p></div>
        <footer>${button(t('关闭', 'Close'), 'data-close')}</footer></form>`;
      dialog.querySelectorAll<HTMLButtonElement>('[data-warp]').forEach(item => { item.disabled = busy; });
      dialog.querySelectorAll('[data-close]').forEach(item => item.addEventListener('click', () => dialog.close()));
      dialog.querySelector('form')!.addEventListener('submit', event => event.preventDefault());
      dialog.querySelectorAll<HTMLButtonElement>('[data-warp]').forEach(item => item.addEventListener('click', () => { void run(item.dataset.warp!); }));
    };
    const run = async (action: string) => {
      if (busy) return;
      if (action === 'outbound') {
        if (!view.outbound) return;
        const outbound = view.outbound;
        change(now => ({ ...now, outbounds: now.outbounds.some(item => item.tag === 'warp') ? now.outbounds.map(item => item.tag === 'warp' ? outbound : item) : [...now.outbounds, outbound] }));
        message = t('已把 warp 出站加入设置；点“保存”后生效。', 'The warp outbound is in the settings; click Save to apply.'); failure = ''; render(); return;
      }
      if (action === 'register' && !await confirmAction(t('由服务器向 Cloudflare 注册一个免费 WARP 设备。', 'Register a free WARP device with Cloudflare from the server.'), { title: t('注册 WARP 设备', 'Register WARP device'), confirmLabel: t('注册', 'Register') })) return;
      if (action === 'delete' && !await confirmAction(t('删除本机保存的 WARP 账户记录。已加入的 warp 出站需在出站列表里另行删除。', 'Delete the WARP account record saved on this server. A warp outbound already added must be removed from the outbound list separately.'), { title: t('删除 WARP 记录', 'Delete WARP record'), confirmLabel: t('删除', 'Delete'), danger: true })) return;
      const license = action === 'license' ? String(new FormData(dialog.querySelector('form')!).get('license') ?? '').trim() : '';
      busy = true; failure = ''; message = t('正在处理…', 'Working…'); render();
      try {
        view = action === 'register' ? await api<WarpView>('xray/warp/register', jsonBody('POST', { confirm: true }))
          : action === 'license' ? await api<WarpView>('xray/warp/license', jsonBody('POST', { license }))
          : action === 'refresh' ? await api<WarpView>('xray/warp?refresh=1')
          : await api<WarpView>('xray/warp', jsonBody('DELETE', {}));
        message = action === 'register' ? t('WARP 账户已创建，可以添加 warp 出站。', 'WARP account created; you can add the warp outbound.') : action === 'delete' ? t('已删除本机 WARP 记录。', 'The local WARP record was deleted.') : t('已更新。', 'Updated.');
      } catch (reason) { failure = (reason as Error).message; message = ''; }
      busy = false; if (dialog.open) render();
    };
    render(); dialog.showModal();
    try { view = await api<WarpView>('xray/warp'); message = ''; } catch (reason) { failure = (reason as Error).message; message = ''; }
    busy = false; if (dialog.open) render();
  }
  const referenced = (p: Parts, tag: string) => p.rules.some(rule => rule.outboundTag === tag) || p.balancers.some(item => balancerForm(item).fallbackTag === tag || balancerForm(item).selector.some(prefix => tag.startsWith(prefix)));

  function balancerDialog(index: number | null): void {
    const p = parts(); if (!p) return;
    const form: BalancerForm = index === null ? emptyBalancer() : balancerForm(p.balancers[index]);
    const tags: [string, string][] = p.outbounds.map(item => [String(item.tag), String(item.tag)]);
    const oldTag = form.tag;
    openDialog(index === null ? t('添加负载均衡', 'Add balancer') : t('编辑负载均衡', 'Edit balancer'), [
      field(t('标识（tag）', 'Tag'), input('tag', form.tag, 'required')),
      field(t('策略', 'Strategy'), select('strategy', form.strategy, balancerStrategies), t('最低延迟/最低负载会按出站测试 URL 自动测速', 'Least ping/load probe outbounds with the outbound test URL')),
      field(t('出站', 'Outbounds'), checks('selector', form.selector, tags)),
      field(t('回退出站', 'Fallback outbound'), select('fallbackTag', form.fallbackTag, [['', t('无', 'None')], ...tags])),
    ].join(''), element => {
      const balancer = balancerFromForm({ tag: value(element, 'tag'), strategy: value(element, 'strategy'), selector: checked(element, 'selector'), fallbackTag: value(element, 'fallbackTag') });
      const current = parts()!;
      if (current.balancers.some((item, i) => i !== index && item.tag === balancer.tag)) throw new Error(t('负载均衡标识重复。', 'Duplicate balancer tag.'));
      if (index !== null && oldTag !== balancer.tag && current.rules.some(rule => rule.balancerTag === oldTag)) throw new Error(t('该负载均衡正被路由使用，不能改名。', 'This balancer is used by routing and cannot be renamed.'));
      change(now => syncObservatory({ ...now, balancers: index === null ? [...now.balancers, balancer] : now.balancers.map((item, i) => i === index ? balancer : item) }, draft.probeURL));
    });
  }

  function dnsDialog(index: number | null): void {
    const p = parts(); if (!p) return;
    const servers = Array.isArray(p.dns.servers) ? p.dns.servers : [];
    const form: DNSServerForm = index === null ? emptyDNSServer() : dnsServerForm(servers[index]);
    openDialog(index === null ? t('添加 DNS 服务器', 'Add DNS server') : t('编辑 DNS 服务器', 'Edit DNS server'), [
      field(t('地址', 'Address'), input('address', form.address, 'required placeholder="https://1.1.1.1/dns-query"'), t('支持 IP、localhost、https:// (DoH)、tcp://', 'Supports IP, localhost, https:// (DoH), tcp://')),
      field(t('端口（可选）', 'Port (optional)'), input('port', form.port, 'type="number" min="1" max="65535"')),
      field(t('仅用于这些域名（每行一项）', 'Only for these domains (one per line)'), area('domains', form.domains)),
      field(t('期望 IP（每行一项）', 'Expected IPs (one per line)'), area('expectIPs', form.expectIPs)),
      `<label class="check-field"><input type="checkbox" name="skipFallback" ${form.skipFallback ? 'checked' : ''}><span>${t('不参与回退查询', 'Skip fallback')}</span></label>`,
    ].join(''), element => {
      const server = dnsServerFromForm({ address: value(element, 'address'), port: value(element, 'port'), domains: value(element, 'domains'), expectIPs: value(element, 'expectIPs'), skipFallback: new FormData(element).has('skipFallback') });
      change(now => { const list = Array.isArray(now.dns.servers) ? [...now.dns.servers] : []; if (index === null) list.push(server); else list[index] = server; return { ...now, dns: { ...now.dns, servers: list } }; });
    });
  }

  function move<T>(list: T[], index: number, offset: number): T[] { const next = [...list]; const [item] = next.splice(index, 1); next.splice(index + offset, 0, item); return next; }
  async function operate(op: string, index: number): Promise<void> {
    const [kind, action] = op.split(':');
    if (kind === 'warp') { void warpDialog(); return; }
    const p = parts(); if (!p) return;
    if (action === 'add') void ({ rule: ruleDialog, outbound: outboundDialog, balancer: balancerDialog, dns: dnsDialog } as Record<string, (i: number | null) => unknown>)[kind](null);
    if (action === 'edit') void ({ rule: ruleDialog, outbound: outboundDialog, balancer: balancerDialog, dns: dnsDialog } as Record<string, (i: number | null) => unknown>)[kind](index);
    if (action === 'up' || action === 'down') {
      const offset = action === 'up' ? -1 : 1;
      change(now => kind === 'rule' ? { ...now, rules: move(now.rules, index, offset) } : kind === 'outbound' ? { ...now, outbounds: move(now.outbounds, index, offset) } : kind === 'balancer' ? { ...now, balancers: move(now.balancers, index, offset) } : { ...now, dns: { ...now.dns, servers: move(Array.isArray(now.dns.servers) ? now.dns.servers : [], index, offset) } });
    }
    if (action === 'delete') {
      if (kind === 'outbound') {
        if (p.outbounds.length === 1) { error = t('至少保留一个出站。', 'Keep at least one outbound.'); paint(); return; }
        if (referenced(p, String(p.outbounds[index].tag))) { error = t('该出站正被路由或负载均衡使用，请先修改它们。', 'This outbound is used by routing or balancers; change them first.'); paint(); return; }
      }
      if (kind === 'balancer' && p.rules.some(rule => rule.balancerTag === p.balancers[index].tag)) { error = t('该负载均衡正被路由使用，请先修改路由。', 'This balancer is used by routing; change the rules first.'); paint(); return; }
      if (kind === 'dns' && (Array.isArray(p.dns.servers) ? p.dns.servers.length : 0) <= 1) { error = t('至少保留一个 DNS 服务器。', 'Keep at least one DNS server.'); paint(); return; }
      if (!await confirmAction(t('删除这一项，保存后生效。', 'Delete this item. It takes effect after saving.'), { title: t('删除设置项', 'Delete setting'), confirmLabel: t('删除', 'Delete'), danger: true })) return;
      change(now => kind === 'rule' ? { ...now, rules: now.rules.filter((_, i) => i !== index) } : kind === 'outbound' ? { ...now, outbounds: now.outbounds.filter((_, i) => i !== index) } : kind === 'balancer' ? syncObservatory({ ...now, balancers: now.balancers.filter((_, i) => i !== index) }, draft.probeURL) : { ...now, dns: { ...now.dns, servers: (Array.isArray(now.dns.servers) ? now.dns.servers : []).filter((_, i) => i !== index) } });
    }
  }

  // ---- 事件 ----
  root.addEventListener('click', event => {
    const target = (event.target as Element).closest<HTMLElement>('button');
    if (!target || target.closest('dialog')) return;
    if (target.dataset.settingsTab) { tab = target.dataset.settingsTab as Tab; error = ''; jsonError = ''; paint(); if (tab === 'outbounds') void refreshTraffic(); return; }
    if (target.id === 'xray-save') { void save(); return; }
    if (target.id === 'xray-restart') { onApply(); return; }
    if (target.id === 'xray-reload') { void load(true); return; }
    if (target.dataset.op && loaded && !saving && !loading) void operate(target.dataset.op, Number(target.dataset.index));
  });
  root.addEventListener('change', event => {
    const element = event.target as HTMLInputElement;
    if (!loaded || saving || loading) return;
    if (['freedomStrategy', 'routingStrategy', 'probeURL', 'logLevel'].includes(element.name)) { draft = { ...draft, [element.name]: element.value }; if (element.name === 'probeURL') { const p = parts(); if (p) draft = writeParts(draft, syncObservatory(p, draft.probeURL)); } paint(); return; }
    if (element.name === 'accessLog' || element.name === 'dnsLog') { draft = { ...draft, [element.name]: element.checked }; paint(); return; }
    if (element.hasAttribute('data-basic-torrent')) { change(p => ({ ...p, outbounds: ensureOutbound(p.outbounds, 'block', { tag: 'block', protocol: 'blackhole', settings: {} }), rules: setRuleValues(p.rules, 'block', 'protocol', element.checked ? ['bittorrent'] : []) })); return; }
    const key = element.dataset.multi ?? (element as HTMLElement).dataset.multiCustom;
    if (key) {
      const [tag, property] = key.split(':');
      const scope = element.closest('.multi-select')!;
      const values = [...scope.querySelectorAll<HTMLInputElement>('[data-multi]:checked')].map(item => item.value).concat(String((scope.querySelector('[data-multi-custom]') as HTMLTextAreaElement).value).split('\n').map(item => item.trim()).filter(Boolean));
      change(p => ({ ...p, outbounds: ensureOutbound(p.outbounds, tag, tag === 'block' ? { tag: 'block', protocol: 'blackhole', settings: {} } : { tag: 'direct', protocol: 'freedom', settings: {} }), rules: setRuleValues(p.rules, tag, property, [...new Set(values)]) }));
      root.querySelector<HTMLDetailsElement>(`[data-multi="${CSS.escape(key)}"]`)?.closest('details')?.setAttribute('open', '');
      return;
    }
    if (element.name?.startsWith('dns-')) { const name = element.name.slice(4); change(p => ({ ...p, dns: { ...p.dns, ...(element.type === 'checkbox' ? { [name]: element.checked || undefined } : { [name]: element.value }) } })); return; }
    if (element.name === 'advanced-part') { part = element.value as AdvancedPart; jsonError = ''; paint(); return; }
    if ((element as HTMLElement).hasAttribute('data-dns-preset') && element.value) { const server = element.value; change(p => ({ ...p, dns: { ...p.dns, servers: [...(Array.isArray(p.dns.servers) ? p.dns.servers : []), server] } })); }
  });
  root.addEventListener('input', event => {
    const element = event.target as HTMLTextAreaElement;
    if (element.id !== 'xray-json-editor' || part === 'preview') return;
    try { JSON.parse(element.value); jsonError = ''; } catch { jsonError = t('JSON 格式无效，修正前无法保存。', 'Invalid JSON; fix it before saving.'); }
    draft = { ...draft, [part]: element.value };
    updateStatus();
    const message = root.querySelector('#xray-settings-form .form-error');
    if (jsonError && !message) element.insertAdjacentHTML('afterend', `<p class="form-error" role="alert">${esc(jsonError)}</p>`);
    if (!jsonError) message?.remove();
  });

  async function save(): Promise<void> {
    if (!loaded || saving || loading || reloadRequired || !dirty()) return;
    error = ''; feedback = '';
    try {
      buildSettings(draft, reverseTags());
      if (persistent) {
        saving = true; paint();
        const sent = structuredClone(draft), before = revision;
        let interrupted = false;
        type Saved = { revision: number; data: SettingsDraft; applied?: boolean; applyError?: string };
        const document = await api<Saved>('settings', jsonBody('PUT', { revision, data: sent })).catch(async (reason): Promise<Saved> => {
          if (uncertainSave(reason)) {
            feedback = t('连接中断，正在确认是否已保存…', 'Connection lost; confirming the save…'); paint();
            const confirmed = await confirmSaved('settings', sent, before + 1);
            if (confirmed) { interrupted = true; return { revision: confirmed.revision, data: confirmed.data }; }
          }
          if (!(reason instanceof APIError) || reason.status === 409 || reason.status >= 500) reloadRequired = true;
          throw reason;
        });
        revision = document.revision;
        feedback = interrupted ? t('已保存（连接曾中断，已向服务器确认；生效由服务器完成）。', 'Saved (the connection dropped, but the server confirmed it; applying was finished by the server).') : document.applied ? t('已保存并生效。', 'Saved and applied.') : `${t('已保存，但未生效：', 'Saved, but not applied: ')}${document.applyError ?? ''}`;
        onSaved();
      } else feedback = t('已保存当前会话草稿；刷新会恢复默认，不应用核心。', 'Session draft saved; refreshing restores defaults and nothing is applied.');
      saved = structuredClone(draft);
    } catch (reason) { error = (reason as Error).message; }
    saving = false; paint();
    root.querySelector<HTMLElement>(error ? '#xray-settings-error' : '#xray-save')?.focus();
  }
  async function load(force = false): Promise<void> {
    if ((!force && loaded) || loading || saving) return;
    if (force && loaded && dirty() && !await confirmAction(t('刷新会丢弃未保存的修改。', 'Refreshing discards unsaved changes.'), { title: t('刷新 Xray 设置', 'Refresh Xray settings'), confirmLabel: t('刷新', 'Refresh') })) return;
    loading = true; error = ''; feedback = ''; paint();
    try {
      const [document, current] = await Promise.all([api<{ revision: number; data: SettingsDraft | null }>('settings'), api<{ data: Workspace | null }>('workspace').catch(() => null)]);
      if (current?.data) workspace = current.data;
      const next = document.data ?? defaultSettings();
      buildSettings(next, reverseTags()); draft = next; saved = structuredClone(next);
      revision = document.revision; loaded = true; reloadRequired = false; jsonError = '';
      if (force) feedback = `${t('已刷新（修订', 'Refreshed (revision ')}${revision}${t('）。', ').')}`;
    } catch (reason) { error = (reason as Error).message; if (force) reloadRequired = true; }
    finally { loading = false; paint(); }
  }
  async function refreshTraffic(): Promise<void> {
    if (!persistent || !visible || root.hidden || tab !== 'outbounds' || document.hidden) return;
    try {
      const result = await api<{ outbounds: Traffic }>('runtime/outbound-traffic');
      traffic = result.outbounds;
      if (tab === 'outbounds' && !dialog.open && !root.contains(document.activeElement)) paint();
    } catch { /* 流量列显示“—”即可。 */ }
  }
  if (persistent) {
    window.setInterval(() => { if (visible && !root.hidden && !document.hidden) updateStatus(); }, 1000);
    window.setInterval(() => { void refreshTraffic(); }, 5000);
  }
  return {
    render: () => { visible = true; paint(); if (!loaded) void load(); else void refreshTraffic(); },
    setRuntime: (status, at) => { runtime = status; runtimeAt = at; updateStatus(); },
  };
}
