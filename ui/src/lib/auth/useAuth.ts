/**
 * Thin hook that reads the access token from localStorage.
 *
 * We intentionally keep this primitive: the token is written by the login
 * page and cleared by the axios 401 interceptor in client.ts, so any
 * component that needs auth state just calls this hook rather than
 * duplicating the localStorage key name.
 *
 * A richer hook (refresh logic, decoded claims, etc.) can extend this
 * later without changing call sites.
 */
export function useAuth() {
  const token = localStorage.getItem('access_token')
  return { isAuthenticated: !!token, token }
}
