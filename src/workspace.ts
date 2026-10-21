import { t } from './i18n.ts';
export const presets = {
  hy2: { label: 'HY2', protocol: 'Hysteria 2', transport: 'UDP', security: 'TLS', port: 443 },
  reality: { label: 'Reality', protocol: 'VLESS', transport: 'TCP', security: 'REALITY', port: 8443 },
  'xhttp-tls': { label: 'XHTTP-TLS', protocol: 'VLESS', transport: 'TCP', security: 'TLS', port: 2053 },
  'xhttp-reality': { label: 'XHTTP-Reality', protocol: 'VLESS', transport: 'TCP', security: 'REALITY', port: 2083 },
} as const;
export type Preset = keyof typeof presets;
export type Sniffing = { enabled: boolean; http: boolean; tls: boolean; quic: boolean; routeOnly: boolean; fakeDns: boolean; metadataOnly: boolean; ipsExcluded?: string[]; domainsExcluded?: string[] };
export type Connection = { certificateFile: string; keyFile: string; target: string; serverName: string; path: string; mode: string; usePanelCertificate?: boolean; mihomoCompatibility?: boolean };
export const defaultConnection = (): Connection => ({ certificateFile: '', keyFile: '', target: '', serverName: '', path: '/', mode: 'auto' });
export type Inbound = { id: string; name: string; preset: Preset; port: number; enabled: boolean; sniffing: Sniffing; connection?: Connection; listen?: string; shareHost?: string; portHopping?: string };
export type Client = { id: string; inboundId: string; name: string; enabled: boolean; quotaGB: number; usedGB: number; expiresAt: number | null; ipLimit: number; subscription: boolean; reverseTag?: string };
export type Workspace = { inbounds: Inbound[]; clients: Client[] };
export type ClientInput = Pick<Client, 'name' | 'quotaGB' | 'expiresAt' | 'ipLimit' | 'subscription' | 'reverseTag'>;

export const defaultSniffing = (): Sniffing => ({
  enabled: true, http: true, tls: true, quic: true, routeOnly: true, fakeDns: false, metadataOnly: false,
});

export function randomName(bytes: Uint8Array = crypto.getRandomValues(new Uint8Array(6))): string {
  const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789';
  return Array.from(bytes.slice(0, 6), value => alphabet[value % alphabet.length]).join('');
}

export function inboundName(preset: Preset, name: string): string {
  return `${t('🇺🇸 美国-', '🇺🇸 United States-')}${presets[preset].label}-${name}`;
}

export function validateClient(input: ClientInput): void {
  if (!input.name.trim() || input.name.trim().length > 64) throw new Error(t('客户端名称需为1至64个字符。', 'Client name must be 1–64 characters.'));
  if (!Number.isFinite(input.quotaGB) || input.quotaGB < 0 || input.quotaGB > 1_000_000 || (input.quotaGB > 0 && input.quotaGB * 1e9 < 1)) throw new Error(t('流量上限需为0至1000000 GB，0表示不限；非零额度最小1字节。', 'Traffic quota must be 0–1000000 GB (0 means unlimited); a non-zero quota is at least 1 byte.'));
  if (!Number.isInteger(input.ipLimit) || input.ipLimit < 0 || input.ipLimit > 10000) throw new Error(t('IP上限需为0至10000的整数，0表示不限。', 'IP limit must be an integer from 0 to 10000 (0 means unlimited).'));
  if (input.expiresAt !== null && (!Number.isFinite(input.expiresAt) || input.expiresAt < 0 || input.expiresAt > 8.64e15)) throw new Error(t('请选择有效的到期日期。', 'Choose a valid expiry date.'));
  if (input.reverseTag?.trim() && !/^[A-Za-z0-9_.-]{1,64}$/.test(input.reverseTag.trim())) throw new Error(t('反向代理标签只能含字母、数字、点、下划线和连字符（最多64个）。', 'Reverse tags may contain only letters, digits, dots, underscores and hyphens (up to 64).'));
}

// 反向代理标签只用于VLESS入站，且全局唯一（核心按标签生成同名出站）。
function validateReverse(state: Workspace, inboundId: string, input: ClientInput, exceptId?: string): Pick<Client, 'reverseTag'> {
  const tag = input.reverseTag?.trim() ?? '';
  if (!tag) return { reverseTag: undefined };
  if (state.inbounds.find(item => item.id === inboundId)?.preset === 'hy2') throw new Error(t('HY2 入站不支持反向代理，请选择 VLESS 入站。', 'HY2 inbounds do not support reverse proxy; choose a VLESS inbound.'));
  if (state.clients.some(item => item.id !== exceptId && item.reverseTag === tag)) throw new Error(t('反向代理标签已被其他客户端使用。', 'This reverse tag is already used by another client.'));
  return { reverseTag: tag };
}

function validatePort(state: Workspace, preset: Preset, port: number, exceptId?: string): void {
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error(t('端口需为1至65535的整数。', 'Port must be an integer from 1 to 65535.'));
  if (state.inbounds.some(item => item.id !== exceptId && item.port === port && presets[item.preset].transport === presets[preset].transport)) {
    throw new Error(`${presets[preset].transport}${t('端口', ' port')} ${port} ${t('已被演示入站使用，请换一个端口。', 'is already used by a demo inbound; choose another port.')}`);
  }
}

