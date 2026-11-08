import { t } from './i18n.ts';
// o-ui的统计接口监听Unix套接字，官方v26.5.3起才支持（XTLS/Xray-core e61eeae258）；更早的核心启动即退出。
export const MINIMUM_CORE_VERSION = '26.5.3';
const versionNumber = (version?: string) => {
  const parts = /^(\d{1,3})\.(\d{1,2})\.(\d{1,2})$/.exec(version?.trim().replace(/^v/, '') ?? '')?.slice(1).map(Number);
  return parts ? parts[0] * 1e4 + parts[1] * 100 + parts[2] : null;
};
export function coreSupported(version?: string): boolean {
  const n = versionNumber(version);
  return n !== null && n >= versionNumber(MINIMUM_CORE_VERSION)!;
}
export function coreCompatibilityWarning(version?: string): string {
  const v = version?.trim().replace(/^v/, '') ?? '';
  if (v === '26.10.17') return t('Xray v26.10.17 本机测试：Mihomo v1.19.32 的 REALITY、XHTTP-REALITY 即使开启兼容模式仍未通过连接；同版官方 Xray 客户端已通过。其他第三方 REALITY 客户端或版本未验证，官方预检通过不代表连接可用；仅降低最低版本不能保证兼容。', 'Local test with Xray v26.10.17: Mihomo v1.19.32 failed to connect over REALITY and XHTTP-REALITY even with compatibility mode; the official Xray client of the same version passed. Other third-party REALITY clients or versions are unverified; passing the official preflight does not mean connections work, and lowering the minimum version alone does not guarantee compatibility.');
  const n = versionNumber(v);
  if (n === null) return '';
  if (!coreSupported(v)) return t(`Xray v${v} 不支持 o-ui 使用的本机统计套接字（v${MINIMUM_CORE_VERSION} 起才支持），启动即退出，不能选择。`, `Xray v${v} does not support the local statistics socket o-ui uses (supported from v${MINIMUM_CORE_VERSION}); it exits on start and cannot be selected.`);
  // v26.9.8起REALITY服务端要求X25519MLKEM768（XTLS/Xray-core#7259），不支持的客户端会被当作外来连接拒绝。
  if (n < 260908) return '';
  return t('Xray v26.9.8 起，REALITY 服务端要求客户端支持 X25519MLKEM768 密钥交换，不支持的客户端会连不上：小火箭需在“设置 → TCP → 指纹”选带 X25519MLKEM768 的指纹（用户反馈的做法）；Clash/Mihomo 需刷新 o-ui 的 Clash 订阅；v2rayN 等 Xray 内核客户端需较新版本并用 chrome、firefox 或 safari 指纹。切换后若常用客户端不通，可切回 v26.7.28。', 'From Xray v26.9.8, REALITY servers require clients to offer the X25519MLKEM768 key exchange; clients without it cannot connect. In Shadowrocket, pick a fingerprint marked X25519MLKEM768 under Settings → TCP → Fingerprint (a user-reported workaround); Clash/Mihomo need to refresh the o-ui Clash subscription; v2rayN and other Xray-based clients need a recent version with the chrome, firefox or safari fingerprint. If your usual clients fail after switching, switch back to v26.7.28.');
}
