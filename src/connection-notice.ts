import { t } from './i18n.ts';
// 证书是否受服务器系统根信任只影响提示，导出链接始终保持证书校验。
export function certificateNotice(trusted: boolean | undefined): string {
  if (trusted) return t('<p class="form-hint">证书受公共CA信任且与服务域名匹配，客户端保持默认证书验证即可。链接不包含证书，也不会要求跳过验证。</p>', '<p class="form-hint">The certificate is trusted by a public CA and matches the server name; clients can keep default verification. Links never contain the certificate or ask to skip verification.</p>');
  return t('<p class="connection-warning">此节点证书不受公共CA信任（如自签证书、私有CA或仅IP证书）。手机等客户端默认会拒绝连接：建议改用与域名匹配的受信任证书后重新应用；或在设备上导入并信任该证书。仅临时测试时才可在客户端为此节点开启“允许不安全/跳过证书验证”，这会失去防中间人保护。</p>', '<p class="connection-warning">This node\'s certificate is not trusted by a public CA (self-signed, private CA or IP-only). Phones and other clients reject it by default: use a trusted certificate matching a domain and apply again, or import and trust the certificate on the device. Only for temporary tests enable "Allow insecure / skip certificate verification" for this node, which removes protection against man-in-the-middle attacks.</p>');
}
