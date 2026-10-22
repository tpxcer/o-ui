import test from 'node:test';
import assert from 'node:assert/strict';
import { defaultSettings } from '../src/xray-settings-model.ts';
import {
  balancerFromForm, dnsServerForm, emptyRule, dnsServerFromForm, outboundAddress, outboundForm, outboundFromForm, outboundFromLink, outboundReverseTag, parseParts, ruleForm, ruleFromForm,
  ruleValues, setRuleValues, syncObservatory, writeParts,
} from '../src/xray-config.ts';

test('设置草稿与可编辑对象互转不丢字段', () => {
  const draft = defaultSettings();
  const parts = parseParts(draft);
  assert.deepEqual(parseParts(writeParts(draft, parts)), parts);
});

test('基础路由按出站与字段读写同一条规则，空值时删除', () => {
  let rules = setRuleValues([{ type: 'field', outboundTag: 'direct', domain: ['geosite:cn'], ip: ['1.1.1.1'] }], 'block', 'protocol', ['bittorrent']);
  assert.deepEqual(rules[0], { type: 'field', outboundTag: 'block', protocol: ['bittorrent'] });
  rules = setRuleValues(rules, 'block', 'ip', ['geoip:private']);
  assert.deepEqual(ruleValues(rules, 'block', 'ip'), ['geoip:private']);
  rules = setRuleValues(rules, 'block', 'protocol', []);
  assert.equal(ruleValues(rules, 'block', 'protocol').length, 0);
  assert.equal(rules.length, 2, '混合条件的自定义规则不被基础路由改写');
});

test('路由规则表单参照OUI：入站、用户、来源、VLESS路由、属性等条件，往返一致', () => {
  const form = { ...emptyRule(), domain: 'geosite:openai, example.com', port: '443', sourceIP: '10.0.0.0/8', network: 'tcp,udp', protocol: ['tls'], inboundTag: ['in-a'], user: 'client-b', vlessRoute: '53,443', attrs: [[':method', 'GET'], ['', 'ignored']] as [string, string][], balancerTag: 'auto' };
  const rule = ruleFromForm(form);
  assert.deepEqual(rule, { type: 'field', domain: ['geosite:openai', 'example.com'], source: ['10.0.0.0/8'], protocol: ['tls'], inboundTag: ['in-a'], user: ['client-b'], port: '443', vlessRoute: '53,443', network: 'tcp,udp', attrs: { ':method': 'GET' }, balancerTag: 'auto' });
  assert.deepEqual(ruleFromForm(ruleForm(rule)!), rule);
  assert.equal(ruleForm({ type: 'field', sourceIP: ['192.0.2.0/24'], outboundTag: 'direct' })!.sourceIP, '192.0.2.0/24', '兼容sourceIP写法');
  assert.equal(ruleForm({ type: 'field', outboundTag: 'direct', attrs: { a: 1 } }), null, '属性值不是字符串时改用JSON编辑');
  assert.equal(ruleForm({ type: 'field', outboundTag: 'direct', localIP: ['127.0.0.1'] }), null, '表单不支持的字段改用JSON编辑');
  assert.throws(() => ruleFromForm({ ...emptyRule(), outboundTag: 'direct' }), /匹配条件/);
  assert.throws(() => ruleFromForm({ ...form, outboundTag: 'direct' }), /只能选一个/);
  assert.throws(() => ruleFromForm({ ...form, balancerTag: '' }), /只能选一个/);
});

test('分享链接导入：vless reality、vmess、trojan、ss、hysteria2', () => {
  const vless = outboundFromLink('vless://11111111-2222-4333-8444-555555555555@1.2.3.4:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.amd.com&fp=chrome&pbk=PUB&sid=ab&spx=%2F&type=tcp#node');
  assert.deepEqual(outboundFromForm(vless), { tag: 'node', protocol: 'vless', settings: { vnext: [{ address: '1.2.3.4', port: 443, users: [{ id: '11111111-2222-4333-8444-555555555555', encryption: 'none', flow: 'xtls-rprx-vision' }] }] }, streamSettings: { network: 'tcp', security: 'reality', realitySettings: { serverName: 'www.amd.com', fingerprint: 'chrome', password: 'PUB', shortId: 'ab', spiderX: '/' } } });
  const vmess = outboundFromLink(`vmess://${btoa(JSON.stringify({ ps: 'vm', add: 'v.example', port: '8443', id: 'uuid-1', net: 'ws', path: '/ws', host: 'h.example', tls: 'tls', sni: 's.example' }))}`);
  assert.equal(vmess.protocol, 'vmess'); assert.equal(vmess.network, 'ws'); assert.equal(vmess.security, 'tls'); assert.equal(vmess.serverName, 's.example');
  const trojan = outboundFromLink('trojan://secret@t.example:443?sni=t.example#tj');
  assert.equal(trojan.security, 'tls'); assert.equal(trojan.password, 'secret');
  const ss = outboundFromLink(`ss://${btoa('aes-128-gcm:pw')}@s.example:8388#ss`);
  assert.equal(ss.method, 'aes-128-gcm'); assert.equal(ss.password, 'pw');
  const hy2 = outboundFromForm(outboundFromLink('hysteria2://auth@h.example:443/?sni=h.example&insecure=1#hy'));
  assert.deepEqual(hy2, { tag: 'hy', protocol: 'hysteria', settings: { version: 2, address: 'h.example', port: 443 }, streamSettings: { network: 'hysteria', security: 'tls', hysteriaSettings: { version: 2, auth: 'auth' }, tlsSettings: { serverName: 'h.example', fingerprint: 'chrome', allowInsecure: true } } });
  assert.throws(() => outboundFromLink('wireguard://x'), /只支持|Only/);
});

