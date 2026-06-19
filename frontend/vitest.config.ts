import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config";

// Beacon — vitest config. Lives in its own file because vitest pulls its
// own pinned vite version and the two type identities don't unify cleanly
// when test config is embedded inside vite.config.ts.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test/setup.ts"],
    },
  }),
);
