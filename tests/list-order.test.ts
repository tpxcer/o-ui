import test from 'node:test';
import assert from 'node:assert/strict';
import { mergeVisibleOrder, orderedRecords } from '../src/list-order.ts';

const records = ['a', 'b', 'c', 'd'].map(id => ({ id }));
test('显示排序独立于业务数组，忽略已删除记录，新增记录稳定追加', () => {
  assert.deepEqual(orderedRecords(records, ['gone', 'c', 'a']).map(item => item.id), ['c', 'a', 'b', 'd']);
  assert.deepEqual(records.map(item => item.id), ['a', 'b', 'c', 'd']);
});
test('筛选排序保留隐藏位置并清理已删除记录', () => {
  assert.deepEqual(mergeVisibleOrder(records, ['gone', 'd', 'a', 'b', 'c'], ['c', 'a']), ['d', 'c', 'b', 'a']);
  assert.deepEqual(mergeVisibleOrder(records, [], ['d', 'c', 'b', 'a']), ['d', 'c', 'b', 'a']);
  assert.throws(() => mergeVisibleOrder(records, [], ['a', 'a']));
  assert.throws(() => mergeVisibleOrder(records, [], ['gone']));
});