export function createQuick(state: Workspace, input: ClientInput & { preset: Preset; port: number; sniffing?: Sniffing }, id: string): Workspace {
  if (!Object.hasOwn(presets, input.preset)) throw new Error(t('请选择有效的创建预设。', 'Choose a valid preset.'));
  validatePort(state, input.preset, input.port);
  validateClient(input);
  if (state.clients.some(item => item.name === input.name.trim())) throw new Error(t('客户端名称已存在，请换一个名称。', 'This client name already exists; choose another.'));
  if (!id || state.inbounds.some(item => item.id === id) || state.clients.some(item => item.id === id)) throw new Error(t('演示记录标识重复，请重试。', 'Duplicate demo record ID; please retry.'));
  const clientId = `${id}-client`;
  if (state.clients.some(item => item.id === clientId)) throw new Error(t('演示客户端标识重复，请重试。', 'Duplicate demo client ID; please retry.'));
  const name = input.name.trim();
  return {
    inbounds: [...state.inbounds, { id, name: inboundName(input.preset, name), preset: input.preset, port: input.port, enabled: true, sniffing: structuredClone(input.sniffing ?? defaultSniffing()) }],
    clients: [...state.clients, { id: clientId, inboundId: id, name, enabled: true, usedGB: 0, quotaGB: input.quotaGB, expiresAt: input.expiresAt, ipLimit: input.ipLimit, subscription: input.subscription }],
  };
}

export function editInbound(state: Workspace, id: string, input: Pick<Inbound, 'name' | 'port' | 'enabled' | 'sniffing'>): Workspace {
  const item = state.inbounds.find(record => record.id === id);
  if (!item) throw new Error(t('入站不存在，请刷新列表。', 'The inbound does not exist; refresh the list.'));
  if (!input.name.trim() || input.name.trim().length > 128) throw new Error(t('入站名称需为1至128个字符。', 'Inbound name must be 1–128 characters.'));
  validatePort(state, item.preset, input.port, id);
  return { ...state, inbounds: state.inbounds.map(record => record.id === id ? { ...record, ...input, name: input.name.trim(), sniffing: structuredClone(input.sniffing) } : record) };
}

export function editClient(state: Workspace, id: string, input: ClientInput & { enabled: boolean }): Workspace {
  if (!state.clients.some(item => item.id === id)) throw new Error(t('客户端不存在，请刷新列表。', 'The client does not exist; refresh the list.'));
  validateClient(input);
  if (state.clients.some(item => item.id !== id && item.name === input.name.trim())) throw new Error(t('客户端名称已存在。', 'This client name already exists.'));
  const reverse = validateReverse(state, state.clients.find(item => item.id === id)!.inboundId, input, id);
  return { ...state, clients: state.clients.map(item => item.id === id ? { ...item, ...input, name: input.name.trim(), ...reverse } : item) };
}

export function addClient(state: Workspace, inboundId: string, input: ClientInput, id: string): Workspace {
  if (!state.inbounds.some(item => item.id === inboundId)) throw new Error(t('请选择有效的关联入站。', 'Choose a valid linked inbound.'));
  validateClient(input);
  if (state.clients.some(item => item.name === input.name.trim())) throw new Error(t('客户端名称已存在。', 'This client name already exists.'));
  if (!id || state.clients.some(item => item.id === id)) throw new Error(t('演示记录标识重复。', 'Duplicate demo record ID.'));
  const reverse = validateReverse(state, inboundId, input);
  return { ...state, clients: [...state.clients, { ...input, name: input.name.trim(), id, inboundId, enabled: true, usedGB: 0, ...reverse }] };
}

export function deleteInbound(state: Workspace, id: string): Workspace {
  if (state.clients.some(item => item.inboundId === id)) throw new Error(t('入站仍有关联客户端，请逐个处理客户端后再删除入站；不会自动批量删除。', 'The inbound still has linked clients; handle them one by one before deleting it (no bulk deletion).'));
  return { ...state, inbounds: state.inbounds.filter(item => item.id !== id) };
}

// 状态文字随界面语言变化，比较时须用同一t()对照。
export type ClientStatus = string;
export function clientStatus(client: Client, inbound: Inbound | undefined, now = Date.now()): ClientStatus {
  if (!client.enabled) return t('已停用', 'Disabled');
  if (!inbound?.enabled) return t('入站已停用', 'Inbound disabled');
  if (client.expiresAt !== null && client.expiresAt <= now) return t('已到期', 'Expired');
  if (client.quotaGB > 0 && client.usedGB >= client.quotaGB) return t('流量已用尽', 'Quota used up');
  return t('可用', 'Available');
}

export function seedWorkspace(now = Date.now()): Workspace {
  const day = 86_400_000;
  const state: Workspace = { inbounds: [], clients: [] };
  const names = ['ab3k9x', 'r7m2pq', 'c8vt4n', 'd5fw6z'];
  let result = state;
  (Object.keys(presets) as Preset[]).forEach((preset, index) => {
    result = createQuick(result, { name: names[index], preset, port: presets[preset].port, quotaGB: index === 1 ? 0 : 100, expiresAt: index === 2 ? now - day : now + (index + 1) * 15 * day, ipLimit: 2, subscription: false }, `demo-${index + 1}`);
  });
  return { ...result, clients: result.clients.map((item, index) => ({ ...item, enabled: index !== 3, usedGB: [12.8, 36.4, 8.2, 0][index] })) };
}

export function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[character]!));
}

export function chineseDate(value: number | null): string {
  if (value === null) return t('永不过期', 'Never expires');
  const date = new Date(value);
  const pad = (part: number) => String(part).padStart(2, '0');
  return `${date.getFullYear()}${t('年', '-')}${date.getMonth() + 1}${t('月', '-')}${date.getDate()}${t('日', '')} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function calendarDays(year: number, month: number): (number | null)[] {
  const before = new Date(year, month, 1).getDay();
  const days = new Date(year, month + 1, 0).getDate();
  return [...Array<null>(before).fill(null), ...Array.from({ length: days }, (_, index) => index + 1)];
}
