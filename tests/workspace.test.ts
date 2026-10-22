import test from 'node:test';
import assert from 'node:assert/strict';
import { createQuick, defaultSniffing, deleteInbound, deleteRecords, quickInboundName, editClient, editInbound, addClient,
  seedWorkspace, clientStatus, randomName, inboundName, presets, escapeHtml, calendarDays, chineseDate } from '../src/workspace.ts';
import type { Preset, Workspace } from '../src/workspace.ts';

const now = new Date(2026, 9, 18, 12).getTime();
const client = { name: 'a1b2c3', quotaGB: 100, expiresAt: null, ipLimit: 2, subscription: false };
const empty: Workspace = { inbounds: [], clients: [] };

test('四预设一键创建同步增加关联客户端，默认命名和嗅探正确', () => {
  for (const preset of Object.keys(presets) as Preset[]) {
    const state = createQuick(empty, { ...client, preset, port: 443 }, 'a');
    assert.equal(state.inbounds[0].name, inboundName(preset, client.name));
    assert.deepEqual(state.inbounds[0].sniffing, defaultSniffing());
    assert.equal(state.clients[0].inboundId, state.inbounds[0].id);
    assert.equal(state.clients[0].subscription, false);
    assert.equal(state.clients[0].name, 'a1b2c3');
    assert.equal(empty.inbounds.length, 0);
  }
});

test('随机客户端名仅6位小写字母和数字', () => {
  assert.match(randomName(new Uint8Array([0, 25, 26, 35, 36, 255])), /^[a-z0-9]{6}$/);
  assert.equal(randomName(new Uint8Array([0, 25, 26, 35, 36, 255])), 'az09ad');
});

test('重复TCP端口被拒绝，同数字UDP端口允许，不静默覆盖', () => {
  const state = createQuick(empty, { ...client, preset: 'reality', port: 443 }, 'a');
  assert.throws(() => createQuick(state, { ...client, name: 'other', preset: 'xhttp-tls', port: 443 }, 'b'), /端口/);
  assert.equal(createQuick(state, { ...client, name: 'other', preset: 'hy2', port: 443 }, 'b').inbounds.length, 2);
  for (const port of [-1, 0, 65536, 1.5, Number.NaN]) assert.throws(() => createQuick(empty, { ...client, preset: 'hy2', port }, 'a'), /端口/);
});

test('表单边界、重名和重复标识不产生半条记录', () => {
  const state = createQuick(empty, { ...client, preset: 'hy2', port: 443 }, 'a');
  assert.throws(() => createQuick(state, { ...client, preset: 'reality', port: 8443 }, 'b'), /名称/);
  assert.throws(() => createQuick(state, { ...client, name: 'other', preset: 'reality', port: 8443 }, 'a'), /标识/);
  for (const bad of [{ quotaGB: -1 }, { quotaGB: Number.NaN }, { ipLimit: 1.5 }, { expiresAt: Number.NaN }, { name: ' ' }]) {
    assert.throws(() => createQuick(empty, { ...client, ...bad, preset: 'hy2', port: 443 }, 'x'));
  }
  assert.equal(state.inbounds.length, 1);
});

test('编辑入站保留已有嗅探值，编辑客户端不重命名原入站', () => {
  const state = seedWorkspace(now);
  const edited = editInbound(state, 'demo-1', { ...state.inbounds[0], port: 1443, sniffing: { ...defaultSniffing(), quic: false, metadataOnly: true } });
  assert.equal(edited.inbounds[0].sniffing.quic, false);
  assert.equal(edited.inbounds[0].sniffing.metadataOnly, true);
  const changed = editClient(edited, 'demo-1-client', { ...client, name: '新名称', enabled: true });
  assert.equal(changed.inbounds[0].name, state.inbounds[0].name);
  assert.equal(changed.clients[0].usedGB, 12.8);
  assert.notEqual(changed.clients[0].name, state.clients[0].name);
});

test('删除入站一并删除其客户端，可批量删除；新增需有入站', () => {
  const state = seedWorkspace(now);
  const cascaded = deleteInbound(state, 'demo-1');
  assert.equal(cascaded.inbounds.some(item => item.id === 'demo-1'), false);
  assert.equal(cascaded.clients.some(item => item.inboundId === 'demo-1'), false);
  assert.equal(cascaded.clients.length, state.clients.filter(item => item.inboundId !== 'demo-1').length);
  const bulk = deleteRecords(state, ['demo-1'], [state.clients.find(item => item.inboundId !== 'demo-1')!.id]);
  assert.equal(bulk.clients.length, cascaded.clients.length - 1);
  assert.throws(() => addClient(state, 'missing', client, 'new'), /关联入站/);
  const result = addClient(state, 'demo-1', client, 'new');
  assert.equal(result.clients.length, 5);
  assert.equal(state.clients.length, 4);
  const removed = deleteInbound({ ...state, clients: state.clients.filter(item => item.inboundId !== 'demo-1') }, 'demo-1');
  assert.equal(removed.inbounds.length, 3);
});

