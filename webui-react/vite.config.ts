import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

// The React SPA is the only shipped UI since v5.18.9. The server
// 308s /legacy and /v4 bookmarks back to '/'. /api/* is proxied to
// the Go backend below so cookies work during local dev.
export default defineConfig({
  base: '/',
  plugins: [react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // Vite dev server proxies API calls to the Go backend so cookies /
      // sessions work without CORS gymnastics during local dev.
      '/api': 'http://localhost:8090',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: true,
  },
});
