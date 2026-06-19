import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { TanStackRouterVite } from "@tanstack/router-plugin/vite";
import path from "node:path";

// Beacon — Vite config.
// Dev proxy points to the docker-compose port mappings:
//   management API (BFF) → host port 8091 → container :8085
//   auth API             → host port 8080
// Keep these two paths separate so production nginx can route them the same way.
export default defineConfig({
  plugins: [
    TanStackRouterVite({
      routesDirectory: "./src/routes",
      generatedRouteTree: "./src/routeTree.gen.ts",
      autoCodeSplitting: true,
    }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      // Management BFF carries every /api/v1/* route the UI consumes.
      "/api": {
        target: "http://localhost:8091",
        changeOrigin: true,
      },
      // Auth service exposes its own /api/v1/login etc.; the gateway co-locates them in prod.
      // We route /auth/* directly to the auth service for Docker token + JWKS surfaces.
      "/auth": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
    target: "es2022",
  },
});
