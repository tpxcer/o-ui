export type ListKind = 'inbounds' | 'clients';
export type ListOrder = { revision: number; data: string[] };

export function orderedRecords<T extends { id: string }>(records: T[], order: string[]): T[] {
  const positions = new Map(order.map((id, index) => [id, index]));
  return [...records].sort((a, b) => (positions.get(a.id) ?? order.length) - (positions.get(b.id) ?? order.length));
}

// 筛选后的拖动只交换可见记录的位置，隐藏记录保持原位置；删除的 ID 不再保存。
export function mergeVisibleOrder(records: { id: string }[], order: string[], visible: string[]): string[] {
  const all = orderedRecords(records, order).map(item => item.id);
  const moving = new Set(visible);
  if (moving.size !== visible.length || visible.some(id => !all.includes(id))) throw new Error('Invalid visible order');
  let index = 0;
  return all.map(id => moving.has(id) ? visible[index++]! : id);
}
