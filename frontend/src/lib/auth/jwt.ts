import { jwtDecode } from "jwt-decode";

// Shape of the JWT the backend issues for the dashboard session.
// `roles` was added in Sprint 6 of the backend rebuild (services/auth) — the
// claim is a flat list of role names like ["admin", "writer"]; we use it
// purely for UI gating (server is the source of truth).
export interface JanusJwtClaims {
  sub: string;        // user_id
  tenant_id: string;
  username?: string;
  exp: number;        // unix seconds
  iat: number;
  jti: string;
  roles?: string[];
  // is_global_admin mirrors users.is_global_admin (REDESIGN-001 Phase 5.1).
  // True grants platform/workspace-admin abilities regardless of role
  // assignments. Optional so legacy JWTs from before Phase 5.1 still parse
  // (the field is omitempty on the backend; absent → false).
  is_global_admin?: boolean;
  access?: Array<{
    type: string;     // "repository"
    name: string;     // "org/repo"
    actions: string[];
  }>;
}

// Decode the JWT body. Throws if the token is malformed — callers must guard.
export function decodeJanusJwt(token: string): JanusJwtClaims {
  return jwtDecode<JanusJwtClaims>(token);
}

// Returns true when the token is within `withinSeconds` of expiring.
// Used by the silent-refresh scheduler in the auth store.
export function isExpiringSoon(claims: JanusJwtClaims, withinSeconds = 60): boolean {
  const now = Math.floor(Date.now() / 1000);
  return claims.exp - now <= withinSeconds;
}

/**
 * @deprecated Use `useIsGlobalAdmin()` from `"@/lib/api/abilities"` instead.
 *
 * This function reads `claims.roles` which is a flat deduped list that lost
 * scope information — it can produce wrong results (e.g. any org-admin appears
 * as "platform admin" because both hold the "admin" role string). The BFF's
 * actual authorization uses scope-aware containment via `GET /api/v1/me/abilities`.
 *
 * REDESIGN-001 Phase 4.4 — sweep of call-sites follows in Phase 4.2.
 */
export function isPlatformAdmin(claims: JanusJwtClaims | null): boolean {
  if (!claims) return false;
  // Phase 5.1: typed users.is_global_admin is the canonical check.
  if (claims.is_global_admin) return true;
  // Legacy fallback for tokens that pre-date the is_global_admin claim.
  // The Phase 5.1 backfill deleted the (admin, org, "*") marker grant so
  // this only matches users still holding org-scoped admin — overly broad
  // (the deprecation note above already flags this).
  return claims.roles?.includes("admin") ?? false;
}

/**
 * @deprecated Use `useAbility(role, scope)` from `"@/lib/api/abilities"` instead.
 *
 * This function reads `claims.roles` which is a flat deduped list that lost
 * scope information — it returns true for any user who holds admin or owner on
 * *any* scope, not just on the workspace-admin surfaces it is meant to guard.
 * The BFF's actual authorization uses scope-aware containment via
 * `GET /api/v1/me/abilities` and `hasScopedRole`.
 *
 * REDESIGN-001 Phase 4.4 — sweep of call-sites follows in Phase 4.2.
 */
// isWorkspaceAdmin returns true when the principal can administer their own
// workspace — i.e. holds the `admin` or `owner` role on any scope within the
// tenant. Matches the backend's `requireDomainAdmin` posture (BFF
// `services/management/internal/handler/workspace_domains.go`) which accepts
// any admin/owner grant on any org in the tenant (platform admins also pass).
// Use this for workspace-admin surfaces (Service accounts, Custom domains,
// Audit streaming). Real platform-admin surfaces (`/admin/*`) keep using
// `isPlatformAdmin`.
export function isWorkspaceAdmin(claims: JanusJwtClaims | null): boolean {
  if (!claims) return false;
  // Phase 5.1: global admins always pass workspace-admin gates.
  if (claims.is_global_admin) return true;
  return claims.roles?.includes("admin") || claims.roles?.includes("owner") || false;
}
