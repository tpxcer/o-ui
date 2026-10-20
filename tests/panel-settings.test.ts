import test from 'node:test';
import assert from 'node:assert/strict';
import { accessURL, generalForm, generalRequest, isLoopback, remoteConfirmation, type GeneralForm, type GeneralStatus } from '../src/panel-access.ts';
import { subscriptionForm, subscriptionRequest } from '../src/subscription-settings.ts';

const status = (overrides: Partial<GeneralStatus['access']> = {}, tls: GeneralStatus['tls']['data'] = null): GeneralStatus => ({
  access: { revision: 2, data: null, active: { public: false, listen: '127.0.0.1:5180', origin: '' }, pending: false, ...overrides },
  tls: { revision: 3, data: tls, active: false, serverNames: [], pending: false },
  session: { revision: 4, minutes: 720 },
});
const form = (values: Partial<GeneralForm> = {}): GeneralForm => ({ listenIP: '127.0.0.1', domain: '', port: '5180', path: '/', sessionMinutes: '720', certificateFile: '', keyFile: '', ...values });

test('常规表单从已保存设置还原：未保存时用本次启动值，对外访问拆出域名，0.0.0.0显示为留空', () => {
  assert.deepEqual(generalForm(status()), form());
  const remote = status({ data: { public: true, listen: '0.0.0.0:23456', origin: 'https://panel.example.com:23456', basePath: '/abc/' } }, { certificateFile: '/etc/o-ui/tls/fullchain.pem', keyFile: '/etc/o-ui/tls/key.pem' });
  assert.deepEqual(generalForm(remote), { listenIP: '', domain: 'panel.example.com', port: '23456', path: '/abc/', sessionMinutes: '720', certificateFile: '/etc/o-ui/tls/fullchain.pem', keyFile: '/etc/o-ui/tls/key.pem' });
  const v6 = status({ data: { public: true, listen: '[::]:30000', origin: 'https://[2001:db8::1]:30000' } });
  assert.deepEqual([generalForm(v6).listenIP, generalForm(v6).domain, generalForm(v6).port], ['::', '2001:db8::1', '30000']);
});

test('回环监听为仅本机且不需要证书；其余为固定域名HTTPS，需证书并带确认口令', () => {
  assert.ok(isLoopback('127.0.0.1') && isLoopback('127.8.0.1') && isLoopback('::1') && !isLoopback('0.0.0.0') && !isLoopback('10.0.0.1'));
  const local = generalRequest(form({ sessionMinutes: '60', path: 'mgmt' }), status());
  assert.deepEqual(local.body, { accessRevision: 2, tlsRevision: 3, sessionRevision: 4, access: { public: false, listen: '127.0.0.1:5180', origin: '', basePath: '/mgmt/' }, tls: { certificateFile: '', keyFile: '' }, sessionMinutes: 60, confirmation: '' });
  assert.equal(local.enablesRemote, false);
  const remote = generalRequest(form({ listenIP: '', domain: 'Panel.Example.COM', port: '23456', certificateFile: '/c.pem', keyFile: '/k.pem' }), status());
  assert.deepEqual(remote.body.access, { public: true, listen: '0.0.0.0:23456', origin: 'https://panel.example.com:23456' });
  assert.equal(remote.body.confirmation, remoteConfirmation);
  assert.equal(remote.enablesRemote, true, '由本机改为对外访问要再确认');
  const already = generalRequest(form({ listenIP: '', domain: 'panel.example.com', port: '23456', certificateFile: '/c.pem', keyFile: '/k.pem' }), status({ data: { public: true, listen: '0.0.0.0:23456', origin: 'https://panel.example.com:23456' } }));
  assert.equal(already.enablesRemote, false, '已是对外访问时不重复弹确认');
  const v6 = generalRequest(form({ listenIP: '::', domain: '2001:db8::1', port: '30000', certificateFile: '/c.pem', keyFile: '/k.pem' }), status());
  assert.deepEqual([v6.body.access.listen, v6.body.access.origin], ['[::]:30000', 'https://[2001:db8::1]:30000']);
});

test('常规表单在提交前拒绝无效输入并给出可读原因', () => {
  for (const [values, message] of [
    [{ listenIP: 'example.com' }, /监听IP/], [{ listenIP: '1.2.3' }, /监听IP/],
    [{ port: '80' }, /端口/], [{ port: '65536' }, /端口/], [{ port: '5180.5' }, /端口/],
    [{ path: '/a/b/' }, /访问路径/], [{ path: '/api/' }, /访问路径/], [{ path: '/sub' }, /访问路径/], [{ path: '/a b/' }, /访问路径/],
    [{ sessionMinutes: '4' }, /会话时长/], [{ sessionMinutes: '43201' }, /会话时长/], [{ sessionMinutes: '1.5' }, /会话时长/],
    [{ certificateFile: '/c.pem' }, /同时填写/], [{ certificateFile: 'c.pem', keyFile: 'k.pem' }, /绝对路径/],
    [{ listenIP: '' }, /监听域名/], [{ listenIP: '0.0.0.0', domain: 'panel.example.com' }, /证书/],
  ] as [Partial<GeneralForm>, RegExp][]) {
    assert.throws(() => generalRequest(form(values), status()), message, JSON.stringify(values));
  }
  for (const path of ['/', '', 'abc', '/abc', 'abc/', '/A_b-9/']) assert.doesNotThrow(() => generalRequest(form({ path }), status()), path);
});

test('访问地址：对外为来源加路径，仅本机沿用当前浏览器地址（如SSH隧道）', () => {
  assert.equal(accessURL({ public: true, listen: '0.0.0.0:23456', origin: 'https://panel.example.com:23456', basePath: '/abc/' }), 'https://panel.example.com:23456/abc/');
  assert.equal(accessURL({ public: false, listen: '127.0.0.1:5180', origin: '' }, { protocol: 'http:', host: '127.0.0.1:15180' } as Location), 'http://127.0.0.1:15180/');
});

test('订阅设置：更新间隔与标题按后端规则校验，开关原样提交', () => {
  const form = subscriptionForm({ enabled: true, updateHours: 12, title: 'o-ui', showInfo: true, remarkClientName: true });
  assert.deepEqual(subscriptionRequest({ ...form, enabled: false, title: '  我的节点  ', updateHours: '24', remarkClientName: false }), { enabled: false, updateHours: 24, title: '我的节点', showInfo: true, remarkClientName: false });
  for (const updateHours of ['0', '169', '1.5', '']) assert.throws(() => subscriptionRequest({ ...form, updateHours }), /1至168小时/);
  assert.throws(() => subscriptionRequest({ ...form, title: 'x'.repeat(65) }), /64个字符/);
  assert.throws(() => subscriptionRequest({ ...form, title: 'a\u0007' }), /控制字符/);
});
