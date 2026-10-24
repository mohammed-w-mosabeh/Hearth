import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Dev only: proxy API calls to a local hearthd. The Origin header is
// rewritten to hearthd's own origin so its same-origin check passes; the dev
// server itself stays on 127.0.0.1.
const hearthd = `http://127.0.0.1:${process.env.HEARTH_PORT ?? '7380'}`;

export default defineConfig({
  plugins: [svelte()],
  server: {
    host: '127.0.0.1',
    strictPort: true,
    proxy: {
      '/api': {
        target: hearthd,
        changeOrigin: true,
        ws: true,
        configure: (proxy) => {
          proxy.on('proxyReq', (req) => req.setHeader('origin', hearthd));
          proxy.on('proxyReqWs', (req) => req.setHeader('origin', hearthd));
        },
      },
      '/auth': { target: hearthd, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // No inline scripts: the CSP is script-src 'self' (SECURITY S4).
    modulePreload: { polyfill: false },
    assetsInlineLimit: 0,
    sourcemap: false,
  },
  test: {
    include: ['src/**/*.test.ts'],
  },
});
