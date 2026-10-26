import assert from 'node:assert/strict';
import { test } from 'node:test';
import { inferPanelBase, pageSegments, pageURL, panelPage } from '../src/panel-routing.ts';

test('固定页面路径接在访问路径后，旧书签转到对应页面', () => {
  for (const base of ['/', '/secret-9/', '/panel/', '/clients/']) {
    for (const page of Object.keys(pageSegments) as (keyof typeof pageSegments)[]) {
      const path = pageURL(page, base);
      assert.equal(panelPage(path, '', base), page);
      assert.equal(panelPage(base, `#${page}`, base), page);
      assert.equal(pageURL(page, base, true), `#${page}`);
    }
    assert.equal(panelPage(base, '', base), 'home');
    assert.equal(panelPage(`${base}clients/`, '#main', base), 'clients');
    assert.equal(panelPage(base, '#constructor', base), 'home');
  }
});

test('开发模式推导根路径与自定义访问路径，保持API目录固定', () => {
  for (const path of ['/', '/panel/', '/inbounds/', '/clients/', '/settings/', '/xray/']) assert.equal(inferPanelBase(path), '/');
  for (const path of ['/secret-9/', '/secret-9/index.html', '/secret-9/panel/', '/secret-9/clients/']) assert.equal(inferPanelBase(path), '/secret-9/');
});
