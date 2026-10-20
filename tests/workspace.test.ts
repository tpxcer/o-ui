import test from 'node:test';
import assert from 'node:assert/strict';
import { createQuick, defaultSniffing, deleteInbound, editClient, editInbound, addClient,
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

test('有客户端不能删除入站，不自动关联批删；新增需有入站', () => {
  const state = seedWorkspace(now);
  assert.throws(() => deleteInbound(state, 'demo-1'), /关联客户端/);
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
