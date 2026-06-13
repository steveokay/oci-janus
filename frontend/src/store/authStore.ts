/**
 * authStore — in-memory authentication state via Zustand.
 *
 * The JWT access token lives only in this store and is wiped on page reload
 * (by design — see CLAUDE-frontend.md §10 and §13). This prevents XSS from
 * reading the token out of localStorage/sessionStorage.
 *
 * Consumers:
 *   - login.tsx         → calls setAuth() after a successful POST /auth/token
 *   - client.ts         → reads token for the Authorization header
 *   - _authenticated.tsx → calls isAuthenticated() in beforeLoad guard
 *   - index.tsx         → calls isAuthenticated() for the root redirect
 *   - useAuth.ts        → re-exports the store selectors for components
 */

import { create } from 'zustand'

/** Decoded JWT payload fields we care about. */
export interface AuthUser {
  sub: string        // user ID
  tenant_id: string
  exp: number        // Unix timestamp (seconds)
}

interface AuthState {
  /** Raw JWT access token — null when logged out or after page reload. */
  token: string | null
  /** Decoded claims from the token — null when no token is present. */
  user: AuthUser | null
  /** Convenience: current tenant ID extracted from the token claims. */
  tenantId: string | null

  /** Store token + decoded claims after a successful login. */
  setAuth: (token: string, user: AuthUser) => void
  /** Clear all auth state — called on logout, 401 response, or token expiry. */
  clearAuth: () => void
  /** Returns true when a token is present in memory. */
  isAuthenticated: () => boolean
}

export const useAuthStore = create<AuthState>((set, get) => ({
  token: null,
  user: null,
  tenantId: null,

  setAuth: (token, user) =>
    set({ token, user, tenantId: user.tenant_id }),

  clearAuth: () =>
    set({ token: null, user: null, tenantId: null }),

  isAuthenticated: () => get().token !== null,
}))
