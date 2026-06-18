/**
 * authStore — JWT + user-claims state, kept in MEMORY ONLY.
 *
 * FE-SEC-001 / FE-SEC-002 (security.md):
 *   We deliberately do NOT persist the token. Putting a JWT in localStorage
 *   or sessionStorage gives any XSS payload free reign — the whole point of
 *   the React + CSP setup is to make that impossible. On refresh the user
 *   logs in again. When we add refresh-token flow it must be HttpOnly
 *   cookie only (FE-SEC-009).
 *
 * Why Zustand: tiny, no boilerplate, no Provider, plays well with
 * TanStack Router's beforeLoad sync reads via .getState().
 */
import { create } from 'zustand'

export interface AuthUser {
  sub: string        // user id (UUID)
  tenantId: string
  username: string
  roles: string[]    // flat role names — see services/auth Claims.Roles
  exp: number        // unix seconds
}

interface AuthState {
  token: string | null
  user: AuthUser | null
  setSession: (token: string) => void
  clearSession: () => void
}

export const useAuthStore = create<AuthState>((set) => ({
  token: null,
  user: null,
  setSession: (token) => set({ token, user: decodeJwt(token) }),
  clearSession: () => set({ token: null, user: null }),
}))

/**
 * Decode JWT payload WITHOUT verifying signature. The server already
 * validated the token (that's how we got it) and re-verifies it on every
 * request. The client only needs the payload to know the user's tenant +
 * roles for UI gating. Verification on the client is pointless — anyone
 * who can substitute a token in memory can also substitute the
 * verification result.
 */
function decodeJwt(token: string): AuthUser | null {
  try {
    const payload = token.split('.')[1]
    // base64url -> base64
    const padded = payload.replace(/-/g, '+').replace(/_/g, '/')
    const json = atob(padded.padEnd(padded.length + ((4 - (padded.length % 4)) % 4), '='))
    const claims = JSON.parse(json) as {
      sub: string
      tenant_id: string
      username?: string
      roles?: string[]
      exp: number
    }
    return {
      sub: claims.sub,
      tenantId: claims.tenant_id,
      username: claims.username ?? '',
      roles: Array.isArray(claims.roles) ? claims.roles : [],
      exp: claims.exp,
    }
  } catch {
    return null
  }
}
