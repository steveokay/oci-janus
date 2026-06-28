// Stand-alone route-tree generator.
//
// WHY THIS EXISTS:
// TanStack Router's `routeTree.gen.ts` is produced by the `@tanstack/router-plugin/vite`
// plugin during `vite dev` / `vite build`. The file is .gitignored — see frontend/.gitignore.
// CI runs lint / typecheck / test without running Vite first, so the generated file is
// absent and every `import { routeTree } from "@/routeTree.gen"` blows up.
//
// This script calls the underlying `@tanstack/router-generator` `Generator.run()` API
// directly (the same API the Vite plugin wraps) so we can produce the file from a plain
// `node` invocation. The npm script `routes:generate` wraps this; CI invokes it before
// every gate (lint / typecheck / test / build).
//
// Mirror the config block in `vite.config.ts` so the inputs stay in lock-step. If you
// change one, change the other.
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Generator, getConfig } from "@tanstack/router-generator";

const root = path.resolve(fileURLToPath(import.meta.url), "../..");

// Inline config mirrors the TanStackRouterVite({ ... }) call in vite.config.ts.
// router-generator's getConfig fills in every other field with sensible defaults.
const config = getConfig(
  {
    routesDirectory: "./src/routes",
    generatedRouteTree: "./src/routeTree.gen.ts",
    autoCodeSplitting: true,
    // Skip the __tests__ directory and any `*.test.tsx` files sitting next
    // to a route. router-generator applies this regex against each entry's
    // BASENAME (not its full path), so we have to match `__tests__` as a
    // directory name AND `.test.` as a filename infix, not `/__tests__/`.
    routeFileIgnorePattern: "__tests__|\\.test\\.",
  },
  root,
);

const generator = new Generator({ config, root });

try {
  await generator.run();
  console.log("✅ routeTree.gen.ts generated at", config.generatedRouteTree);
} catch (err) {
  console.error("❌ route generation failed");
  console.error(err);
  process.exit(1);
}