test('面板导出的出站JSON可回填表单并原样生成', () => {
  const exported = { tag: 'proxy', protocol: 'vless', settings: { vnext: [{ address: 'a.example', port: 2083, users: [{ id: 'u', encryption: 'none' }] }] }, streamSettings: { network: 'xhttp', security: 'reality', realitySettings: { serverName: 'www.amd.com', fingerprint: 'chrome', password: 'PUB', shortId: '01', spiderX: '/' }, xhttpSettings: { path: '/p', mode: 'auto' } } };
  assert.deepEqual(outboundFromForm(outboundForm(exported)!), exported);
  assert.equal(outboundForm({ tag: 'wg', protocol: 'wireguard' }), null);
  assert.deepEqual(outboundFromForm(outboundForm({ tag: 'direct', protocol: 'freedom', settings: {} })!), { tag: 'direct', protocol: 'freedom', settings: {} });
});

test('最低延迟/最低负载均衡自动维护观测配置', () => {
  const parts = parseParts(defaultSettings());
  const balancer = balancerFromForm({ tag: 'auto', strategy: 'leastPing', selector: ['direct'], fallbackTag: 'direct' });
  assert.deepEqual(balancer, { tag: 'auto', selector: ['direct'], strategy: { type: 'leastPing' }, fallbackTag: 'direct' });
  const synced = syncObservatory({ ...parts, balancers: [balancer] }, 'https://probe.test/');
  assert.deepEqual(synced.advanced.observatory, { subjectSelector: ['direct'], probeURL: 'https://probe.test/', probeInterval: '1m' });
  assert.equal(syncObservatory({ ...synced, balancers: [] }, 'https://probe.test/').advanced.observatory, undefined);
});

test('DNS服务器表单：简单地址保持字符串，带条件时为对象', () => {
  assert.equal(dnsServerFromForm(dnsServerForm('1.1.1.1')), '1.1.1.1');
  assert.deepEqual(dnsServerFromForm({ address: '8.8.8.8', port: '53', domains: 'geosite:google', expectIPs: '', skipFallback: true }), { address: '8.8.8.8', port: 53, domains: ['geosite:google'], skipFallback: true });
  assert.throws(() => dnsServerFromForm({ address: '', port: '', domains: '', expectIPs: '', skipFallback: false }), /DNS/);
});

test('VLESS反向代理桥接端用简化写法，往返一致；无反向时仍为vnext', () => {
  const form = { ...outboundForm({ tag: 'home', protocol: 'vless', settings: { vnext: [{ address: 'p.example', port: 443, users: [{ id: 'u', encryption: 'none', flow: 'xtls-rprx-vision' }] }] }, streamSettings: { network: 'tcp', security: 'reality', realitySettings: { serverName: 's.example', fingerprint: 'chrome', password: 'PUB', shortId: '01', spiderX: '/' } } })!, reverseTag: 'r-bridge' };
  const bridge = outboundFromForm(form);
  assert.deepEqual(bridge.settings, { address: 'p.example', port: 443, id: 'u', encryption: 'none', flow: 'xtls-rprx-vision', reverse: { tag: 'r-bridge' } });
  assert.deepEqual(outboundFromForm(outboundForm(bridge)!), bridge);
  assert.equal(outboundReverseTag(bridge), 'r-bridge');
  assert.ok('vnext' in (outboundFromForm({ ...form, reverseTag: '' }).settings as object));
  assert.equal(outboundForm({ ...bridge, settings: { ...(bridge.settings as object), reverse: { tag: 'r', sniffing: {} } } }), null, '表单不支持的reverse字段改用JSON编辑');
  assert.throws(() => outboundFromForm({ ...form, reverseTag: 'r bridge' }), /反向|Reverse/);
});

test('WireGuard（WARP）出站在列表中显示对端地址', () => {
  assert.equal(outboundAddress({ tag: 'warp', protocol: 'wireguard', settings: { address: ['172.16.0.2/32'], peers: [{ endpoint: 'engage.cloudflareclient.com:2408' }] } }), 'engage.cloudflareclient.com:2408');
});