test('演示状态区分手动禁用、入站停用、到期和用尽，0流量上限表示不限', () => {
  const state = seedWorkspace(now), inbound = state.inbounds[0], item = state.clients[0];
  assert.equal(clientStatus(item, inbound, now), '可用');
  assert.equal(clientStatus({ ...item, enabled: false }, inbound, now), '已停用');
  assert.equal(clientStatus(item, { ...inbound, enabled: false }, now), '入站已停用');
  assert.equal(clientStatus({ ...item, expiresAt: now }, inbound, now), '已到期');
  assert.equal(clientStatus({ ...item, quotaGB: 10 }, inbound, now), '流量已用尽');
  assert.equal(clientStatus({ ...item, quotaGB: 0 }, inbound, now), '可用');
});

test('中文日期、闰月日历与HTML转义', () => {
  assert.equal(chineseDate(now), '2026年10月18日 12:00');
  assert.equal(chineseDate(null), '永不过期');
  assert.equal(calendarDays(2024, 1).filter(Boolean).length, 29);
  assert.equal(calendarDays(2026, 1).filter(Boolean).length, 28);
  assert.equal(calendarDays(2026, 9).slice(0, 4).every(value => value === null), true);
  assert.equal(escapeHtml('<img onerror="x">&\''), '&lt;img onerror=&quot;x&quot;&gt;&amp;&#39;');
});

test('反向代理标签只用于VLESS入站且唯一，留空时不写入', () => {
  let state: Workspace = createQuick({ inbounds: [], clients: [] }, { ...client, preset: 'reality', port: 18443 }, 'vless');
  state = createQuick(state, { ...client, name: 'hy2one', preset: 'hy2', port: 18444 }, 'hy');
  state = editClient(state, 'vless-client', { ...client, enabled: true, reverseTag: ' r-home ' });
  assert.equal(state.clients[0].reverseTag, 'r-home');
  assert.throws(() => addClient(state, 'vless', { ...client, name: 'other', reverseTag: 'r-home' }, 'dup'), /已被|already/);
  assert.throws(() => editClient(state, 'hy-client', { ...client, name: 'hy2one', enabled: true, reverseTag: 'r-hy' }), /HY2/);
  assert.throws(() => editClient(state, 'vless-client', { ...client, enabled: true, reverseTag: 'r home' }), /反向|Reverse/);
  state = editClient(state, 'vless-client', { ...client, enabled: true, reverseTag: undefined });
  assert.equal('reverseTag' in JSON.parse(JSON.stringify(state.clients[0])), false);
});

test('一键创建入站名称为地区-类型-日期，同名依次加(1)(2)', () => {
  const day = new Date(2026, 9, 22, 9, 30);
  assert.equal(quickInboundName('台湾', 'HY2', day, []), '台湾-HY2-2026.10.22');
  assert.equal(quickInboundName('台湾', 'HY2', day, ['台湾-HY2-2026.10.22']), '台湾-HY2-2026.10.22(1)');
  assert.equal(quickInboundName('台湾', 'HY2', day, ['台湾-HY2-2026.10.22', '台湾-HY2-2026.10.22(1)']), '台湾-HY2-2026.10.22(2)');
  assert.equal(quickInboundName('台湾', 'Reality', day, ['台湾-HY2-2026.10.22']), '台湾-Reality-2026.10.22');
  assert.equal(quickInboundName('美国', 'HY2', new Date(2026, 0, 5), []), '美国-HY2-2026.01.05');
});

test('流量自动单位：字节取整，去掉多余的0', async () => {
  const { formatBytes } = await import('../src/traffic.ts');
  assert.equal(formatBytes(218.83), '219 B');
  assert.equal(formatBytes(50e9), '50 GB');
  assert.equal(formatBytes(41_500_000), '41.5 MB');
  assert.equal(formatBytes(2_850_000), '2.85 MB');
  assert.equal(formatBytes(1_500_000_000n), '1.5 GB');
  assert.equal(formatBytes(null), '未采集');
});
