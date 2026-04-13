import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

// The SPA ships under /v4/ during the v3→v4 co-exist period. Once parity
// is reached the base can flip back to '/' and /api/* stays unchanged.
export default defineConfig({
  base: '/v4/',
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
