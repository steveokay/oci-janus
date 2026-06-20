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
      "/api/v1/apikeys":        { target: "http://localhost:8080", changeOrigin: true },
      "/api/v1/users":          { target: "http://localhost:8080", changeOrigin: true },
      "/api/v1":                { target: "http://localhost:8091", changeOrigin: true },
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
