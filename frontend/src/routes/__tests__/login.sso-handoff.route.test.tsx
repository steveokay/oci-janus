import { describe, test, expect, beforeEach, vi } from "vitest";
import { createRouter, createMemoryHistory } from "@tanstack/react-router";
import { routeTree } from "@/routeTree.gen";

// FUT-084 — /login SSO callback handoff (beforeLoad).
//
// The auth service 302s back to /login?sso_token=<jwt> after a successful
// OAuth/SAML dance. login's beforeLoad must consume the token into the auth
// store and redirect to the stashed return path; a malformed token must be
// dropped from the URL so the user lands on the bare password form.
//
// We mock the auth store with a stateful double whose setToken mimics the real
// one (only a well-formed 3-segment JWT sticks; anything else clears to null),
// so we can drive both branches. Same store-mock approach as the sibling
// helm.redirect.route.test.tsx.

const store = vi.hoisted(() => {
  let token: string | null = null;
  return {
    getToken: () => token,
    setToken: (t: string | null) => {
      token = t && /^[^.]+\.[^.]+\.[^.]+$/.test(t) ? t : null;
    },
    reset: () => {
      token = null;
    },
  };
});

vi.mock("@/lib/auth/store", () => ({
  authStore: {
    getToken: store.getToken,
    getClaims: () => null,
    setToken: store.setToken,
    clear: () => store.setToken(null),
  },
  useAuthStore: (
    selector: (s: { claims: unknown; token: string | null }) => unknown,
  ) => selector({ claims: null, token: store.getToken() }),
}));

beforeEach(() => {
  store.reset();
  sessionStorage.clear();
});

describe("/login SSO token handoff", () => {
  test("consumes a valid sso_token and redirects to the stashed return path", async () => {
    sessionStorage.setItem("sso_return_to", "/repositories");
    const history = createMemoryHistory({
      initialEntries: ["/login?sso_token=head.payload.sig"],
    });
    const router = createRouter({ routeTree, history });

    await router.load();

    expect(store.getToken()).toBe("head.payload.sig");
    expect(router.state.location.pathname).toBe("/repositories");
    // The one-shot return target is cleared after consumption.
    expect(sessionStorage.getItem("sso_return_to")).toBeNull();
  });

  test("drops a malformed sso_token and lands on the bare login form", async () => {
    const history = createMemoryHistory({
      initialEntries: ["/login?sso_token=not-a-jwt"],
    });
    const router = createRouter({ routeTree, history });

    await router.load();

    expect(store.getToken()).toBeNull();
    expect(router.state.location.pathname).toBe("/login");
    expect(router.state.location.search).not.toHaveProperty("sso_token");
  });
});
