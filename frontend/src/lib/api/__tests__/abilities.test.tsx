// REDESIGN-001 Phase 4.4 — abilities hook unit tests.
//
// Covers:
//   1. hasAbility returns true for exact scope match.
//   2. hasAbility returns true when is_global_admin=true regardless of scope.
//   3. hasAbility tenant grant covers any sub-scope (org and repo).
//   4. hasAbility org grant covers repo within that org but NOT sibling repos.
//   5. hasAbility role hierarchy: admin satisfies >= writer requirement.
//   6. hasAbility returns false on undefined input (loading state).
//   7. useAbility returns reactive value after the abilities query resolves.

import * as React from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import {
  hasAbility,
  type AbilitiesResponse,
  type AbilityScope,
} from "../abilities";

// ---------------------------------------------------------------------------
// Mock the API client so network calls never leave the process.
// ---------------------------------------------------------------------------

const getMock = vi.fn();
vi.mock("../client", () => ({
  apiClient: {
    get: (...args: unknown[]) => getMock(...args),
  },
}));

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// wrapper creates a fresh QueryClient for each test so caches don't bleed.
function wrapper(): React.FC<{ children: React.ReactNode }> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  function Wrap({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client }, children);
  }
  return Wrap;
}

// buildAbilities is a convenience factory for AbilitiesResponse objects.
function buildAbilities(
  overrides?: Partial<AbilitiesResponse>,
): AbilitiesResponse {
  return {
    is_global_admin: false,
    role_assignments: [],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// hasAbility — pure-function tests (no hooks, no React)
// ---------------------------------------------------------------------------

describe("hasAbility — pure containment rule (REDESIGN-001 Phase 4.4)", () => {
  // Test 1 — exact scope match.
  test("returns true for exact scope match", () => {
    const abilities = buildAbilities({
      role_assignments: [
        { role: "admin", scope_type: "org", scope_value: "myorg" },
      ],
    });
    const scope: AbilityScope = { type: "org", value: "myorg" };
    expect(hasAbility(abilities, "admin", scope)).toBe(true);
  });

  // Test 2 — is_global_admin bypasses all scope checks.
  test("returns true for is_global_admin regardless of scope", () => {
    const abilities = buildAbilities({
      is_global_admin: true,
      role_assignments: [], // no scoped grants needed
    });
    // Ask for owner on an org the caller has no explicit grant for.
    const scope: AbilityScope = { type: "org", value: "any-org" };
    expect(hasAbility(abilities, "owner", scope)).toBe(true);

    // Even for a repo scope never granted.
    const repoScope: AbilityScope = {
      type: "repo",
      value: "any-org/any-repo",
    };
    expect(hasAbility(abilities, "admin", repoScope)).toBe(true);
  });

  // Test 3 — tenant-scoped grant covers any sub-scope.
  test("tenant grant covers any org/repo sub-scope within the tenant", () => {
    const tenantID = "tenant-uuid-123";
    const abilities = buildAbilities({
      role_assignments: [
        { role: "admin", scope_type: "tenant", scope_value: tenantID },
      ],
    });

    // Should satisfy an org-level check.
    expect(
      hasAbility(abilities, "admin", { type: "org", value: "some-org" }),
    ).toBe(true);

    // Should satisfy a repo-level check.
    expect(
      hasAbility(abilities, "reader", {
        type: "repo",
        value: "some-org/some-repo",
      }),
    ).toBe(true);

    // Should satisfy the tenant itself (exact match path fires first, but
    // the tenant-grant shortcut would also fire).
    expect(
      hasAbility(abilities, "admin", { type: "tenant", value: tenantID }),
    ).toBe(true);
  });

  // Test 4a — org grant covers repos within that org.
  test("org grant covers repo within that org", () => {
    const abilities = buildAbilities({
      role_assignments: [
        { role: "admin", scope_type: "org", scope_value: "myorg" },
      ],
    });
    expect(
      hasAbility(abilities, "admin", { type: "repo", value: "myorg/myimage" }),
    ).toBe(true);
    expect(
      hasAbility(abilities, "writer", {
        type: "repo",
        value: "myorg/another-image",
      }),
    ).toBe(true);
  });

  // Test 4b — org grant does NOT cover sibling orgs or repos in other orgs.
  test("org grant does NOT cover sibling repos or other orgs", () => {
    const abilities = buildAbilities({
      role_assignments: [
        { role: "admin", scope_type: "org", scope_value: "org-a" },
      ],
    });
    // Sibling org — must be denied (PENTEST-002 mirror).
    expect(
      hasAbility(abilities, "admin", { type: "org", value: "org-b" }),
    ).toBe(false);

    // Repo in sibling org — must be denied.
    expect(
      hasAbility(abilities, "reader", {
        type: "repo",
        value: "org-b/anyrepo",
      }),
    ).toBe(false);

    // Repo prefix collision: "myorg" should not cover "myorg-evil/repo".
    const abilities2 = buildAbilities({
      role_assignments: [
        { role: "admin", scope_type: "org", scope_value: "myorg" },
      ],
    });
    expect(
      hasAbility(abilities2, "admin", {
        type: "repo",
        value: "myorg-evil/repo",
      }),
    ).toBe(false);
  });

  // Test 5 — role hierarchy: admin satisfies >= writer requirement.
  test("role hierarchy: admin grant satisfies >= writer requirement", () => {
    const abilities = buildAbilities({
      role_assignments: [
        { role: "admin", scope_type: "org", scope_value: "myorg" },
      ],
    });
    const scope: AbilityScope = { type: "org", value: "myorg" };

    // admin satisfies writer, reader.
    expect(hasAbility(abilities, "writer", scope)).toBe(true);
    expect(hasAbility(abilities, "reader", scope)).toBe(true);
    // admin satisfies admin.
    expect(hasAbility(abilities, "admin", scope)).toBe(true);
    // admin does NOT satisfy owner.
    expect(hasAbility(abilities, "owner", scope)).toBe(false);

    // owner satisfies all.
    const ownerAbilities = buildAbilities({
      role_assignments: [
        { role: "owner", scope_type: "org", scope_value: "myorg" },
      ],
    });
    expect(hasAbility(ownerAbilities, "owner", scope)).toBe(true);
    expect(hasAbility(ownerAbilities, "admin", scope)).toBe(true);
    expect(hasAbility(ownerAbilities, "writer", scope)).toBe(true);
    expect(hasAbility(ownerAbilities, "reader", scope)).toBe(true);
  });

  // Test 6 — returns false on undefined input (loading / unauthenticated).
  test("returns false on undefined input", () => {
    const scope: AbilityScope = { type: "org", value: "myorg" };
    expect(hasAbility(undefined, "reader", scope)).toBe(false);
    expect(hasAbility(undefined, "admin", scope)).toBe(false);
  });

  // Bonus: unknown minRole string returns false (not a throw).
  test("returns false for unrecognised minRole string", () => {
    const abilities = buildAbilities({
      role_assignments: [
        { role: "owner", scope_type: "org", scope_value: "myorg" },
      ],
    });
    expect(
      hasAbility(abilities, "superadmin", { type: "org", value: "myorg" }),
    ).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// useAbility — React hook test (requires QueryClient wrapper).
// ---------------------------------------------------------------------------

describe("useAbility — reactive hook (REDESIGN-001 Phase 4.4)", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  // Test 7 — useAbility returns the reactive value after the query resolves.
  test("returns reactive value after abilities query resolves", async () => {
    // Lazily import so the vi.mock() for ../client is active first.
    const { useAbility } = await import("../abilities");

    // Stub the GET /me/abilities response.
    getMock.mockResolvedValueOnce({
      data: {
        is_global_admin: false,
        role_assignments: [
          { role: "admin", scope_type: "org", scope_value: "myorg" },
        ],
      } satisfies AbilitiesResponse,
    });

    const { result } = renderHook(
      () =>
        useAbility("admin", { type: "org", value: "myorg" } as AbilityScope),
      { wrapper: wrapper() },
    );

    // While loading, the hook returns false (fail-closed).
    expect(result.current).toBe(false);

    // After the query resolves, it returns true because the org-admin grant
    // satisfies the "admin" requirement on "myorg".
    await waitFor(() => expect(result.current).toBe(true));
  });

  // Verify useAbility returns false when the scope doesn't match.
  test("returns false when scope does not match any assignment", async () => {
    const { useAbility } = await import("../abilities");

    getMock.mockResolvedValueOnce({
      data: {
        is_global_admin: false,
        role_assignments: [
          { role: "reader", scope_type: "org", scope_value: "other-org" },
        ],
      } satisfies AbilitiesResponse,
    });

    const { result } = renderHook(
      () =>
        useAbility("admin", { type: "org", value: "myorg" } as AbilityScope),
      { wrapper: wrapper() },
    );

    await waitFor(() => expect(result.current).toBe(false));
  });

  // Verify useIsGlobalAdmin returns true when is_global_admin=true.
  test("useIsGlobalAdmin returns true when is_global_admin=true", async () => {
    const { useIsGlobalAdmin } = await import("../abilities");

    getMock.mockResolvedValueOnce({
      data: {
        is_global_admin: true,
        role_assignments: [],
      } satisfies AbilitiesResponse,
    });

    const { result } = renderHook(() => useIsGlobalAdmin(), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current).toBe(true));
  });
});
