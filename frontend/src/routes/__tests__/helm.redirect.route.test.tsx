import { describe, test, expect, vi } from "vitest";
import { createRouter, createMemoryHistory } from "@tanstack/react-router";
import { routeTree } from "@/routeTree.gen";

// Route-guard test for the /helm retirement (unified-artifact-catalog).
// /helm now throws a beforeLoad redirect to /repositories (the environments
// overview) so old links/bookmarks don't 404. The Helm catalog page is gone.
//
// /helm lives under the `_authenticated` layout, whose own beforeLoad
// redirects to /login unless `authStore.getToken()` is truthy. We mock the
// auth store to return a token (same pattern as the sibling redirect tests:
// getting-started.redirect.route.test.tsx + repositories.environments.route.test.tsx)
// so the load reaches the /helm route and hits its own redirect rather than
// bouncing to /login.
vi.mock("@/lib/auth/store", () => ({
  useAuthStore: (
    selector: (s: { claims: unknown; token: string | null }) => unknown,
  ) =>
    selector({
      claims: {
        username: "test-user",
        sub: "u-1",
        tenant_id: "t-1",
        exp: 0,
        iat: 0,
        jti: "j",
      },
      token: "any-token-here",
    }),
  authStore: {
    getToken: () => "any-token-here",
    getClaims: () => ({ sub: "u-1" }),
    setToken: vi.fn(),
    clear: vi.fn(),
  },
}));

describe("/helm retirement", () => {
  test("/helm redirects to /repositories", async () => {
    const history = createMemoryHistory({ initialEntries: ["/helm"] });
    const router = createRouter({ routeTree, history });
    // The redirect fires in beforeLoad during load(), so no render needed.
    await router.load();
    expect(router.state.location.pathname).toBe("/repositories");
  });
});
