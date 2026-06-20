import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// FE-API-030 — pull / push analytics time-series.
//
// Two endpoints:
//   GET /api/v1/stats/analytics                       — tenant-wide
//   GET /api/v1/repositories/{org}/{repo}/analytics   — per-repo
//
// Both share the same response shape and accept (metric, range) query
// params. The BFF pre-fills empty buckets so a quiet window comes back as
// count=0 rather than absent — the sparkline can iterate the array
// without gap-filling on the client.
//
// Known backend gap: pull.image is not currently produced by the audit
// consumer, so ?metric=pulls returns a flat-zero series today. The UI
// still renders it (correctly) as a zero line; we won't gate the toggle
// on backend wiring.

export type AnalyticsMetric = "pulls" | "pushes";
export type AnalyticsRange = "24h" | "7d" | "30d";

export interface AnalyticsBucket {
  bucket_start: string;
  count: number;
}

export interface AnalyticsResponse {
  metric: string;
  range: string;
  bucket_size_secs: number;
  buckets: AnalyticsBucket[];
  total: number;
}

interface TenantAnalyticsArgs {
  metric: AnalyticsMetric;
  range: AnalyticsRange;
  // Optional, defaults true. Used by AnalyticsCard to silence the tenant
  // query when the card is mounted in repo-scope.
  enabled?: boolean;
}

interface RepoAnalyticsArgs {
  org: string;
  repo: string;
  metric: AnalyticsMetric;
  range: AnalyticsRange;
  enabled?: boolean;
}

export const analyticsKeys = {
  tenant: (metric: string, range: string) =>
    ["analytics", "tenant", metric, range] as const,
  repo: (org: string, repo: string, metric: string, range: string) =>
    ["analytics", "repo", org, repo, metric, range] as const,
};

export function useTenantAnalytics({
  metric,
  range,
  enabled = true,
}: TenantAnalyticsArgs) {
  return useQuery({
    queryKey: analyticsKeys.tenant(metric, range),
    enabled,
    queryFn: async () => {
      const params = new URLSearchParams({ metric, range });
      const { data } = await apiClient.get<AnalyticsResponse>(
        `/stats/analytics?${params.toString()}`,
      );
      return data;
    },
    // Range governs how often we refresh — short windows feel live, long
    // windows can sit on a longer staleTime so we don't hammer the audit
    // service for a 30d view that barely moves bucket-to-bucket.
    staleTime: range === "24h" ? 60_000 : range === "7d" ? 5 * 60_000 : 10 * 60_000,
  });
}

export function useRepoAnalytics({
  org,
  repo,
  metric,
  range,
  enabled = true,
}: RepoAnalyticsArgs) {
  return useQuery({
    queryKey: analyticsKeys.repo(org, repo, metric, range),
    enabled: enabled && Boolean(org && repo),
    queryFn: async () => {
      const params = new URLSearchParams({ metric, range });
      const { data } = await apiClient.get<AnalyticsResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/analytics?${params.toString()}`,
      );
      return data;
    },
    staleTime: range === "24h" ? 60_000 : range === "7d" ? 5 * 60_000 : 10 * 60_000,
  });
}
