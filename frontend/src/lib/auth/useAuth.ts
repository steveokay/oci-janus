/**
 * useAuth — convenience hook exposing auth state from the Zustand store.
 *
 * Token is stored in memory only (never localStorage) per CLAUDE-frontend.md §13.
 * Components should use this hook rather than importing the store directly so
 * the abstraction boundary is preserved if the store shape changes.
 */
import { useAuthStore } from '@/store/authStore'

export function useAuth() {
  const token = useAuthStore((s) => s.token)
  const user = useAuthStore((s) => s.user)
  const tenantId = useAuthStore((s) => s.tenantId)
  const clearAuth = useAuthStore((s) => s.clearAuth)
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)

  return { token, user, tenantId, isAuthenticated, logout: clearAuth }
}
