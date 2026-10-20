import QRCode from 'qrcode';

// 本地编码的实际输出也参与解码测试，不调用外部二维码服务。
export function connectionQR(link: string): string {
  const qr = QRCode.create(link, { errorCorrectionLevel: 'M' });
  const size = qr.modules.size, scale = size + 8;
  let path = '';
  for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) {
    if (qr.modules.get(y, x)) path += `M${x + 4},${y + 4}h1v1h-1z`;
  }
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${scale * 6}" height="${scale * 6}" viewBox="0 0 ${scale} ${scale}"><rect width="100%" height="100%" fill="white"/><path d="${path}" fill="black"/></svg>`;
  return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
}
