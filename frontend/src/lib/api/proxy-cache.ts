// FUT-013 — pull-through cache visibility.
//
// API client + TanStack Query hooks for the three backend routes:
//
//   GET    /api/v1/proxy/cache               — paginated list
//   GET    /api/v1/proxy/cache/stats         — page-header aggregate
//   DELETE /api/v1/proxy/cache/{id}          — evict single row
//
// All three return 404 when PROXY_GRPC_ADDR is unset on the BFF. The
// sidebar entry + the route's header card both gate on
// `useCacheStats().data !== undefined` so an unconfigured deployment
// degrades to "feature not visible" instead of erroring.

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";

// ─── Types ──────────────────────────────────────────────────────────

export interface CachedManifest {
  id: string;
  upstream_id: string;
  upstream_name: string;
  image: string;
  reference: string;
  digest: string;
  media_type: string;
  size_bytes: number;
  fetched_at: string; // RFC3339Nano UTC, always present
  last_pulled_at?: string; // omitted when never pulled
  pull_count: number;
}

export interface CachedManifestsPage {
  manifests: CachedManifest[];
  next_page_token?: string;
}

export interface CacheStats {
  total_manifests: number;
  total_bytes: number;
  unique_upstreams: number;
  total_pulls: number;
}

export interface CacheListFilters {
  upstream_id?: string;
  image_contains?: string;
  page_size?: number;
}

// ─── Query keys ─────────────────────────────────────────────────────

export const proxyCacheKeys = {
  all: ["proxy-cache"] as const,
  stats: () => [...proxyCacheKeys.all, "stats"] as const,
  list: (filters: CacheListFilters) =>
    [...proxyCacheKeys.all, "list", filters] as const,
};

// ─── Hooks ──────────────────────────────────────────────────────────

// useCacheStats — header aggregate. 403 (non-workspace-admin) or 404
// (PROXY_GRPC_ADDR unset) both resolve to `null` so callers can use
// `stats === null` as a single "feature is not available to me"
// signal. Real 5xx errors still throw so the route's ErrorState
// fires on the page.
//
// null (not undefined) because TanStack Query v5 treats an undefined
// queryFn return as an error condition — null is the cleanest sentinel
// for "the query completed; there is no data and that's expected."
//
// Used by the sidebar to decide whether to render the menu item, and
// by the page header to decide whether to render the page content
// vs an empty-state.
export function useCacheStats() {
  return useQuery({
    queryKey: proxyCacheKeys.stats(),
    queryFn: async (): Promise<CacheStats | null> => {
      try {
        const { data } = await apiClient.get<CacheStats>("/proxy/cache/stats");
        return data;
      } catch (e) {
        if (e instanceof AxiosError) {
          const s = e.response?.status;
          // 403 (non-admin) or 404 (route disabled) → "unavailable to me".
          if (s === 403 || s === 404) return null;
        }
        throw e;
      }
    },
    // Cheap aggregate; refresh on focus is enough.
    staleTime: 30_000,
  });
}

// useCachedManifests — paginated list. Uses infiniteQuery so the
// table can offer a "Load more" button without juggling page-token
// state in the route component.
//
// Filters are part of the query key so changing them resets pagination.
export function useCachedManifests(filters: CacheListFilters = {}) {
  return useInfiniteQuery({
    queryKey: proxyCacheKeys.list(filters),
    initialPageParam: "",
    queryFn: async ({ pageParam }): Promise<CachedManifestsPage> => {
      const params: Record<string, string> = {};
      if (filters.upstream_id) params.upstream_id = filters.upstream_id;
      if (filters.image_contains) params.image_contains = filters.image_contains;
      if (filters.page_size) params.page_size = String(filters.page_size);
      if (pageParam) params.page_token = pageParam;
      const { data } = await apiClient.get<CachedManifestsPage>("/proxy/cache", {
        params,
      });
      return data;
    },
    getNextPageParam: (lastPage) => lastPage.next_page_token ?? undefined,
  });
}

// useEvictCachedManifest — DELETE one cached row. On success, the
// stats card + list query both refetch.
export function useEvictCachedManifest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string): Promise<void> => {
      await apiClient.delete(`/proxy/cache/${encodeURIComponent(id)}`);
    },
    onSuccess: () => {
      // Invalidate every cached page + the stats aggregate. Both
      // are cheap and the operator's mental model is "I clicked
      // evict; the row + the count drop now."
      void qc.invalidateQueries({ queryKey: proxyCacheKeys.all });
    },
  });
}
