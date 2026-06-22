import { render, screen, act } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import {
  createRouter,
  createMemoryHistory,
  RouterProvider,
  createRoute,
  createRootRoute,
} from "@tanstack/react-router";
import { AccessSubNav } from "../AccessSubNav";
import type { JanusJwtClaims } from "@/lib/auth/jwt";

// ---------------------------------------------------------------------------
// Mock the auth store so we can control what `claims` the component sees
// without needing a real JWT. `useAuthStore` is a Zustand selector hook;
// we replace the entire module with a factory that returns whatever `mockClaims`
// is set to at the time the hook is called.
// ---------------------------------------------------------------------------
let mockClaims: JanusJwtClaims | null = null;

vi.mock("@/lib/auth/store", () => ({
  // `useAuthStore` is called with a selector: `(s) => s.claims`
  // We satisfy the selector pattern by returning mockClaims directly.
  useAuthStore: (selector: (s: { claims: JanusJwtClaims | null }) => unknown) =>
    selector({ claims: mockClaims }),
  authStore: {
    getToken: () => null,
    getClaims: () => mockClaims,
    setToken: vi.fn(),
    clear: vi.fn(),
  },
}));

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// Non-admin user — roles array does not include "admin".
const nonAdminClaims: JanusJwtClaims = {
  sub: "user-non-admin",
  tenant_id: "tenant-1",
  username: "regular-user",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-non-admin",
  roles: ["reader"],
};

// Platform-admin user — `isPlatformAdmin` checks `roles.includes("admin")`.
const adminClaims: JanusJwtClaims = {
  sub: "user-admin",
  tenant_id: "tenant-1",
  username: "admin-user",
  exp: Math.floor(Date.now() / 1000) + 3600,
  iat: Math.floor(Date.now() / 1000),
  jti: "jti-admin",
  roles: ["admin"],
};

// ---------------------------------------------------------------------------
// Router wrapper — AccessSubNav renders TanStack Router <Link> elements, so
// it must be mounted inside a RouterProvider. We build a minimal in-memory
// router with just a root route to satisfy the context requirement without
// needing any real route tree.
// ---------------------------------------------------------------------------
async function renderSubNav() {
  // Build a minimal route tree: root renders AccessSubNav directly.
  const rootRoute = createRootRoute({
    component: AccessSubNav,
  });

  // Add a stub index child route so the router has a matched route to render.
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => null,
  });

  const routeTree = rootRoute.addChildren([indexRoute]);

  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  // `router.load()` resolves the initial location, then wrapping the render in
  // `act` flushes all React state updates from the router's navigation effect.
  await router.load();

  await act(async () => {
    render(<RouterProvider router={router} />);
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("AccessSubNav", () => {
  beforeEach(() => {
    // Reset claims before each test so state from a previous test doesn't leak.
    mockClaims = null;
  });

  test("hides Workspace and Preview sections for non-admin users", async () => {
    mockClaims = nonAdminClaims;
    await renderSubNav();

    // The "Yours" section is always visible — verify we rendered something.
    expect(screen.getByText("Yours")).toBeInTheDocument();

    // Admin-only sections must be absent from the DOM entirely.
    expect(screen.queryByText("Workspace")).not.toBeInTheDocument();
    expect(screen.queryByText("Preview")).not.toBeInTheDocument();
  });

  test("shows Yours, Workspace, and Preview sections for admin users", async () => {
    mockClaims = adminClaims;
    await renderSubNav();

    // All three section headings must be present for platform-admins.
    expect(screen.getByText("Yours")).toBeInTheDocument();
    expect(screen.getByText("Workspace")).toBeInTheDocument();

    // The "Preview" section heading is a plain div; the badge pills that also
    // say "Preview" are <span> elements inside <a> tags. We confirm the section
    // heading exists by filtering to div elements with no child elements.
    const previewMatches = screen.getAllByText("Preview");
    const previewSectionHeading = previewMatches.find(
      (el) => el.tagName === "DIV" && el.children.length === 0,
    );
    expect(previewSectionHeading).toBeInTheDocument();
  });

  test("shows Personal keys link for all users", async () => {
    mockClaims = nonAdminClaims;
    await renderSubNav();

    expect(screen.getByText("Personal keys")).toBeInTheDocument();
  });

  test("Service accounts and Activity links are absent for non-admins", async () => {
    // Non-admin: admin-only items must not appear.
    mockClaims = nonAdminClaims;
    await renderSubNav();
    expect(screen.queryByText("Service accounts")).not.toBeInTheDocument();
    expect(screen.queryByText("Activity")).not.toBeInTheDocument();
  });

  test("shows all preview links for admins", async () => {
    mockClaims = adminClaims;
    await renderSubNav();

    expect(screen.getByText("Federated trust")).toBeInTheDocument();
    expect(screen.getByText("Credential helpers")).toBeInTheDocument();
    expect(screen.getByText("Token policies")).toBeInTheDocument();
    expect(screen.getByText("Access review")).toBeInTheDocument();
  });
});
