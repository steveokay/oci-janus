import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { TanStackRouterVite } from '@tanstack/router-vite-plugin'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    TanStackRouterVite(),
  ],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  server: {
    proxy: {
      // Management service (port 8091) — more specific paths listed first so
      // they take priority over the catch-all /api rule below.
      '/api/v1/stats': { target: 'http://localhost:8091', changeOrigin: true },
      '/api/v1/repositories': { target: 'http://localhost:8091', changeOrigin: true },

      // Auth service (port 8080) — /api/v1/login, /api/v1/apikeys, /api/v1/logout, etc.
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      '/auth': { target: 'http://localhost:8080', changeOrigin: true },
      '/.well-known': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
  build: {
    target: 'es2020',
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-react':  ['react', 'react-dom'],
          'vendor-router': ['@tanstack/react-router'],
          'vendor-query':  ['@tanstack/react-query'],
          'vendor-table':  ['@tanstack/react-table'],
          'vendor-charts': ['recharts'],
        },
      },
    },
  },
})
