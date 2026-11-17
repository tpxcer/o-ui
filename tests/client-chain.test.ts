import test from 'node:test';
import assert from 'node:assert/strict';
import { clientProxyTargets } from '../src/client-chain.ts';
import { parseParts } from '../src/xray-config.ts';
import { defaultSettings } from '../src/xray-settings-model.ts';
import type { Client } from '../src/workspace.ts';

const client = { id: 'fixture-user', inboundId: 'fixture-in', inboundIds: ['fixture-in', 'other-in'] } as Client;
const parts = () => ({ ...parseParts(defaultSettings()), outbounds: [{ tag: 'direct', protocol: 'freedom' }, { tag: 'block', protocol: 'blackhole' }, { tag: 'proxy', protocol: 'http' }, { tag: 'socks', protocol: 'socks' }] });

test('链式代理标记：用户与入站同时匹配，部分域名分流也可定位出站', () => {
  const config = parts();
  config.rules = [{ type: 'field', user: ['client-fixture-user'], inboundTag: ['in-fixture-in'], domain: ['example.test'], outboundTag: 'proxy' }];
  assert.deepEqual(clientProxyTargets(client, config), ['proxy']);
  assert.deepEqual(clientProxyTargets({ ...client, id: 'unrelated' }, config), []);
  assert.deepEqual(clientProxyTargets({ ...client, inboundId: 'unrelated', inboundIds: ['unrelated'] }, config), []);
});

test('链式代理标记遵循规则顺序，直连阻断无标记，默认代理仅用于未终止的路径', () => {
  const config = parts();
  config.rules = [{ type: 'field', user: ['client-fixture-user'], outboundTag: 'direct' }, { type: 'field', user: ['client-fixture-user'], outboundTag: 'proxy' }];
  assert.deepEqual(clientProxyTargets(client, config), []);
  config.rules[0].outboundTag = 'block'; assert.deepEqual(clientProxyTargets(client, config), []);
  config.rules = []; config.outbounds.reverse(); assert.deepEqual(clientProxyTargets(client, config), ['socks']);
  config.rules = [{ type: 'field', inboundTag: ['in-fixture-in'], outboundTag: 'direct' }];
  assert.deepEqual(clientProxyTargets(client, config), ['socks'], '另一入站仍使用默认代理');
});

test('链式代理定位：均衡成员和回退、显式代理链，循环或丢失引用不误标', () => {
  const config = parts();
  config.balancers = [{ tag: 'auto', selector: ['proxy'], fallbackTag: 'socks' }];
  config.rules = [{ type: 'field', user: ['client-fixture-user'], balancerTag: 'auto' }];
  assert.deepEqual(clientProxyTargets(client, config), ['proxy', 'socks']);
  config.outbounds.unshift({ tag: 'chained', protocol: 'freedom', streamSettings: { sockopt: { dialerProxy: 'proxy' } } } as any);
  config.rules = []; assert.deepEqual(clientProxyTargets(client, config), ['chained']);
  (config.outbounds[0] as any).streamSettings.sockopt.dialerProxy = 'chained'; assert.deepEqual(clientProxyTargets(client, config), []);
  (config.outbounds[0] as any).streamSettings.sockopt.dialerProxy = 'missing'; assert.deepEqual(clientProxyTargets(client, config), []);
});
