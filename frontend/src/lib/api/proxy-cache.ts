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

// FUT-016 — detail-page types.
//
// kind discriminates image manifests (config + layers) from image
// indexes / Docker manifest lists (per-platform `manifests[]`). The BFF
// always populates both arrays (empty when not applicable) so callers
// can `.map` without optional-chaining.
export type CachedManifestKind = "image" | "index";

export interface CachedManifestLayer {
  digest: string;
  size: number;
  media_type: string;
}

export interface CachedManifestPlatformRef {
  digest: string;
  size: number;
  media_type: string;
  architecture: string;
  os: string;
  variant?: string;
  os_version?: string;
}

export interface CachedManifestDetail extends CachedManifest {
  kind: CachedManifestKind;
  // Base64-encoded raw manifest body. The "Manifest" tab decodes + pretty-
  // prints this as JSON. We send it base64-encoded because the body is
  // arbitrary bytes from upstream; embedding the raw string in JSON would
  // require server-side escaping that's fiddier than the round-trip.
  body_base64: string;
  layers: CachedManifestLayer[];
  manifests: CachedManifestPlatformRef[];
}

// FUT-017 — per-upstream scan + sign policy shapes. JSON mirrors the
// BFF's `proxyCacheScanPolicyResponse` / `proxyCacheSignPolicyResponse`
// snake_case fields, so no field-rename is needed here.
//
// `severity_threshold`: "" and "none" are both "never block" — the BFF
// accepts either, and we surface them as distinct values so the form can
// model the empty-on-first-load case separately from an explicit "off".
export type ProxyCacheSeverity =
  | ""
  | "none"
  | "low"
  | "medium"
  | "high"
  | "critical";

export interface ProxyCacheScanPolicy {
  upstream_name: string;
  auto_scan: boolean;
  severity_threshold: ProxyCacheSeverity;
  updated_at?: string;
  updated_by?: string;
}

export interface ProxyCacheSignPolicy {
  upstream_name: string;
  auto_sign: boolean;
  key_id?: string;
  created_at?: string;
  updated_at?: string;
}

// ─── Query keys ─────────────────────────────────────────────────────

