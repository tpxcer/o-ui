import { escapeHtml as esc } from './workspace.ts';
import { buildSettings, defaultSettings, freedomStrategies, routingStrategies, settingsTabs } from './xray-settings-model.ts';
import type { SettingsDraft, SettingsTab } from './xray-settings-model.ts';
import { api, APIError, jsonBody } from './api.ts';
import { emptyRoute, moveRoute, putRoute, removeRoute, routeFields, routeSummary, routingRules, routingTargets } from './routing-model.ts';
import type { RouteFields } from './routing-model.ts';
import { buildDNS, dnsFields, dnsStrategies } from './dns-model.ts';
import type { DNSFields } from './dns-model.ts';
import { balancerFields, balancerTexts, emptyBalancer, matchedOutbounds, putBalancer, removeBalancer } from './balancer-model.ts';
import type { BalancerFields } from './balancer-model.ts';
import { emptyOutbound, moveOutbound, outboundFields, outboundProtocols, outboundTexts, putOutbound, removeOutbound } from './outbound-model.ts';
import type { OutboundFields } from './outbound-model.ts';
import type { RuntimeStatus } from './runtime.ts';
import { settingsStatusText } from './settings-status.ts';
import './xray-settings.css';
import { t } from './i18n.ts';

const select = (id: string, label: string, hint: string, value: string, choices: string[]) => `<label class="xray-setting-row"><span><strong id="xray-label-${id}">${label}</strong><small id="xray-hint-${id}">${hint}</small></span><select name="${id}" aria-labelledby="xray-label-${id}" aria-describedby="xray-hint-${id}">${choices.map(choice => `<option ${choice === value ? 'selected' : ''}>${choice}</option>`).join('')}</select></label>`;
const check = (name: string, label: string, value: boolean) => `<label class="check-field"><input name="${name}" type="checkbox" ${value ? 'checked' : ''}><span>${label}</span></label>`;

