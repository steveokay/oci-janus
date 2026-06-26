// Beacon — caller abilities (REDESIGN-001 Phase 4.4).
//
// Backend surface: GET /api/v1/me/abilities — auth-gated.
// Returns the caller's role assignments + is_global_admin flag so the FE
// applies the same containment rule the BFF enforces. Closes Review §C2 +
// §D3 FE/BE RBAC drift — previously the FE read claims.roles which is a
// flat deduped list that lost scope information.

import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// AbilityAssignment is a single scoped role grant for the caller.
export interface AbilityAssignment {
  // role is one of "reader" | "writer" | "admin" | "owner".
  role: string;
  // scope_type is one of "org" | "repo" | "tenant".
  scope_type: string;
  // scope_value is the concrete scope identifier:
  //   org    → "myorg"
  //   repo   → "myorg/myimage"
  //   tenant → "<tenant_uuid>"
  scope_value: string;
}

// AbilitiesResponse is the JSON body of GET /api/v1/me/abilities.
export interface AbilitiesResponse {
  // is_global_admin reflects users.is_global_admin (Phase 5.1 typed column).
  // True bypasses all scope checks — the caller has every ability.
  is_global_admin: boolean;
  // role_assignments is the ordered list of scoped grants for the caller.
  // Never null — always an array (possibly empty).
  role_assignments: AbilityAssignment[];
}

// AbilityScope describes the target scope for an ability check.
export type AbilityScope =
  | { type: "tenant"; value: string }
  | { type: "org"; value: string }
  | { type: "repo"; value: string };

// ROLE_HIERARCHY defines the ordering of roles from least to most privileged.
// Index 0 = least privileged ("reader").
//
// MUST match the BFF's roleHierarchy in
// services/management/internal/handler/rbac.go:22 — if one changes, the
// other must change too (REDESIGN-001 Phase 4.4 containment-mirror rule).
const ROLE_HIERARCHY = ["reader", "writer", "admin", "owner"] as const;

// roleIndex returns the numeric rank of a role. Unknown roles return -1
// (below all defined roles), which causes hasAbility to deny.
function roleIndex(role: string): number {
  return ROLE_HIERARCHY.indexOf(role as (typeof ROLE_HIERARCHY)[number]);
}

// fetchAbilities fetches the caller's ability set from the BFF.
// Uses /api/v1/me/abilities — auth-gated; called by useAbilities().
async function fetchAbilities(): Promise<AbilitiesResponse> {
  const response = await apiClient.get<AbilitiesResponse>("/me/abilities");
  return response.data;
}

// useAbilities returns the raw abilities query result (is_global_admin +
// role_assignments). Most callers should prefer useAbility() which wraps the
// containment evaluation. Long staleTime: abilities only change on grant/revoke,
// which the FE must explicitly invalidate after a successful mutation by calling
// `queryClient.invalidateQueries({ queryKey: abilitiesKeys.all })`.
export function useAbilities() {
  return useQuery({
    queryKey: abilitiesKeys.all,
    queryFn: fetchAbilities,
    // 5 min — abilities rarely change mid-session. On grant/revoke mutations
    // the FE should call queryClient.invalidateQueries({ queryKey: abilitiesKeys.all }).
    staleTime: 5 * 60_000,
    // Keep in cache for 30 min after the component unmounts so navigating away
    // and back does not refetch immediately.
    gcTime: 30 * 60_000,
  });
}

// abilitiesKeys holds the React Query cache key for ability queries.
// Export so mutation handlers can call invalidateQueries after grant/revoke.
export const abilitiesKeys = {
  all: ["abilities"] as const,
};

// hasAbility evaluates the containment rule client-side. Pure function —
// does not call hooks. Useful for evaluating abilities you've already fetched
// (e.g. inside a useMemo or an event handler).
//
// Containment rule (MUST mirror BFF hasScopedRole in rbac.go:45 and
// effectiveTenantAdmin in rbac.go:81):
//   - is_global_admin = true → all abilities qualify regardless of scope.
//   - tenant-scoped grant covers every org/repo within that tenant.
//   - org-scoped grant covers all repos within that org
//     (scope.value starts with grantValue + "/").
//   - role rank must be >= minimum (owner > admin > writer > reader).
//   - Returns false on undefined input (loading or unauthenticated state).
export function hasAbility(
  abilities: AbilitiesResponse | undefined,
  minRole: string,
  scope: AbilityScope,
): boolean {
  if (!abilities) return false;
  // Global admin bypasses all scope checks.
  if (abilities.is_global_admin) return true;

  const minIdx = roleIndex(minRole);
  // Unknown minRole (mistyped string) → deny.
  if (minIdx < 0) return false;

  for (const a of abilities.role_assignments) {
    // Skip grants below the minimum rank.
    if (roleIndex(a.role) < minIdx) continue;

    // Exact match: same scope type and value.
    if (a.scope_type === scope.type && a.scope_value === scope.value) {
      return true;
    }

    // Tenant-scoped grant covers every org and repo in that tenant.
    // (Mirrors effectiveTenantAdmin + the special-case in effectiveGlobalAdmin
    // for single-mode deployments; here we generalise: any tenant grant
    // covers any sub-scope because the abilities endpoint is tenant-scoped.)
    if (a.scope_type === "tenant") {
      return true;
    }

    // Org-scoped grant covers all repos within that org.
    // Mirrors hasScopedRole in rbac.go:58-60.
    if (scope.type === "repo" && a.scope_type === "org") {
      if (scope.value.startsWith(a.scope_value + "/")) {
        return true;
      }
    }
  }
  return false;
}

// useAbility returns a boolean for "does the caller hold ≥minRole on scope".
// Reactively re-renders when the abilities query resolves. Returns false while
// loading — callers must not gate destructive actions until isLoading is false
// (use useAbilities() directly to inspect loading state in that case).
//
// Example:
//   const canDelete = useAbility("admin", { type: "org", value: "myorg" });
export function useAbility(minRole: string, scope: AbilityScope): boolean {
  const { data } = useAbilities();
  return hasAbility(data, minRole, scope);
}

// useIsGlobalAdmin is a convenience hook for /admin/* route guards.
// Returns false while loading. Equivalent to
// `useAbilities().data?.is_global_admin ?? false`.
export function useIsGlobalAdmin(): boolean {
  const { data } = useAbilities();
  return data?.is_global_admin ?? false;
}
