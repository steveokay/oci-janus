/**
 * usePlatformAdmin — UX-level "is this user a platform admin?" check.
 *
 * IMPORTANT: this is a UX gate only. It hides admin controls the server
 * would deny anyway. The authoritative check lives in the management API
 * (PENTEST-024 — hasScopedRole(_, "org", "*", "admin")), which we can't
 * fully replicate client-side because the JWT only carries the flat
 * `roles` list, not the scope of each assignment.
 *
 * Heuristic: a user is treated as a candidate platform-admin when they
 * hold an `admin` or `owner` role anywhere in the tenant. If the server
 * later rejects an `/admin/*` call (because their assignment is on a
 * specific org, not the `*` marker), the UI surfaces the 403 via the
 * existing apiClient interceptor + toast pattern.
 */

import { useAuthStore } from '@/store/authStore'

/** True when the user holds admin or owner anywhere in the tenant. */
export function useUserIsPlatformAdmin(): boolean {
  const user = useAuthStore((s) => s.user)
  const roles = user?.roles
  if (!Array.isArray(roles)) return false
  return roles.includes('admin') || roles.includes('owner')
}
