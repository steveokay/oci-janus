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
      // Specificity matters — Vite matches the longest prefix.
      // Management service (port 8091) — owns all /api/v1/* application paths
      // EXCEPT the auth surface listed below.
      '/api/v1/admin':        { target: 'http://localhost:8091', changeOrigin: true },
      '/api/v1/stats':        { target: 'http://localhost:8091', changeOrigin: true },
      '/api/v1/repositories': { target: 'http://localhost:8091', changeOrigin: true },
      '/api/v1/orgs':         { target: 'http://localhost:8091', changeOrigin: true },

      // Auth service (port 8080) — login, apikeys, logout, password reset.
      '/api/v1/login':    { target: 'http://localhost:8080', changeOrigin: true },
      '/api/v1/logout':   { target: 'http://localhost:8080', changeOrigin: true },
      '/api/v1/me':       { target: 'http://localhost:8080', changeOrigin: true },
      '/api/v1/apikeys':  { target: 'http://localhost:8080', changeOrigin: true },
      '/api/v1/users':    { target: 'http://localhost:8080', changeOrigin: true },

      // OIDC / discovery surface lives on auth.
      '/.well-known':     { target: 'http://localhost:8080', changeOrigin: true },
      '/auth':            { target: 'http://localhost:8080', changeOrigin: true },
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
