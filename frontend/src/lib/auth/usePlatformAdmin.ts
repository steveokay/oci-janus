/**
 * usePlatformAdmin — true if the current user holds the platform-admin
 * marker (the `role_assignments` row with scope_type='org', scope_value='*'
 * introduced in PENTEST-024 and seeded for the dev admin).
 *
 * Why this is a probe and not a JWT claim:
 *   The JWT only carries flat role names (`['admin']`, etc.), not their
 *   scope values, so the marker isn't decidable from claims alone. Until
 *   a `/api/v1/me` endpoint surfaces a typed `is_platform_admin` flag,
 *   we send one GET to `/admin/tenants?page_size=1` per session. That
 *   route is gated by the marker in services/management
 *   (admin_tenants.go), so a 200 is proof the caller holds it. A 403 is
 *   proof they don't. Any other failure (network, 5xx) is conservatively
 *   treated as "not admin" — we'd rather hide a section than render an
 *   admin nav for someone who can't use it.
 *
 * The result is cached for the session (`staleTime: Infinity`) so the
 * sidebar doesn't re-probe on every render.
 */
import { useQuery } from '@tanstack/react-query'
import { AxiosError } from 'axios'
import { apiClient } from '@/lib/api/client'
import { useAuthStore } from '@/store/authStore'

export function usePlatformAdmin(): boolean {
  const token = useAuthStore((s) => s.token)
  const query = useQuery({
    queryKey: ['auth', 'platform-admin'],
    queryFn: async () => {
      try {
        await apiClient.get('/admin/tenants', { params: { page_size: 1 } })
        return true
      } catch (err) {
        if (err instanceof AxiosError && err.response?.status === 403) {
          return false
        }
        // Network errors / 5xx fall here. Default to non-admin so we
        // never accidentally surface admin nav to a non-admin caller.
        return false
      }
    },
    staleTime: Infinity,
    enabled: !!token,
  })
  return query.data === true
}
