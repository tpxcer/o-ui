import { defineConfig } from 'vite';

export default defineConfig({
  build: { license: { fileName: 'THIRD-PARTY-LICENSES.json' } },
  server: {
    host: '127.0.0.1', port: 5178, strictPort: true,
    proxy: {
      '/api': { target: 'http://127.0.0.1:5180', changeOrigin: false },
      '/sub': {
        target: 'http://127.0.0.1:5180', changeOrigin: false,
        configure(proxy) {
          // Vite随后注册的默认错误日志会读取原请求地址。
          proxy.on('error', (_error, request) => { request.url = '/sub/[redacted]'; });
        },
      },
    },
  },
});
