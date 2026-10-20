import { t } from './i18n.ts';
export function coreCompatibilityWarning(version?: string): string {
  if (version?.trim().replace(/^v/, '') !== '26.10.17') return '';
  return t('Xray v26.10.17 本机测试：Mihomo v1.19.32 的 REALITY、XHTTP-REALITY 即使开启兼容模式仍未通过连接；同版官方 Xray 客户端已通过。其他第三方 REALITY 客户端或版本未验证，官方预检通过不代表连接可用；仅降低最低版本不能保证兼容。', 'Local test with Xray v26.10.17: Mihomo v1.19.32 failed to connect over REALITY and XHTTP-REALITY even with compatibility mode; the official Xray client of the same version passed. Other third-party REALITY clients or versions are unverified; passing the official preflight does not mean connections work, and lowering the minimum version alone does not guarantee compatibility.');
}