export const proxyCacheKeys = {
  all: ["proxy-cache"] as const,
  stats: () => [...proxyCacheKeys.all, "stats"] as const,
  list: (filters: CacheListFilters) =>
    [...proxyCacheKeys.all, "list", filters] as const,
  // FUT-016 — per-row detail key so invalidating on evict + invalidating
  // on detail-page navigation are independent.
  detail: (id: string) => [...proxyCacheKeys.all, "detail", id] as const,
  // FUT-017 — per-upstream policy keys. Both the list + per-upstream
  // single-row queries hang off the same `scanPolicy` / `signPolicy`
  // namespace so a PUT can invalidate both with one prefix.
  scanPolicies: () => [...proxyCacheKeys.all, "scanPolicy"] as const,
  scanPolicy: (upstreamName: string) =>
    [...proxyCacheKeys.all, "scanPolicy", upstreamName] as const,
  signPolicies: () => [...proxyCacheKeys.all, "signPolicy"] as const,
  signPolicy: (upstreamName: string) =>
    [...proxyCacheKeys.all, "signPolicy", upstreamName] as const,
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

// useCachedManifest — FUT-016 detail-page hook. Returns the full row
// + parsed layers / per-platform projection + raw body for the manifest
// JSON tab. 404 / 403 surface as TanStack Query errors so the detail
// page can branch on `isError` + status code (404 → "not found" empty
// state, 403 → "workspace admin required" empty state).
export function useCachedManifest(id: string | undefined) {
  return useQuery({
    queryKey: id ? proxyCacheKeys.detail(id) : ["proxy-cache", "detail", "_disabled"],
    enabled: Boolean(id),
    queryFn: async (): Promise<CachedManifestDetail> => {
      // `enabled: false` keeps queryFn from running when id is undefined,
      // but TypeScript still wants a code path — assert here.
      if (!id) throw new Error("id is required");
      const { data } = await apiClient.get<CachedManifestDetail>(
        `/proxy/cache/${encodeURIComponent(id)}`,
      );
      return data;
    },
    // The body is immutable for the lifetime of a row (eviction is the
    // only mutation) so we don't refetch on focus. 5 minutes is enough
    // to keep tab-switching cheap without holding onto stale layer data
    // after an eviction-then-re-cache cycle.
    staleTime: 5 * 60 * 1000,
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

// ─── FUT-017 scan + sign policy hooks ───────────────────────────────
//
// Same probe-and-hide pattern as useCacheStats: 403 (caller is not a
// workspace admin) and 404 (scanner / signer client unwired on the
// management service) both resolve to `null`. The UpstreamPoliciesCard
// hides itself when both list hooks return null, and gracefully shows
// "scanner/signer not wired" inline when only one is unavailable.
//
// 5xx still throws — a real backend failure should surface as an error
// rather than silently dropping the policy editor.

// Internal helper. TanStack Query treats `undefined` as "no data yet"
// (loading), so we return `null` to mean "request completed; the
// feature is unavailable to me." Callers check `data === null`.
function nullOn403or404<T>(fn: () => Promise<T>): () => Promise<T | null> {
  return async () => {
    try {
      return await fn();
    } catch (e) {
      if (e instanceof AxiosError) {
        const s = e.response?.status;
        if (s === 403 || s === 404) return null;
      }
      throw e;
    }
  };
}

// useProxyCacheScanPolicy — fetch the scan policy for one upstream.
// 200 returns the parsed body; 403/404 resolve to `null` so the
// UpstreamPoliciesCard can hide its scan controls without erroring.
export function useProxyCacheScanPolicy(upstreamName: string) {
  return useQuery({
    queryKey: proxyCacheKeys.scanPolicy(upstreamName),
    enabled: Boolean(upstreamName),
    queryFn: nullOn403or404<ProxyCacheScanPolicy>(async () => {
      const { data } = await apiClient.get<ProxyCacheScanPolicy>(
        `/proxy/upstreams/${encodeURIComponent(upstreamName)}/scan-policy`,
      );
      return data;
    }),
    staleTime: 30_000,
  });
}

// useProxyCacheScanPolicies — list scan policies across every upstream
// the tenant has touched. Returns `null` on 403/404 so the policy card
// can hide cleanly. Used by UpstreamPoliciesCard to join with the
// upstreams discovered from the cached-manifest list.
export function useProxyCacheScanPolicies() {
  return useQuery({
    queryKey: proxyCacheKeys.scanPolicies(),
    queryFn: nullOn403or404<ProxyCacheScanPolicy[]>(async () => {
      const { data } = await apiClient.get<{
        policies: ProxyCacheScanPolicy[];
      }>(`/proxy/cache/scan-policies`);
      // The BFF always returns `{ policies: [...] }`. Default to []
      // defensively in case a future BFF version omits the field.
      return data.policies ?? [];
    }),
    staleTime: 30_000,
  });
}

// useUpdateProxyCacheScanPolicy — PUT one upstream's scan policy. On
// success invalidates both the single-policy + list keys so the card
// refetches both views.
export function useUpdateProxyCacheScanPolicy(upstreamName: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: {
      auto_scan: boolean;
      severity_threshold: ProxyCacheSeverity;
    }): Promise<ProxyCacheScanPolicy> => {
      const { data } = await apiClient.put<ProxyCacheScanPolicy>(
        `/proxy/upstreams/${encodeURIComponent(upstreamName)}/scan-policy`,
        body,
      );
      return data;
    },
    onSuccess: () => {
      // Invalidate by the broad `scanPolicy` prefix so the list query
      // + the single-upstream query both refetch. Cheaper than picking
      // two keys and matches the operator's mental model — "I saved;
      // both the row I edited and the list should reflect it."
      void qc.invalidateQueries({ queryKey: proxyCacheKeys.scanPolicies() });
    },
  });
}

// useProxyCacheSignPolicy — single-upstream sign policy mirror of
// useProxyCacheScanPolicy. 403/404 → null.
export function useProxyCacheSignPolicy(upstreamName: string) {
  return useQuery({
    queryKey: proxyCacheKeys.signPolicy(upstreamName),
    enabled: Boolean(upstreamName),
    queryFn: nullOn403or404<ProxyCacheSignPolicy>(async () => {
      const { data } = await apiClient.get<ProxyCacheSignPolicy>(
        `/proxy/upstreams/${encodeURIComponent(upstreamName)}/sign-policy`,
      );
      return data;
    }),
    staleTime: 30_000,
  });
}

// useProxyCacheSignPolicies — list-mode for sign policies.
export function useProxyCacheSignPolicies() {
  return useQuery({
    queryKey: proxyCacheKeys.signPolicies(),
    queryFn: nullOn403or404<ProxyCacheSignPolicy[]>(async () => {
      const { data } = await apiClient.get<{
        policies: ProxyCacheSignPolicy[];
      }>(`/proxy/cache/sign-policies`);
      return data.policies ?? [];
    }),
    staleTime: 30_000,
  });
}

// useUpdateProxyCacheSignPolicy — PUT sign policy. The BFF rejects
// `auto_sign=true` with empty `key_id` as 400; callers must enforce
// the same constraint client-side (the policy card disables Save
// until the combo is valid).
export function useUpdateProxyCacheSignPolicy(upstreamName: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: {
      auto_sign: boolean;
      key_id: string;
    }): Promise<ProxyCacheSignPolicy> => {
      const { data } = await apiClient.put<ProxyCacheSignPolicy>(
        `/proxy/upstreams/${encodeURIComponent(upstreamName)}/sign-policy`,
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: proxyCacheKeys.signPolicies() });
    },
  });
}
