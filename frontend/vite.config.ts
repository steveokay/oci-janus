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
      // Keep test files next to routes from being treated as routes. Mirrors
      // the same setting in scripts/generate-routes.mjs — if you change one,
      // change the other. Pattern is matched against the basename, not the
      // full path, so we have to match the dir name AND `.test.` infix.
      routeFileIgnorePattern: "__tests__|\\.test\\.",
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
    // Allow tunnelling the dev server through localhost.run / ngrok-style
    // hosts. Vite 5+ enforces a Host header allowlist by default to block
    // DNS-rebinding attacks; for dev we widen it to any *.ngrok-free.dev
    // / *.ngrok.app / *.trycloudflare.com subdomain and localhost.
    allowedHosts: [
      "localhost",
      ".ngrok-free.dev",
      ".ngrok-free.app",
      ".ngrok.app",
      ".ngrok.io",
      ".trycloudflare.com",
    ],
    // In production the gateway routes all `/api/v1/*` to the right service.
    // In dev we don't have the gateway in front, so the proxy has to split
    // the namespace itself: auth-owned subroutes go to :8080, everything else
    // goes to the management BFF on :8091. Order matters — Vite picks the
    // first matching key, so the auth-specific entries must come first.
    proxy: {
      "/api/v1/login":          { target: "http://localhost:8080", changeOrigin: true },
      "/api/v1/logout":         { target: "http://localhost:8080", changeOrigin: true },
      "/api/v1/token":          { target: "http://localhost:8080", changeOrigin: true },
      "/api/v1/apikeys":         { target: "http://localhost:8080", changeOrigin: true },
      // FUT-019 Phase 2 — /api/v1/users/me/notification-preferences
      // lives on the BFF, not on auth. Has to land BEFORE the
      // `/api/v1/users` catch-all below because Vite picks first
      // matching key. Without this entry the call goes to auth (8080)
      // which returns 404 and the FE renders ErrorState.
      "/api/v1/users/me/notification-preferences": { target: "http://localhost:8091", changeOrigin: true },
      "/api/v1/users":           { target: "http://localhost:8080", changeOrigin: true },
      "/api/v1/service-accounts": { target: "http://localhost:8080", changeOrigin: true },
      // FUT-001 — the OIDC trust admin routes live on services/management
      // (BFF), not services/auth. Must come BEFORE the /api/v1/access
      // catchall below (Vite matches the first key) or the FE hits auth
      // and gets a silent 404.
      "/api/v1/access/oidc-trust": { target: "http://localhost:8091", changeOrigin: true },
      // FUT-003 — the token-policy admin route lives on the management
      // BFF too. Same first-match rule: must come BEFORE the generic
      // /api/v1/access catchall or the FE hits auth (:8080) and 404s.
      "/api/v1/access/token-policy": { target: "http://localhost:8091", changeOrigin: true },
      // FUT-004 — the access-review routes (stale + snooze) live on the
      // management BFF. Same first-match rule: must come BEFORE the
      // generic /api/v1/access catchall or the FE hits auth (:8080) and
      // 404s.
      "/api/v1/access/review":  { target: "http://localhost:8091", changeOrigin: true },
      "/api/v1/access":          { target: "http://localhost:8080", changeOrigin: true },
      "/api/v1":                 { target: "http://localhost:8091", changeOrigin: true },
      "/healthz":               { target: "http://localhost:8091", changeOrigin: true },
      "/auth":                  { target: "http://localhost:8080", changeOrigin: true },
      "/.well-known":           { target: "http://localhost:8080", changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
    target: "es2022",
  },
});
