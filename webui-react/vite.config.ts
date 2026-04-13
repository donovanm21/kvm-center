import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

// v4.0.0 cutover: SPA is now the primary UI served at '/'. The legacy
// v3 HTML still lives at /legacy/ for a grace window so operators with
// muscle memory can fall back if they hit a snag. /api/* is unchanged.
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
