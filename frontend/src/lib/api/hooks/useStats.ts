/**
 * useStats — TanStack Query hook for `GET /api/v1/stats`.
 *
 * The management service computes these by fanning out to registry-metadata
 * (repo count + quota usage + vulnerability totals) and registry-audit
 * (daily pulls), so the response represents an authoritative cross-service
 * snapshot. We don't paginate it — it's always a single object.
 *
 * Refetch policy:
 *   * `refetchInterval: 60s` — the dashboard is a glanceable surface; an
 *     auto-refresh once a minute keeps numbers fresh without spamming
 *     the backend or making the network tab feel alive.
 *   * `refetchOnWindowFocus: false` inherited from the root QueryClient.
 *
 * The response shape mirrors the Go `StatsResponse` struct in
 * `services/management/internal/handler/handler.go`. If a field name
 * changes there it must change here too — there's no codegen.
 */
import { useQuery } from '@tanstack/react-query'
import { apiClient } from '@/lib/api/client'

export interface StatsResponse {
  total_repos: number
  storage_used_bytes: number
  storage_quota_bytes: number
  daily_pulls: number
  vulnerability_count: number
  system_health_pct: number
}

export function useStats() {
  return useQuery({
    queryKey: ['stats'],
    queryFn: async () => {
      const { data } = await apiClient.get<StatsResponse>('/stats')
      return data
    },
    refetchInterval: 60_000,
  })
}