export function initXraySettings(root: HTMLElement, icons: () => void, persistent = false, onApply: () => void = () => {}): { render: () => void; setRuntime: (status: RuntimeStatus | null, at: number) => void } {
  let draft = defaultSettings();
  let saved = structuredClone(draft);
  let tab: SettingsTab = 'basic';
  let feedback = '', error = '', preview = '';
  let revision = 0, loaded = !persistent, loading = false, saving = false, reloadRequired = false;
  let visible = false;
  let runtime: RuntimeStatus | null = null, runtimeAt = 0;
  let routingJSON = false, routeEditing: number | null = null, routeDeleting: number | null = null;
  let routeForm = emptyRoute();
  let dnsJSON = false, dnsEditing = false;
  let dnsForm = dnsFields(draft.dns)!;
  let balancerJSON = false, balancerEditing: number | null = null, balancerDeleting: number | null = null;
  let balancerForm = emptyBalancer();
  let outboundJSON = false, outboundEditing: number | null = null, outboundDeleting: number | null = null;
  let outboundForm = emptyOutbound();
  const editorInfo = {
    routing: ['rules', t('路由规则', 'Routing rules'), t('JSON对象数组，顺序就是匹配顺序。每项type=field并指定出站或均衡tag；不自动添加规则。', 'JSON array of objects; order is match order. Each entry has type=field and an outbound or balancer tag; no rules are added automatically.')],
    outbounds: ['outbounds', t('出站规则', 'Outbounds'), t('JSON对象数组，第一项通常是默认出站。每项tag唯一；Freedom策略以基础配置为准。', 'JSON array of objects; the first is usually the default outbound. Tags must be unique; the Freedom strategy comes from Basics.')],
    balancers: ['balancers', t('负载均衡', 'Balancers'), t('JSON对象数组。selector按前缀匹配出站；leastPing/leastLoad还需高级观测配置。', 'JSON array of objects. selector matches outbounds by prefix; leastPing/leastLoad also need an observatory in Advanced.')],
    dns: ['dns', 'DNS', t('JSON对象，设置servers、queryStrategy等。草稿不会修改系统DNS或发起查询。', 'JSON object with servers, queryStrategy and so on. Drafts never change system DNS or send queries.')],
    advanced: ['advanced', t('高级配置', 'Advanced'), t('JSON对象，当前允许observatory、burstObservatory、fakedns、transport；不覆盖其他分类或入站/控制接口。', 'JSON object; observatory, burstObservatory, fakedns and transport are allowed, without overriding other categories or inbound/control APIs.')],
  } as const;
  function capture(): void {
    const form = root.querySelector<HTMLFormElement>('#xray-settings-form');
    if (!form) return;
    form.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('[name]').forEach(field => {
      const key = field.name as keyof SettingsDraft;
      if (field instanceof HTMLInputElement && field.type === 'checkbox') Object.assign(draft, { [key]: field.checked });
      else Object.assign(draft, { [key]: field.value });
    });
    root.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('[data-route-field]').forEach(field => {
      routeForm[field.dataset.routeField as keyof RouteFields] = field.value;
    });
    const dnsInputs = root.querySelectorAll<HTMLSelectElement | HTMLTextAreaElement>('[data-dns-field]');
    dnsInputs.forEach(field => { dnsForm[field.dataset.dnsField as keyof DNSFields] = field.value; });
    if (dnsInputs.length) dnsEditing = JSON.stringify(dnsForm) !== JSON.stringify(dnsFields(draft.dns));
    root.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('[data-balancer-field]').forEach(field => {
      balancerForm[field.dataset.balancerField as keyof BalancerFields] = field.value;
    });
    root.querySelectorAll<HTMLInputElement | HTMLSelectElement>('[data-outbound-field]').forEach(field => {
      outboundForm[field.dataset.outboundField as keyof OutboundFields] = field.value;
    });
  }
  function outboundContent(): string {
    let items: string[] = [];
    try { items = outboundTexts(draft.outbounds); } catch { outboundJSON = true; }
    const pending = outboundEditing !== null || outboundDeleting !== null;
    const mode = `<div class="routing-toolbar"><h2 class="xray-editor-title">${t('出站规则', 'Outbounds')}</h2><button type="button" class="secondary-button" data-outbound-action="${outboundJSON ? 'list' : 'json'}" ${pending ? 'disabled' : ''}>${outboundJSON ? t('返回出站列表', 'Back to outbound list') : t('编辑JSON', 'Edit JSON')}</button></div>`;
    const hint = t('<p class="form-intro">第一项是无路由命中时的默认出站；上下移动会改变默认，新增放在末尾。先更新列表再保存，最后明确应用才改变流量。HTTP/SOCKS5本身不加密，凭据仅供可信网络使用；TLS、链式代理与其他协议请用高级JSON。</p>', '<p class="form-intro">The first entry is the default outbound when no rule matches; moving entries changes the default, and new ones go last. Update the list, save, then apply explicitly to change traffic. HTTP/SOCKS5 are not encrypted, so use credentials only on trusted networks; use advanced JSON for TLS, chained proxies and other protocols.</p>');
    if (outboundJSON) return `<div class="xray-setting-content">${mode}${hint}<label class="form-field"><span id="xray-json-label">${t('出站规则 JSON', 'Outbounds JSON')}</span><textarea id="xray-json-editor" name="outbounds" rows="16" spellcheck="false" autocomplete="off" aria-labelledby="xray-json-label" aria-describedby="xray-json-hint">${esc(draft.outbounds)}</textarea><small id="xray-json-hint" class="form-hint">${t('JSON和配置预览可能含代理密码，请勿截图外传。Freedom策略以基础配置为准；高级条目原文保留，不自动转换。', 'JSON and the preview may contain proxy passwords; do not share screenshots. The Freedom strategy comes from Basics; advanced entries are kept verbatim and never converted.')}</small></label></div>`;
    const list = items.map((text, index) => {
      const item = JSON.parse(text), fields = outboundFields(text);
      const title = fields ? outboundProtocols.find(([value]) => value === fields.protocol)![1] : t('高级JSON', 'Advanced JSON');
      const summary = fields && ['http', 'socks'].includes(fields.protocol) ? `${t('代理：', 'Proxy: ')}${fields.address.includes(':') ? `[${fields.address}]` : fields.address}:${fields.port} · ${fields.user ? t('已配置认证（不展示凭据）', 'Authentication configured (credentials hidden)') : t('无需认证', 'No authentication')}` : fields?.protocol === 'freedom' ? `${t('Freedom策略：', 'Freedom strategy: ')}${draft.freedomStrategy}${t('（基础配置统一设置）', ' (set in Basics)')}` : fields?.protocol === 'blackhole' ? t('丢弃连接', 'Drops connections') : t('使用JSON查看，列表不展开高级参数或凭据', 'View as JSON; the list hides advanced parameters and credentials');
      return `<li class="routing-rule"><strong>${esc(String(item.tag ?? t('缺少标识', 'Missing tag')))} · ${title}${index === 0 ? t(' · 默认出站', ' · default outbound') : ''}</strong><p>${esc(summary)}</p><div class="routing-row-actions"><button type="button" class="secondary-button" data-outbound-action="edit" data-outbound-index="${index}" ${pending || !fields ? 'disabled' : ''} aria-label="${t('编辑第', 'Edit outbound #')}${index + 1}${t('个出站', '')}">${t('编辑', 'Edit')}</button><button type="button" class="secondary-button" data-outbound-action="up" data-outbound-index="${index}" ${pending || index === 0 ? 'disabled' : ''} aria-label="${t('上移第', 'Move up outbound #')}${index + 1}${t('个出站', '')}">${t('上移', 'Move up')}</button><button type="button" class="secondary-button" data-outbound-action="down" data-outbound-index="${index}" ${pending || index === items.length - 1 ? 'disabled' : ''} aria-label="${t('下移第', 'Move down outbound #')}${index + 1}${t('个出站', '')}">${t('下移', 'Move down')}</button><button type="button" class="secondary-button" data-outbound-action="delete" data-outbound-index="${index}" ${pending || !fields || index === 0 ? 'disabled' : ''} aria-label="${t('删除第', 'Delete outbound #')}${index + 1}${t('个出站', '')}">${t('删除', 'Delete')}</button>${!fields ? t('<span class="form-hint">高级内容请用JSON编辑</span>', '<span class="form-hint">Edit advanced content as JSON</span>') : ''}</div></li>`;
    }).join('');
    const field = (key: keyof OutboundFields, label: string, hint: string, attributes = '') => `<label class="form-field"><span id="outbound-label-${key}">${label}</span><input data-outbound-field="${key}" value="${esc(outboundForm[key])}" autocomplete="off" spellcheck="false" aria-labelledby="outbound-label-${key}" aria-describedby="outbound-hint-${key}" ${attributes}><small id="outbound-hint-${key}" class="form-hint">${hint}</small></label>`;
    const form = outboundEditing === null ? '' : `<div class="routing-rule-form" role="group" aria-labelledby="outbound-form-title"><h3 id="outbound-form-title" tabindex="-1">${outboundEditing === -1 ? t('添加出站', 'Add outbound') : t('编辑出站', 'Edit outbound')}</h3><p class="form-hint">${t('切换分类保留输入；取消不改原配置。列表不显示用户名或密码，JSON/预览仍包含凭据。', 'Switching categories keeps your input; canceling leaves the original unchanged. The list hides usernames and passwords, but JSON and the preview still contain them.')}</p><div class="routing-form-grid">${field('tag', t('出站标识（tag）', 'Outbound tag'), t('供路由或均衡引用，已被引用时不能直接改名。', 'Referenced by routing or balancers; it cannot be renamed while referenced.'), 'maxlength="128"')}<label class="form-field"><span id="outbound-label-protocol">${t('出站类型', 'Outbound type')}</span><select data-outbound-field="protocol" aria-labelledby="outbound-label-protocol" aria-describedby="outbound-hint-protocol">${outboundProtocols.map(([value, label]) => `<option value="${value}" ${outboundForm.protocol === value ? 'selected' : ''}>${label}</option>`).join('')}</select><small id="outbound-hint-protocol" class="form-hint">${t('HTTP使用CONNECT，SOCKS仅此处配置SOCKS5。', 'HTTP uses CONNECT; SOCKS here means SOCKS5.')}</small></label></div><div class="routing-form-grid" data-outbound-proxy ${['http', 'socks'].includes(outboundForm.protocol) ? '' : 'hidden'}>${field('address', t('代理地址', 'Proxy address'), t('填写域名、IPv4或不带方括号的IPv6，不含端口。', 'Domain, IPv4 or IPv6 without brackets, no port.'), 'maxlength="253"')}${field('port', t('代理端口', 'Proxy port'), t('1–65535，不自动探测。', '1–65535; not probed automatically.'), 'inputmode="numeric" maxlength="5"')}${field('user', t('代理用户名（可选）', 'Proxy username (optional)'), t('无需认证时用户名与密码都留空。', 'Leave username and password empty for no authentication.'), 'maxlength="1024"')}${field('pass', t('代理密码（可选）', 'Proxy password (optional)'), t('已有密码会保留；更换请直接修改，清空需同时清空用户名。', 'An existing password is kept; edit it to change it, and clear the username too to remove it.'), 'type="password" maxlength="1024"')}</div><div class="routing-row-actions"><button type="button" class="primary-button" data-outbound-action="commit">${outboundEditing === -1 ? t('加入出站列表', 'Add to outbound list') : t('更新这个出站', 'Update this outbound')}</button><button type="button" class="secondary-button" data-outbound-action="cancel">${t('取消出站编辑', 'Cancel outbound edit')}</button></div></div>`;
    const confirmation = outboundDeleting === null ? '' : t('<div class="routing-delete" role="group" aria-labelledby="outbound-delete-title"><h3 id="outbound-delete-title">删除所选出站？</h3><p>只移除这一项草稿；路由、均衡或高级配置仍在引用时拒绝删除，不自动改成直连。</p><div class="routing-row-actions"><button type="button" id="outbound-confirm-delete" class="primary-button" data-outbound-action="confirm-delete">确认删除这个出站</button><button type="button" class="secondary-button" data-outbound-action="cancel-delete">取消删除出站</button></div></div>', '<div class="routing-delete" role="group" aria-labelledby="outbound-delete-title"><h3 id="outbound-delete-title">Delete the selected outbound?</h3><p>Only this draft entry is removed; deletion is refused while routing, balancers or advanced config reference it, and nothing is switched to Direct.</p><div class="routing-row-actions"><button type="button" id="outbound-confirm-delete" class="primary-button" data-outbound-action="confirm-delete">Delete this outbound</button><button type="button" class="secondary-button" data-outbound-action="cancel-delete">Cancel deleting outbound</button></div></div>');
    return `<div class="xray-setting-content">${mode}${hint}<ol class="routing-rules">${list || t('<li class="routing-empty">尚无出站，请添加；不会自动补充直连。</li>', '<li class="routing-empty">No outbounds yet; add one. Direct is not added automatically.</li>')}</ol><button type="button" class="secondary-button" id="outbound-add" data-outbound-action="add" ${pending ? 'disabled' : ''}>${t('添加出站', 'Add outbound')}</button>${form}${confirmation}</div>`;
  }
  function outboundAction(action: string, index: number): void {
    capture(); error = ''; feedback = ''; preview = '';
    let focus = '#outbound-add';
    try {
      if (action === 'json') { if (outboundEditing !== null || outboundDeleting !== null) throw new Error(t('请先完成或取消出站编辑/删除确认。', 'Finish or cancel the outbound edit or delete first.')); outboundJSON = true; focus = '#xray-json-editor'; }
      if (action === 'list') { outboundTexts(draft.outbounds); outboundJSON = false; }
      if (action === 'add') { outboundEditing = -1; outboundForm = emptyOutbound(); focus = '#outbound-form-title'; }
      if (action === 'edit') {
        const fields = outboundFields(outboundTexts(draft.outbounds)[index]);
        if (!fields) throw new Error(t('此出站需要JSON编辑，原文未修改。', 'This outbound needs JSON editing; the original was not changed.'));
        outboundEditing = index; outboundForm = fields; focus = '#outbound-form-title';
      }
      if (action === 'commit' && outboundEditing !== null) { draft.outbounds = putOutbound(draft, outboundEditing, outboundForm); outboundEditing = null; outboundForm = emptyOutbound(); feedback = t('出站列表已更新，请保存草稿；尚未应用。', 'Outbound list updated; save the draft. Not applied yet.'); }
      if (action === 'cancel') { outboundEditing = null; outboundForm = emptyOutbound(); }
      if (action === 'up' || action === 'down') { draft.outbounds = moveOutbound(draft.outbounds, index, action === 'up' ? -1 : 1); feedback = t('出站顺序已更新，首项为默认；请保存草稿，尚未应用。', 'Outbound order updated; the first is the default. Save the draft; not applied yet.'); }
      if (action === 'delete') { outboundDeleting = index; focus = '#outbound-confirm-delete'; }
      if (action === 'confirm-delete' && outboundDeleting !== null) { draft.outbounds = removeOutbound(draft, outboundDeleting); outboundDeleting = null; feedback = t('仅移除所选出站，请保存草稿；尚未应用。', 'Removed only the selected outbound; save the draft. Not applied yet.'); }
      if (action === 'cancel-delete') outboundDeleting = null;
    } catch (reason) { error = (reason as Error).message; focus = '#xray-settings-error'; }
    paint(); root.querySelector<HTMLElement>(focus)?.focus();
  }
  function balancerMatches(): string {
    try { return matchedOutbounds(draft, balancerForm.selector).join(t('、', ', ')) || t('没有匹配的出站，不能提交。', 'No matching outbound; cannot submit.'); }
    catch (reason) { return (reason as Error).message; }
  }
  function balancerContent(): string {
    let items: string[] = [];
    try { items = balancerTexts(draft.balancers); } catch { balancerJSON = true; }
    const pending = balancerEditing !== null || balancerDeleting !== null;
    const mode = `<div class="routing-toolbar"><h2 class="xray-editor-title">${t('负载均衡', 'Balancers')}</h2><button type="button" class="secondary-button" data-balancer-action="${balancerJSON ? 'list' : 'json'}" ${pending ? 'disabled' : ''}>${balancerJSON ? t('返回均衡列表', 'Back to balancer list') : t('编辑JSON', 'Edit JSON')}</button></div>`;
    const hint = t('<p class="form-intro">仅创建均衡不会改变流量，需要在路由规则中选择该均衡，再保存并应用。selector按出站标识前缀匹配，不是精确名称或正则；随机/轮询不检测可达性，不保证坏节点自动切换。</p>', '<p class="form-intro">Creating a balancer alone does not change traffic; select it in a routing rule, then save and apply. selector matches outbound tag prefixes, not exact names or regex; random and round robin do not check reachability or guarantee failover.</p>');
    if (balancerJSON) return `<div class="xray-setting-content">${mode}${hint}<label class="form-field"><span id="xray-json-label">${t('负载均衡 JSON', 'Balancers JSON')}</span><textarea id="xray-json-editor" name="balancers" rows="16" spellcheck="false" autocomplete="off" aria-labelledby="xray-json-label" aria-describedby="xray-json-hint">${esc(draft.balancers)}</textarea><small class="form-hint" id="xray-json-hint">${t('leastPing/leastLoad、fallbackTag及其它高级字段保留JSON；回退依赖高级观测配置，保存表单不会自动添加观测或联网测速。', 'leastPing/leastLoad, fallbackTag and other advanced fields stay in JSON; fallback depends on an observatory in Advanced, and saving the form never adds one or runs speed tests.')}</small></label></div>`;
    const list = items.map((text, index) => {
      const item = JSON.parse(text), fields = balancerFields(text);
      const strategy = fields ? fields.strategy === 'roundRobin' ? t('轮询', 'Round robin') : fields.strategy ? t('随机', 'Random') : t('核心默认（随机）', 'Core default (random)') : t('高级JSON', 'Advanced JSON');
      let matches = t('请在JSON中检查', 'Check in JSON');
      if (fields) { try { matches = matchedOutbounds(draft, fields.selector).join(t('、', ', ')) || t('未匹配出站', 'No matching outbound'); } catch (reason) { matches = (reason as Error).message; } }
      return `<li class="routing-rule"><strong>${esc(String(item.tag ?? t('缺少标识', 'Missing tag')))} · ${strategy}</strong><p>${t('匹配出站：', 'Matched outbounds: ')}${esc(matches)}</p><div class="routing-row-actions"><button type="button" class="secondary-button" data-balancer-action="edit" data-balancer-index="${index}" ${pending || !fields ? 'disabled' : ''} aria-label="${t('编辑第', 'Edit balancer #')}${index + 1}${t('个均衡', '')}">${t('编辑', 'Edit')}</button><button type="button" class="secondary-button" data-balancer-action="delete" data-balancer-index="${index}" ${pending || !fields ? 'disabled' : ''} aria-label="${t('删除第', 'Delete balancer #')}${index + 1}${t('个均衡', '')}">${t('删除', 'Delete')}</button>${!fields ? t('<span class="form-hint">高级内容请用JSON编辑</span>', '<span class="form-hint">Edit advanced content as JSON</span>') : ''}</div></li>`;
    }).join('');
    let targets: string[] = [], targetError = '';
    try { targets = routingTargets(draft).filter(item => item.value.startsWith('outboundTag:')).map(item => item.value.slice('outboundTag:'.length)); }
    catch (reason) { targetError = (reason as Error).message; }
    const form = balancerEditing === null ? '' : `<div class="routing-rule-form" role="group" aria-labelledby="balancer-form-title"><h3 id="balancer-form-title" tabindex="-1">${balancerEditing === -1 ? t('添加负载均衡', 'Add balancer') : t('编辑负载均衡', 'Edit balancer')}</h3><p class="form-hint">${t('先加入均衡列表，再保存草稿。切换分类保留输入，取消不改原配置。', 'Add to the balancer list, then save the draft. Switching categories keeps your input; canceling leaves the original unchanged.')}</p><div class="routing-form-grid"><label class="form-field"><span>${t('均衡标识（tag）', 'Balancer tag')}</span><input data-balancer-field="tag" value="${esc(balancerForm.tag)}" maxlength="128" autocomplete="off"><small class="form-hint">${t('路由使用此标识；被引用时不能直接改名或删除。', 'Routing uses this tag; it cannot be renamed or deleted while referenced.')}</small></label><label class="form-field"><span>${t('分配策略', 'Strategy')}</span><select data-balancer-field="strategy">${[['', t('核心默认（随机）', 'Core default (random)')], ['random', t('随机', 'Random')], ['roundRobin', t('轮询', 'Round robin')]].map(([value, label]) => `<option value="${value}" ${balancerForm.strategy === value ? 'selected' : ''}>${label}</option>`).join('')}</select><small class="form-hint">${t('按新连接分配，不拆分单个连接，不自动测速。', 'Assigned per new connection; single connections are not split and no speed test runs.')}</small></label><label class="form-field"><span>${t('出站前缀（每行一项）', 'Outbound prefixes (one per line)')}</span><textarea data-balancer-field="selector" rows="4" spellcheck="false" placeholder="proxy-">${esc(balancerForm.selector)}</textarea><small class="form-hint" data-balancer-matches>${t('当前匹配：', 'Currently matches: ')}${esc(balancerMatches())}</small></label></div><p class="form-hint">${t('fallbackTag、leastPing/leastLoad和观测依赖请用高级JSON；常用表单不自动建立测速请求。', 'Use advanced JSON for fallbackTag, leastPing/leastLoad and the observatory; the form never creates speed-test requests.')}</p><div class="routing-row-actions"><button type="button" class="primary-button" data-balancer-action="commit">${balancerEditing === -1 ? t('加入均衡列表', 'Add to balancer list') : t('更新这个均衡', 'Update this balancer')}</button><button type="button" class="secondary-button" data-balancer-action="cancel">${t('取消均衡编辑', 'Cancel balancer edit')}</button></div></div>`;
    const confirmation = balancerDeleting === null ? '' : t('<div class="routing-delete" role="group" aria-labelledby="balancer-delete-title"><h3 id="balancer-delete-title">删除所选负载均衡？</h3><p>只移除这一项草稿；被路由引用时拒绝删除，不自动更改路由或切换直连。</p><div class="routing-row-actions"><button type="button" class="primary-button" id="balancer-confirm-delete" data-balancer-action="confirm-delete">确认删除这个均衡</button><button type="button" class="secondary-button" data-balancer-action="cancel-delete">取消删除均衡</button></div></div>', '<div class="routing-delete" role="group" aria-labelledby="balancer-delete-title"><h3 id="balancer-delete-title">Delete the selected balancer?</h3><p>Only this draft entry is removed; deletion is refused while routing references it, and routing is never changed or switched to Direct.</p><div class="routing-row-actions"><button type="button" class="primary-button" id="balancer-confirm-delete" data-balancer-action="confirm-delete">Delete this balancer</button><button type="button" class="secondary-button" data-balancer-action="cancel-delete">Cancel deleting balancer</button></div></div>');
    return `<div class="xray-setting-content">${mode}${hint}${targetError ? `<p class="form-error" role="alert">${esc(targetError)}</p>` : ''}<ol class="routing-rules">${list || t('<li class="routing-empty">尚无负载均衡，不会自动添加或启用。</li>', '<li class="routing-empty">No balancers yet; none are added or enabled automatically.</li>')}</ol><button type="button" class="secondary-button" id="balancer-add" data-balancer-action="add" ${pending || !!targetError || !targets.length ? 'disabled' : ''}>${t('添加负载均衡', 'Add balancer')}</button>${form}${confirmation}</div>`;
  }
  function balancerAction(action: string, index: number): void {
    capture(); error = ''; feedback = ''; preview = '';
    let focus = '#balancer-add';
    try {
      if (action === 'json') { if (balancerEditing !== null || balancerDeleting !== null) throw new Error(t('请先完成或取消均衡编辑/删除确认。', 'Finish or cancel the balancer edit or delete first.')); balancerJSON = true; focus = '#xray-json-editor'; }
      if (action === 'list') { balancerTexts(draft.balancers); balancerJSON = false; }
      if (action === 'add') { balancerEditing = -1; balancerForm = emptyBalancer(); focus = '#balancer-form-title'; }
      if (action === 'edit') {
        const fields = balancerFields(balancerTexts(draft.balancers)[index]);
        if (!fields) throw new Error(t('此均衡需要JSON编辑，原文未修改。', 'This balancer needs JSON editing; the original was not changed.'));
        balancerEditing = index; balancerForm = fields; focus = '#balancer-form-title';
      }
      if (action === 'commit' && balancerEditing !== null) { draft.balancers = putBalancer(draft, balancerEditing, balancerForm); balancerEditing = null; feedback = t('均衡列表已更新，请保存草稿；尚未应用。', 'Balancer list updated; save the draft. Not applied yet.'); }
      if (action === 'cancel') { balancerEditing = null; balancerForm = emptyBalancer(); }
      if (action === 'delete') { balancerDeleting = index; focus = '#balancer-confirm-delete'; }
      if (action === 'confirm-delete' && balancerDeleting !== null) { draft.balancers = removeBalancer(draft, balancerDeleting); balancerDeleting = null; feedback = t('仅移除所选均衡，请保存草稿；尚未应用。', 'Removed only the selected balancer; save the draft. Not applied yet.'); }
      if (action === 'cancel-delete') balancerDeleting = null;
    } catch (reason) { error = (reason as Error).message; focus = '#xray-settings-error'; }
    paint(); root.querySelector<HTMLElement>(focus)?.focus();
  }
  function dnsContent(): string {
    const fields = dnsFields(draft.dns);
    if (!fields) dnsJSON = true;
    if (!dnsEditing && fields) dnsForm = fields;
    const mode = `<div class="dns-toolbar"><h2 class="xray-editor-title">DNS</h2><button type="button" class="secondary-button" data-dns-action="${dnsJSON ? 'form' : 'json'}" ${dnsEditing ? 'disabled' : ''}>${dnsJSON ? t('使用常用表单', 'Use the form') : t('编辑JSON', 'Edit JSON')}</button></div>`;
    const hint = t('<p class="form-intro">只编辑Xray内置DNS，不修改系统DNS。保存不会查询或应用；实际解析方式还取决于Freedom/路由策略。多个服务器不代表严格按列表先后依次查询。</p>', '<p class="form-intro">Edits only the built-in Xray DNS, never the system DNS. Saving does not query or apply; actual resolution also depends on the Freedom and routing strategies. Multiple servers are not necessarily queried strictly in list order.</p>');
    if (dnsJSON) return `<div class="xray-setting-content">${mode}${hint}<label class="form-field"><span id="xray-json-label">DNS JSON</span><textarea id="xray-json-editor" name="dns" rows="16" spellcheck="false" autocomplete="off" aria-labelledby="xray-json-label" aria-describedby="xray-json-hint">${esc(draft.dns)}</textarea><small class="form-hint" id="xray-json-hint">${t('对象服务器、hosts、域名分流或其它高级字段请保留JSON；无法完整表达时不会转成简单表单，也不会替换原文。', 'Keep object servers, hosts, domain splitting and other advanced fields in JSON; content the form cannot fully express is never converted or replaced.')}</small></label></div>`;
    const choice = (key: keyof DNSFields, label: string, hint: string, choices: [string, string][]) => `<label class="form-field"><span>${label}</span><select data-dns-field="${key}">${choices.map(([value, title]) => `<option value="${value}" ${dnsForm[key] === value ? 'selected' : ''}>${title}</option>`).join('')}</select><small class="form-hint">${hint}</small></label>`;
    return `<div class="xray-setting-content">${mode}${hint}<p class="form-hint">${t('改动后先点“更新DNS草稿”，再保存；切换分类保留未完成输入，取消不改原配置。', 'After changes, click “Update DNS draft”, then save. Switching categories keeps unfinished input; canceling leaves the original unchanged.')}</p><label class="form-field"><span>${t('DNS服务器（每行一项）', 'DNS servers (one per line)')}</span><textarea class="dns-servers" data-dns-field="servers" rows="5" spellcheck="false" placeholder="localhost&#10;1.1.1.1&#10;tcp://127.0.0.1:5353&#10;https://dns.example.test/dns-query">${esc(dnsForm.servers)}</textarea><small class="form-hint">${t('localhost使用系统解析器；裸IP使用UDP默认53端口。TCP写IP及端口，HTTPS为DoH；+local模式绕过Xray路由。DoH域名需要能先解析，避免依赖自身形成循环；不自动填公共服务商，不接受账号或查询参数。', 'localhost uses the system resolver; a bare IP uses UDP port 53. For TCP enter IP and port; HTTPS means DoH; +local modes bypass Xray routing. A DoH domain must be resolvable first to avoid a loop. No public providers are filled in, and credentials or query parameters are not accepted.')}</small></label>
      <div class="dns-form-grid">${choice('queryStrategy', t('DNS查询策略', 'DNS query strategy'), t('UseIP查询IPv4/IPv6；实际选择还受出站策略约束。', 'UseIP queries IPv4/IPv6; the actual choice also depends on the outbound strategy.'), dnsStrategies.map(value => [value, value || t('核心默认（UseIP）', 'Core default (UseIP)')]))}${choice('disableCache', t('DNS缓存', 'DNS cache'), t('禁用后每次解析可能产生新的上游请求。', 'When disabled, every lookup may send a new upstream request.'), [['', t('核心默认', 'Core default')], ['false', t('允许缓存', 'Allow cache')], ['true', t('禁用缓存', 'Disable cache')]])}${choice('disableFallback', t('DNS回退', 'DNS fallback'), t('禁止回退可能导致解析失败；按域名匹配/跳过回退需使用高级JSON。', 'Disabling fallback can cause resolution failures; per-domain fallback rules need advanced JSON.'), [['', t('核心默认', 'Core default')], ['false', t('允许回退', 'Allow fallback')], ['true', t('禁止回退', 'Disable fallback')]])}</div><div class="dns-actions"><button type="button" class="primary-button" data-dns-action="commit">${t('更新DNS草稿', 'Update DNS draft')}</button><button type="button" class="secondary-button" data-dns-action="cancel">${t('取消DNS编辑', 'Cancel DNS edit')}</button></div></div>`;
  }
  function dnsAction(action: string): void {
    capture(); error = ''; feedback = ''; preview = '';
    let focus = '[data-dns-field="servers"]';
    try {
      if (action === 'json') { if (dnsEditing) throw new Error(t('请先更新或取消DNS表单，再切换JSON。', 'Update or cancel the DNS form before switching to JSON.')); dnsJSON = true; focus = '#xray-json-editor'; }
      if (action === 'form') {
        const fields = dnsFields(draft.dns);
        if (!fields) throw new Error(t('此DNS配置含高级字段、重复键或表单无法完整表达的内容，请继续使用JSON；原文未改动。', 'This DNS configuration has advanced fields, duplicate keys or content the form cannot express; keep using JSON. The original was not changed.'));
        dnsForm = fields; dnsJSON = false;
      }
      if (action === 'commit') { draft.dns = buildDNS(dnsForm); dnsEditing = false; feedback = t('DNS草稿已更新，请保存草稿；未查询、未应用。', 'DNS draft updated; save the draft. Nothing was queried or applied.'); }
      if (action === 'cancel') { dnsEditing = false; feedback = t('已取消本次DNS表单编辑，原草稿保持。', 'DNS form edit canceled; the draft is unchanged.'); }
    } catch (reason) { error = (reason as Error).message; focus = '#xray-settings-error'; }
    paint(); root.querySelector<HTMLElement>(focus)?.focus();
  }
  function routeContent(): string {
    const hint = t('<p class="form-intro">从上往下匹配，先命中的规则生效。同一条的不同条件为“并且”，同组多项为“或者”；不自动添加规则，未匹配走首个出站。保存草稿后仍需单独应用。</p>', '<p class="form-intro">Rules match top to bottom and the first match wins. Conditions in one rule are AND; entries in one group are OR. No rules are added automatically, and unmatched traffic uses the first outbound. Saved drafts still need to be applied separately.</p>');
    let rules;
    try { rules = routingRules(draft.rules); } catch { routingJSON = true; }
    const mode = `<div class="routing-toolbar"><h2 class="xray-editor-title">${t('路由规则', 'Routing rules')}</h2><button type="button" class="secondary-button" data-route-action="${routingJSON ? 'list' : 'json'}" ${routeEditing !== null || routeDeleting !== null ? 'disabled' : ''}>${routingJSON ? t('返回规则列表', 'Back to rule list') : t('编辑JSON', 'Edit JSON')}</button></div>`;
    if (routingJSON) return `<div class="xray-setting-content">${mode}${hint}<label class="form-field"><span id="xray-json-label">${t('路由规则 JSON', 'Routing rules JSON')}</span><textarea id="xray-json-editor" name="rules" rows="16" spellcheck="false" autocomplete="off" aria-labelledby="xray-json-label" aria-describedby="xray-json-hint">${esc(draft.rules)}</textarea><small class="form-hint" id="xray-json-hint">${t('保留完整高级字段。无效JSON不会被规则列表覆盖，修正后可返回列表。', 'All advanced fields are kept. Invalid JSON is never overwritten by the rule list; fix it to return to the list.')}</small></label></div>`;
    let targets: ReturnType<typeof routingTargets> = [], targetError = '';
    try { targets = routingTargets(draft); } catch (reason) { targetError = (reason as Error).message; }
    const pending = routeEditing !== null || routeDeleting !== null;
    const list = (rules ?? []).map((rule, index) => {
      const editable = !!routeFields(rule);
      const target = typeof rule.outboundTag === 'string' ? `${t('出站 ·', 'Outbound ·')} ${rule.outboundTag}` : typeof rule.balancerTag === 'string' ? `${t('均衡 ·', 'Balancer ·')} ${rule.balancerTag}` : t('目标需在JSON中检查', 'Check the target in JSON');
      const button = (action: string, label: string, disabled = false) => `<button type="button" class="secondary-button" data-route-action="${action}" data-route-index="${index}" aria-label="${label}${t('第', ' rule #')}${index + 1}${t('条规则', '')}" ${pending || disabled ? 'disabled' : ''}>${label}</button>`;
      return `<li class="routing-rule"><div><strong>${t('第', 'Rule ')}${index + 1}${t('条', '')} · ${esc(target)}</strong><p>${esc(routeSummary(rule))}</p>${!editable ? t('<small class="form-hint">高级规则：仅用JSON编辑，列表移动/删除不会改写其它字段。</small>', '<small class="form-hint">Advanced rule: edit as JSON only; moving or deleting in the list keeps other fields.</small>') : ''}</div><div class="routing-row-actions">${button('edit', t('编辑', 'Edit'), !editable)}${button('up', t('上移', 'Move up'), index === 0)}${button('down', t('下移', 'Move down'), index === (rules?.length ?? 0) - 1)}${button('delete', t('删除', 'Delete'))}</div></li>`;
    }).join('');
    const field = (key: 'domain' | 'ip' | 'protocol', label: string, placeholder: string) => `<label class="form-field"><span>${label}</span><textarea data-route-field="${key}" rows="3" placeholder="${placeholder}" spellcheck="false">${esc(routeForm[key])}</textarea></label>`;
    const form = routeEditing === null ? '' : `<div class="routing-rule-form" role="group" aria-labelledby="route-form-title"><h3 id="route-form-title" tabindex="-1">${routeEditing === -1 ? t('添加规则（追加到末尾）', 'Add rule (appended)') : `${t('编辑第', 'Edit rule #')}${routeEditing + 1}${t('条规则', '')}`}</h3><p class="form-hint">${t('以下条件至少填一项。表单内容需先加入规则列表，再保存草稿；取消不会改动原规则。', 'Fill in at least one condition. Add the form to the rule list, then save the draft; canceling leaves the original rule unchanged.')}</p><div class="routing-form-grid">${field('domain', t('目标域名（每行一项）', 'Target domains (one per line)'), 'domain:example.test&#10;full:example.org')}${field('ip', t('目标IP / CIDR（每行一项）', 'Target IP / CIDR (one per line)'), '192.0.2.0/24&#10;2001:db8::/32')}<label class="form-field"><span>${t('目标端口', 'Target ports')}</span><input data-route-field="port" value="${esc(routeForm.port)}" placeholder="443,1000-2000" inputmode="text"></label><label class="form-field"><span>${t('传输', 'Network')}</span><select data-route-field="network">${[['', t('不限', 'Unlimited')], ['tcp', 'TCP'], ['udp', 'UDP'], ['tcp,udp', 'TCP + UDP']].map(([value, label]) => `<option value="${value}" ${routeForm.network === value ? 'selected' : ''}>${label}</option>`).join('')}</select></label>${field('protocol', t('嗅探协议（每行一项）', 'Sniffed protocols (one per line)'), 'http&#10;tls&#10;quic&#10;bittorrent')}<label class="form-field"><span>${t('转发目标', 'Forward to')}</span><select data-route-field="target"><option value="">${t('请选择出站或负载均衡', 'Choose an outbound or balancer')}</option>${!targets.some(item => item.value === routeForm.target) && routeForm.target ? `<option value="${esc(routeForm.target)}" selected>${t('已不存在 ·', 'Missing ·')} ${esc(routeForm.target)}</option>` : ''}${targets.map(item => `<option value="${esc(item.value)}" ${routeForm.target === item.value ? 'selected' : ''}>${esc(item.label)}</option>`).join('')}</select><small class="form-hint">${t('域名/协议匹配可能需要对应嗅探或DNS设置；填写规则不保证该协议能被识别。', 'Domain or protocol matching may need sniffing or DNS settings; a rule does not guarantee the protocol is detected.')}</small></label></div><div class="routing-row-actions"><button type="button" class="primary-button" data-route-action="commit">${routeEditing === -1 ? t('加入规则列表', 'Add to rule list') : t('更新这条规则', 'Update this rule')}</button><button type="button" class="secondary-button" data-route-action="cancel">${t('取消编辑', 'Cancel edit')}</button></div></div>`;
    const confirmation = routeDeleting === null ? '' : `<div class="routing-delete" role="group" aria-labelledby="route-delete-title"><h3 id="route-delete-title">${t('删除第', 'Delete rule #')}${routeDeleting + 1}${t('条规则？', '?')}</h3><p>${t('仅从当前草稿移除这一条，其它规则及顺序保持；未保存/应用前不改变核心。', 'Removes only this rule from the draft; other rules and their order are kept. The core is unchanged until you save and apply.')}</p><div class="routing-row-actions"><button type="button" class="primary-button" id="route-confirm-delete" data-route-action="confirm-delete">${t('确认删除这一条', 'Delete this rule')}</button><button type="button" class="secondary-button" data-route-action="cancel-delete">${t('取消删除', 'Cancel')}</button></div></div>`;
    return `<div class="xray-setting-content">${mode}${hint}${targetError ? `<p class="form-error" role="alert">${esc(targetError)}</p>` : ''}<ol class="routing-rules">${list || t('<li class="routing-empty">尚无规则，当前使用默认出站。</li>', '<li class="routing-empty">No rules yet; the default outbound is used.</li>')}</ol><button type="button" class="secondary-button" id="route-add" data-route-action="add" ${pending || !!targetError || !targets.length ? 'disabled' : ''}>${t('添加规则', 'Add rule')}</button>${form}${confirmation}</div>`;
  }
  function routeAction(action: string, index: number): void {
    capture(); error = ''; preview = ''; feedback = '';
    let focus = '#route-add';
    try {
      if (action === 'json') { routingJSON = true; focus = '#xray-json-editor'; }
      if (action === 'list') { routingRules(draft.rules); routingJSON = false; }
      if (action === 'add') { routeEditing = -1; routeForm = emptyRoute(); focus = '#route-form-title'; }
      if (action === 'edit') {
        const rule = routingRules(draft.rules)[index];
        const fields = rule && routeFields(rule);
        if (!fields) throw new Error(t('该规则需用JSON编辑，以保留全部字段。', 'This rule needs JSON editing to keep all fields.'));
        routeEditing = index; routeForm = fields; focus = '#route-form-title';
      }
      if (action === 'commit' && routeEditing !== null) { draft.rules = putRoute(draft, routeEditing, routeForm); routeEditing = null; feedback = t('规则列表已更新，请保存草稿；尚未应用。', 'Rule list updated; save the draft. Not applied yet.'); }
      if (action === 'cancel') { routeEditing = null; routeForm = emptyRoute(); }
      if (action === 'up' || action === 'down') { draft.rules = moveRoute(draft.rules, index, action === 'up' ? -1 : 1); feedback = t('规则顺序已更改，请保存草稿；尚未应用。', 'Rule order changed; save the draft. Not applied yet.'); focus = `[data-route-action="${action}"][data-route-index="${index + (action === 'up' ? -1 : 1)}"]`; }
      if (action === 'delete') { routeDeleting = index; focus = '#route-confirm-delete'; }
      if (action === 'confirm-delete' && routeDeleting !== null) { draft.rules = removeRoute(draft.rules, routeDeleting); routeDeleting = null; feedback = t('仅移除所选规则，请保存草稿；尚未应用。', 'Removed only the selected rule; save the draft. Not applied yet.'); }
      if (action === 'cancel-delete') routeDeleting = null;
    } catch (reason) { error = (reason as Error).message; focus = '#xray-settings-error'; }
    paint();
    const target = root.querySelector<HTMLElement>(focus);
    (target && !(target instanceof HTMLButtonElement && target.disabled) ? target : root.querySelector<HTMLElement>('#route-add'))?.focus();
  }
  function statusText(): string {
    if (loading) return t('正在读取已保存草稿…', 'Reading the saved draft…');
    if (!loaded) return t('草稿尚未读取，无法核对应用状态。', 'The draft is not loaded; cannot check the apply status.');
    if (saving) return t('正在保存草稿，保存不会自动应用核心…', 'Saving the draft; saving does not apply the core…');
    const pending = outboundEditing !== null || outboundDeleting !== null ? t('出站有未完成的编辑或删除确认，请完成或取消。', 'Outbounds have an unfinished edit or delete; finish or cancel it.') : balancerEditing !== null || balancerDeleting !== null ? t('负载均衡有未完成的编辑或删除确认，请完成或取消。', 'Balancers have an unfinished edit or delete; finish or cancel it.') : routeEditing !== null ? t('路由表单尚未加入规则列表，请完成或取消编辑。', 'The routing form is not in the rule list yet; finish or cancel the edit.') : routeDeleting !== null ? t('路由删除尚未确认，原规则仍保留。', 'The routing deletion is not confirmed; the rule is kept.') : dnsEditing ? t('DNS表单尚未更新草稿，请完成或取消编辑。', 'The DNS form has not updated the draft; finish or cancel the edit.') : '';
    const dirty = JSON.stringify(draft) !== JSON.stringify(saved) ? t('有未保存修改，当前页面修改尚未应用。', 'There are unsaved changes; changes on this page are not applied.') : '';
    return [pending || dirty, feedback, settingsStatusText(persistent, revision, runtime, runtimeAt)].filter(Boolean).join(' ');
  }
  function updateStatus(): void {
    const message = root.querySelector<HTMLElement>('#xray-settings-status');
    const text = statusText();
    if (message && message.textContent !== text) message.textContent = text;
  }
  function paint(): void {
    const content = tab === 'basic' ? `<details class="xray-setting-section" open><summary>${t('常规配置', 'General')}</summary><div class="xray-setting-content"><p class="core-warning">${t('这些选项决定核心配置草稿；保存后需另行确认应用。下方状态仅核对设置修订，不代表入站一致或代理连接验收通过。', 'These options shape the core configuration draft; after saving, apply separately. The status below checks only the settings revision, not inbounds or verified proxy connections.')}</p>
      ${select('freedomStrategy', t('Freedom协议策略', 'Freedom domain strategy'), t('设置Freedom出站的域名解析策略', 'Domain resolution strategy for Freedom outbounds'), draft.freedomStrategy, freedomStrategies)}
      ${select('routingStrategy', t('配置路由域策略', 'Routing domain strategy'), t('设置路由匹配时的域名解析策略', 'Domain resolution strategy used for routing'), draft.routingStrategy, routingStrategies)}
      <label class="xray-setting-row"><span><strong id="xray-label-probeURL">${t('出站测试URL', 'Outbound test URL')}</strong><small id="xray-hint-probeURL">${t('只保存面板草稿选项，本轮不会发起测试请求', 'Saved as a draft option only; no test request is sent')}</small></span><input name="probeURL" type="url" value="${esc(draft.probeURL)}" required aria-labelledby="xray-label-probeURL" aria-describedby="xray-hint-probeURL"></label></div></details>
      <details class="xray-setting-section"><summary>${t('统计', 'Statistics')}</summary><div class="xray-setting-content">${check('statistics', t('生成用户及入站上下行统计配置', 'Generate per-user and per-inbound traffic statistics'), draft.statistics)}<p class="form-hint">${t('保存后需应用：开启时通过受限本机套接字采集累计；非零额度必须开启。关闭期间不计流量，后续累计会标为可能不完整。离线预览不采集。', 'Takes effect after applying: when on, totals are collected through a restricted local socket; non-zero quotas require it. Traffic is not counted while off, and later totals are marked as possibly incomplete. The offline preview does not collect.')}</p></div></details>
      <details class="xray-setting-section"><summary>${t('日志', 'Logs')}</summary><div class="xray-setting-content">${select('logLevel', t('日志级别', 'Log level'), t('调试日志可能包含连接信息，使用时注意隐私', 'Debug logs may contain connection details; mind privacy'), draft.logLevel, ['none', 'error', 'warning', 'info', 'debug'])}${check('accessLog', t('开启访问日志（输出到核心控制台）', 'Enable access log (to the core console)'), draft.accessLog)}${check('dnsLog', t('开启DNS日志', 'Enable DNS log'), draft.dnsLog)}</div></details>
      <details class="xray-setting-section"><summary>${t('基本路由', 'Basic routing')}</summary><div class="xray-setting-content"><p class="form-intro">${t('默认仅保留direct与block两个出站，不自动添加屏蔽或绕过规则。在“路由规则”分类可通过表单添加域名/IP/端口/嗅探协议规则，或用JSON编辑高级规则。', 'By default only the direct and block outbounds exist, and no blocking or bypass rules are added. In Routing rules you can add domain, IP, port and sniffed-protocol rules with the form, or edit advanced rules as JSON.')}</p><button type="button" class="secondary-button" id="xray-open-routing">${t('管理路由规则', 'Manage routing rules')}</button></div></details>` : tab === 'routing' ? routeContent() : tab === 'dns' ? dnsContent() : tab === 'balancers' ? balancerContent() : tab === 'outbounds' ? outboundContent() : (() => {
        const [key, title, hint] = editorInfo[tab];
        return `<div class="xray-setting-content"><h2 class="xray-editor-title">${title}</h2><p class="form-intro" id="xray-json-hint">${hint}</p><label class="form-field"><span id="xray-json-label">${title} JSON</span><textarea id="xray-json-editor" name="${key}" rows="16" spellcheck="false" autocomplete="off" aria-labelledby="xray-json-label" aria-describedby="xray-json-hint">${esc(draft[key])}</textarea></label></div>`;
      })();
    root.innerHTML = `<div class="page-heading"><div><h1 class="page-title" tabindex="-1">${t('Xray 设置', 'Xray settings')}</h1><p>${t('六类配置独立编辑；版本切换在首页Xray弹窗。', 'Six categories are edited separately; switch versions from the Xray dialog on the dashboard.')}</p></div></div>
      <div class="card xray-settings-actions"><button class="primary-button" id="xray-save" ${!loaded || saving || loading || reloadRequired ? 'disabled' : ''}>${saving ? t('正在保存…', 'Saving…') : persistent ? t('保存草稿', 'Save draft') : t('保存会话草稿', 'Save session draft')}</button><button class="secondary-button" id="xray-preview" ${!loaded || saving || loading ? 'disabled' : ''}>${t('校验并预览', 'Verify and preview')}</button>${persistent ? `<button class="secondary-button" id="xray-reload" ${loading || saving ? 'disabled' : ''}>${t('重新读取已保存草稿', 'Reload saved draft')}</button>` : ''}<button class="secondary-button" id="xray-apply" ${!persistent || !loaded || saving || loading || reloadRequired ? 'disabled' : ''}>${t('应用已保存配置', 'Apply saved configuration')}</button></div>
      ${reloadRequired ? t('<p class="form-error" role="alert">上次保存失败或结果未确认，已保留本页输入。请先复制需要保留的修改，再重新读取已保存草稿；不会自动合并或覆盖其它页面的数据。</p>', '<p class="form-error" role="alert">The last save failed or was not confirmed; your input on this page is kept. Copy any changes you need, then reload the saved draft. Data from other pages is never merged or overwritten automatically.</p>') : ''}
      <p class="form-intro">${persistent ? t('草稿保存到本项目独立SQLite，刷新或重启后保留；保存不等于应用核心。应用会单独确认并执行官方预检，仅限本机。', 'Drafts are saved to o-ui\'s own SQLite and survive refreshes and restarts; saving does not apply the core. Applying is confirmed separately and runs the official preflight.') : t('离线演示草稿只保留到页面刷新，不写数据库、不应用核心。', 'Offline demo drafts last until the page refreshes; nothing is written to the database or applied.')}${t('路由、DNS、出站和负载均衡支持常用表单与JSON；其他高级配置使用JSON。', ' Routing, DNS, outbounds and balancers support forms and JSON; other advanced settings use JSON.')}</p>
      <section class="card xray-settings-card"><div class="xray-settings-tabs" role="tablist" aria-label="${t('Xray配置分类', 'Xray setting categories')}">${settingsTabs.map(([id, label, icon]) => `<button role="tab" id="xray-tab-${id}" aria-selected="${id === tab}" aria-controls="xray-settings-panel" tabindex="${id === tab ? 0 : -1}" data-settings-tab="${id}"><i data-lucide="${icon}" aria-hidden="true"></i>${label}</button>`).join('')}</div>
      <div id="xray-settings-panel" role="tabpanel" aria-labelledby="xray-tab-${tab}"><form id="xray-settings-form"><fieldset class="xray-edit-fields" ${!loaded || saving || loading ? 'disabled' : ''}>${content}</fieldset></form></div></section>
      <p class="core-message" id="xray-settings-status" role="status">${esc(statusText())}</p>
      ${error ? `<p class="form-error" id="xray-settings-error" tabindex="-1" role="alert">${esc(error)}</p>` : ''}
      ${preview ? `<section class="card xray-settings-preview"><h2>${t('配置片段预览（非完整可运行配置）', 'Configuration preview (not a complete runnable config)')}</h2><p class="form-hint">${t('只完成JSON结构和部分引用校验，未经过所选官方核心预检；未包含任何入站。', 'Only JSON structure and some references are checked; the selected official core has not run a preflight. No inbounds are included.')}</p><pre>${esc(preview)}</pre></section>` : ''}`;
    root.querySelectorAll<HTMLElement>('[data-balancer-field]').forEach(field => {
      const label = field.closest('label')?.querySelector('span'), hint = field.closest('label')?.querySelector('small');
      if (label) { label.id = `balancer-label-${field.dataset.balancerField}`; field.setAttribute('aria-labelledby', label.id); }
      if (hint) { hint.id = `balancer-hint-${field.dataset.balancerField}`; field.setAttribute('aria-describedby', hint.id); }
    });
    root.querySelectorAll<HTMLButtonElement>('[data-settings-tab]').forEach(button => {
      button.addEventListener('click', () => { if (loading || saving) return; capture(); tab = button.dataset.settingsTab as SettingsTab; error = ''; feedback = ''; preview = ''; paint(); root.querySelector<HTMLElement>(`#xray-tab-${tab}`)?.focus(); });
      button.addEventListener('keydown', event => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
        event.preventDefault(); const index = settingsTabs.findIndex(item => item[0] === tab);
        const next = event.key === 'Home' ? 0 : event.key === 'End' ? settingsTabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + settingsTabs.length) % settingsTabs.length;
        root.querySelector<HTMLButtonElement>(`#xray-tab-${settingsTabs[next][0]}`)?.click();
      });
    });
    root.querySelector('#xray-settings-form')!.addEventListener('submit', event => event.preventDefault());
    root.querySelectorAll<HTMLButtonElement>('[data-route-action]').forEach(button => button.addEventListener('click', () => routeAction(button.dataset.routeAction!, Number(button.dataset.routeIndex))));
    root.querySelectorAll<HTMLButtonElement>('[data-dns-action]').forEach(button => button.addEventListener('click', () => dnsAction(button.dataset.dnsAction!)));
    root.querySelectorAll<HTMLButtonElement>('[data-balancer-action]').forEach(button => button.addEventListener('click', () => balancerAction(button.dataset.balancerAction!, Number(button.dataset.balancerIndex))));
    root.querySelectorAll<HTMLButtonElement>('[data-outbound-action]').forEach(button => button.addEventListener('click', () => outboundAction(button.dataset.outboundAction!, Number(button.dataset.outboundIndex))));
    root.querySelector<HTMLSelectElement>('[data-outbound-field="protocol"]')?.addEventListener('change', () => {
      capture(); const proxy = root.querySelector<HTMLElement>('[data-outbound-proxy]');
      if (proxy) proxy.hidden = !['http', 'socks'].includes(outboundForm.protocol);
    });
    root.querySelector('#xray-open-routing')?.addEventListener('click', () => root.querySelector<HTMLButtonElement>('#xray-tab-routing')?.click());
    root.querySelector('#xray-settings-form')!.addEventListener('input', () => {
      capture(); feedback = ''; preview = ''; error = '';
      updateStatus();
      const matches = root.querySelector<HTMLElement>('[data-balancer-matches]'); if (matches) matches.textContent = `${t('当前匹配：', 'Currently matches: ')}${balancerMatches()}`;
      const dnsMode = root.querySelector<HTMLButtonElement>('[data-dns-action="json"]'); if (dnsMode) dnsMode.disabled = dnsEditing;
      root.querySelector('.xray-settings-preview')?.remove();
      root.querySelector('#xray-settings-error')?.remove();
    });
    async function validate(save: boolean): Promise<void> {
      if (!loaded || saving || loading || (save && reloadRequired)) return;
      capture(); error = ''; feedback = ''; preview = '';
      try {
        if (outboundEditing !== null || outboundDeleting !== null) throw new Error(t('出站有尚未完成的编辑或删除确认，请到“出站规则”加入列表或取消后再保存/预览。', 'Outbounds have an unfinished edit or delete; add it to the list or cancel it in “Outbounds” before saving or previewing.'));
        if (balancerEditing !== null || balancerDeleting !== null) throw new Error(t('负载均衡有尚未完成的编辑或删除确认，请到“负载均衡”加入列表或取消后再保存/预览。', 'Balancers have an unfinished edit or delete; add it to the list or cancel it in “Balancers” before saving or previewing.'));
        if (routeEditing !== null || routeDeleting !== null) throw new Error(t('路由有尚未完成的编辑或删除确认，请到“路由规则”加入列表或取消后再保存/预览。', 'Routing has an unfinished edit or delete; add it to the list or cancel it in “Routing rules” before saving or previewing.'));
        if (dnsEditing) throw new Error(t('DNS有尚未完成的表单编辑，请到“DNS”更新草稿或取消后再保存/预览。', 'DNS has an unfinished form edit; update the draft or cancel it in “DNS” before saving or previewing.'));
        const config = buildSettings(draft);
        if (save) {
          if (persistent) {
            saving = true; paint();
            const document = await api<{ revision: number; data: SettingsDraft }>('settings', jsonBody('PUT', { revision, data: draft })).catch(reason => { if (!(reason instanceof APIError) || reason.status === 409 || reason.status >= 500) reloadRequired = true; throw reason; });
            revision = document.revision;
          }
          saved = structuredClone(draft); feedback = persistent ? `${t('草稿已保存到数据库（修订', 'Draft saved to the database (revision ')}${revision}${t('）；保存不会自动应用。', '); saving does not apply automatically.')}` : t('已保存当前会话草稿；刷新会恢复默认，不应用核心。', 'Session draft saved; refreshing restores defaults and nothing is applied.');
        }
        else { preview = JSON.stringify(config, null, 2); feedback = t('基础结构及引用校验通过；本次未执行官方核心预检或更改运行核心。', 'Structure and reference checks passed; no official core preflight was run and the running core is unchanged.'); }
      } catch (reason) { error = (reason as Error).message; }
      saving = false; paint();
      root.querySelector<HTMLElement>(error ? '#xray-settings-error' : save ? '#xray-save' : '#xray-preview')?.focus({ preventScroll: !error });
    }
    root.querySelector('#xray-save')!.addEventListener('click', () => { void validate(true); });
    root.querySelector('#xray-preview')!.addEventListener('click', () => { void validate(false); });
    root.querySelector('#xray-reload')?.addEventListener('click', () => { void load(true); });
    root.querySelector('#xray-apply')!.addEventListener('click', () => {
      if (!loaded || loading || saving || reloadRequired) return;
      capture();
      if (outboundEditing !== null || outboundDeleting !== null) { error = t('请先完成或取消“出站规则”的编辑/删除确认，再保存并应用。', 'Finish or cancel the edit or delete in “Outbounds” before saving and applying.'); paint(); root.querySelector<HTMLElement>('#xray-settings-error')?.focus(); return; }
      if (balancerEditing !== null || balancerDeleting !== null) { error = t('请先完成或取消“负载均衡”的编辑/删除确认，再保存并应用。', 'Finish or cancel the edit or delete in “Balancers” before saving and applying.'); paint(); root.querySelector<HTMLElement>('#xray-settings-error')?.focus(); return; }
      if (routeEditing !== null || routeDeleting !== null) { error = t('请先完成或取消“路由规则”中的编辑/删除确认，再保存并应用。', 'Finish or cancel the edit or delete in “Routing rules” before saving and applying.'); paint(); root.querySelector<HTMLElement>('#xray-settings-error')?.focus(); return; }
      if (dnsEditing) { error = t('请先更新或取消“DNS”中的表单编辑，再保存并应用。', 'Update or cancel the form edit in “DNS” before saving and applying.'); paint(); root.querySelector<HTMLElement>('#xray-settings-error')?.focus(); return; }
      if (JSON.stringify(draft) !== JSON.stringify(saved)) { error = t('有未保存修改，请先保存草稿，再应用。', 'There are unsaved changes; save the draft before applying.'); paint(); root.querySelector<HTMLElement>('#xray-settings-error')?.focus(); return; }
      onApply();
    });
    icons();
  }
  async function load(force = false): Promise<void> {
    if ((!force && loaded) || loading || saving) return;
    if (force && loaded) {
      capture();
      if (!window.confirm(t('重新读取将丢弃本页六类设置的全部未保存修改，包括未加入列表的表单和删除确认。请先复制需要保留的内容。只读取最新草稿，不保存、不应用核心。确认继续？', 'Reloading discards all unsaved changes in the six categories on this page, including forms not added to lists and pending deletions. Copy anything you need first. It only reads the latest draft; nothing is saved or applied. Continue?'))) return;
    }
    loading = true; error = ''; feedback = ''; paint();
    try {
      const document = await api<{ revision: number; data: SettingsDraft | null }>('settings');
      const next = document.data ?? defaultSettings();
      buildSettings(next); draft = next; saved = structuredClone(draft);
      revision = document.revision; loaded = true; reloadRequired = false; preview = '';
      routeEditing = routeDeleting = balancerEditing = balancerDeleting = outboundEditing = outboundDeleting = null;
      routingJSON = dnsJSON = balancerJSON = outboundJSON = dnsEditing = false;
      routeForm = emptyRoute(); dnsForm = dnsFields(draft.dns) ?? dnsForm; balancerForm = emptyBalancer(); outboundForm = emptyOutbound();
      if (force) feedback = `${t('已重新读取数据库草稿（修订', 'Reloaded the database draft (revision ')}${revision}${t('）；未保存或应用核心。', '); nothing was saved or applied.')}`;
    } catch (reason) { error = (reason as Error).message; if (force) reloadRequired = true; }
    finally { loading = false; if (visible) { paint(); if (force) root.querySelector<HTMLElement>(error ? '#xray-settings-error' : '#xray-reload')?.focus(); } }
  }
  if (persistent) window.setInterval(() => { if (visible && !root.hidden && !document.hidden) updateStatus(); }, 1000);
  return { render: () => { visible = true; capture(); paint(); if (!loaded) void load(); }, setRuntime: (status, at) => { runtime = status; runtimeAt = at; updateStatus(); } };
}
