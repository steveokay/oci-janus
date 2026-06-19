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

export function isPlatformAdmin(claims: JanusJwtClaims | null): boolean {
  // Platform-admin marker — backend grants `(admin, org, "*")` via the dev seed
  // migration. The `roles` claim is the deduped role-name list; we look for the
  // capital-A literal that the migration uses.
  if (!claims?.roles) return false;
  return claims.roles.includes("admin");
}
