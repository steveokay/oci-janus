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

// ─── FUT-018 — digest-keyed scan + signature hooks ──────────────────
//
// Cached manifests don't sit in a repo / tag — they're keyed on
// (tenant_id, upstream, image, reference, digest) by the proxy. The
// per-tag /scan + /signature routes can't reach them. The BFF added
// four digest-keyed routes (PR #93); these hooks wrap them.
//
// Wire shapes mirror the per-tag routes:
//   • ScanByDigestResponse is the same ScanResponse JSON the per-tag
//     route serializes (handler.go:1038).
//   • SignaturesByDigestResponse is the same SignatureResponse shape
//     (signature.go:51). Unsigned manifests return 200 with
//     `signed=false, signatures=[]` rather than 404 — separates
//     "nothing has signed" from "route disabled".
//
// 403/404 mapping:
//   • GET scan: 404 → null when no scan recorded yet. 403/route-
//     disabled also collapse to null so non-admin or unwired BFFs
//     skip the panel (mirrors useCacheStats + useScan).
//   • GET signatures: 200 even for unsigned; 404 only when route is
//     disabled (signer unwired) — SIGNING_DISABLED sentinel.
//   • POST sign + POST trigger: the mutations throw the AxiosError
//     up so callers can branch on status code (403 writer-required,
//     404 disabled, 400 bad signer_id) the same way SignManifestDialog
//     branches on the per-tag mutation.

// Reuse the ScanResult shape from the existing `types` module so the
// SeverityBar + SeverityLegend + parseFindings helpers used by the
// per-tag ScanPanel can be reused 1:1 in the digest-keyed tab.
import type { ScanResult } from "./types";
import { SIGNING_DISABLED, type SignatureStatus } from "./signature";

export type ScanByDigestResponse = ScanResult;
export type SignaturesByDigestResponse = SignatureStatus;

// PENDING_STATUSES — kept inline (rather than imported from scan.ts) so
// this module doesn't reach into the per-tag hook's polling helpers.
// Same wire values though; drift here would manifest as a stuck pill in
// the Scans tab.
const PENDING_STATUSES: ReadonlyArray<ScanResult["status"]> = ["pending", "running"];
const POLL_INTERVAL_MS = 4_000;
const NULL_POLL_WINDOW_MS = 30_000;

// Extend the query key tree with the digest-keyed namespaces. Both hang
// off the proxy-cache root so a tenant-wide invalidate (rare) still
// catches them.
export const proxyCacheDigestKeys = {
  scanByDigest: (digest: string) =>
    [...proxyCacheKeys.all, "scanByDigest", digest] as const,
  signaturesByDigest: (digest: string) =>
    [...proxyCacheKeys.all, "signaturesByDigest", digest] as const,
};

// useScanByDigest — mirror of useScan but keyed on the manifest digest.
// 404 → null so the Scans tab can render the "no scan yet" CTA; the
// per-tag hook uses the same convention. Polling fires every 4s while
// the result is pending/running OR while the result is null + recent
// (so an auto-scan that lands a few hundred ms after mount surfaces
// without a manual refresh).
export function useScanByDigest(digest: string | undefined) {
  return useQuery({
    queryKey: digest
      ? proxyCacheDigestKeys.scanByDigest(digest)
      : ["proxy-cache", "scanByDigest", "_disabled"],
    enabled: Boolean(digest),
    queryFn: async (): Promise<ScanByDigestResponse | null> => {
      if (!digest) throw new Error("digest is required");
      try {
        const { data } = await apiClient.get<ScanByDigestResponse>(
          `/scan-by-digest/${encodeURIComponent(digest)}`,
        );
        return data;
      } catch (e) {
        // 404 covers two cases the BFF distinguishes by message but the
        // FE treats identically: (a) no scan recorded for this digest,
        // (b) the scanner route is disabled because SCANNER_GRPC_ADDR
        // is unset. Both render the "trigger a scan" CTA. 403 means the
        // caller can read the cache list but isn't a writer; we don't
        // hide the tab in that case (the read GET is open to any
        // tenant member) so 403 collapses to the "no scan" path too.
        if (e instanceof AxiosError) {
          const s = e.response?.status;
          if (s === 404 || s === 403) return null;
        }
        throw e;
      }
    },
    refetchInterval: (q) => {
      const status = q.state.data?.status;
      if (status && PENDING_STATUSES.includes(status)) {
        return POLL_INTERVAL_MS;
      }
      // Brief null-poll window so a background scan (auto-scan-on-cache
      // from FUT-017, or a trigger fired in another tab) surfaces here
      // without a manual refresh.
      if (q.state.data === null) {
        const sinceMount = Date.now() - q.state.dataUpdatedAt;
        if (sinceMount < NULL_POLL_WINDOW_MS) {
          return POLL_INTERVAL_MS;
        }
      }
      return false;
    },
    // Match useScan's posture — the list-table columns also fire this
    // hook; a 30s stale window keeps the per-row request quiet but
    // still surfaces a freshly-completed scan within a list refresh.
    staleTime: 30_000,
  });
}

// useTriggerScanByDigest — POST /scan-by-digest/{digest}. Writes an
// optimistic `pending` stub into the cache for the read hook so the
// "Trigger scan" → "Scanning…" transition is instant; the 4s poll
// then catches the real result row when the scanner writes it.
export function useTriggerScanByDigest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (digest: string) => {
      const { data } = await apiClient.post<{
        scan_id: string;
        manifest_digest: string;
        status: string;
      }>(`/scan-by-digest/${encodeURIComponent(digest)}`, {});
      return data;
    },
    onSuccess: (resp, digest) => {
      const key = proxyCacheDigestKeys.scanByDigest(digest);
      const existing = qc.getQueryData<ScanByDigestResponse | null>(key);
      // Same optimistic-stub shape as useTriggerScan — preserves prior
      // scanner_name/version so a "Rescan" doesn't blank the card.
      const stub: ScanByDigestResponse = {
        scan_id: resp.scan_id || existing?.scan_id || "pending",
        status: "pending",
        scanner_name: existing?.scanner_name ?? "",
        scanner_version: existing?.scanner_version ?? "",
        severity_counts: existing?.severity_counts ?? {},
        findings_json: existing?.findings_json,
        started_at: new Date().toISOString(),
        completed_at: undefined,
      };
      qc.setQueryData(key, stub);
      void qc.invalidateQueries({ queryKey: key });
    },
  });
}

// useSignaturesByDigest — mirror of useSignature (per-tag) but keyed on
// the manifest digest. The BFF returns 200 with `signed=false,
// signatures=[]` when nothing has signed — that's a valid state, not
// a 404. The only 404 case is "route disabled" (signer unwired), which
// we surface via the same SIGNING_DISABLED sentinel useSignature uses
// so callers can branch on it identically.
export function useSignaturesByDigest(digest: string | undefined) {
  return useQuery({
    queryKey: digest
      ? proxyCacheDigestKeys.signaturesByDigest(digest)
      : ["proxy-cache", "signaturesByDigest", "_disabled"],
    enabled: Boolean(digest),
    queryFn: async (): Promise<
      SignaturesByDigestResponse | typeof SIGNING_DISABLED
    > => {
      if (!digest) throw new Error("digest is required");
      try {
        const { data } = await apiClient.get<SignaturesByDigestResponse>(
          `/signatures-by-digest/${encodeURIComponent(digest)}`,
        );
        return data;
      } catch (e) {
        if (e instanceof AxiosError && e.response?.status === 404) {
          return SIGNING_DISABLED;
        }
        throw e;
      }
    },
    staleTime: 30_000,
  });
}

// useSignByDigest — POST /sign-by-digest/{digest}. Optional signer_id
// payload (empty → workspace default). On success invalidates the
// read query so the new row appears next render.
//
// The mutationFn signature is `(digest, signer_id?)`; we model that
// as one args object to keep call-sites readable and to leave room
// for adding `key_id` later if the signer ever exposes a picker.
export interface SignByDigestArgs {
  digest: string;
  signer_id?: string;
}

export function useSignByDigest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ digest, signer_id }: SignByDigestArgs) => {
      const body: Record<string, string> = {};
      if (signer_id && signer_id.length > 0) body.signer_id = signer_id;
      const { data } = await apiClient.post<{
        manifest_digest: string;
        signer_id: string;
        key_id: string;
        signature_digest: string;
        signed_at: string;
      }>(`/sign-by-digest/${encodeURIComponent(digest)}`, body);
      return data;
    },
    onSuccess: (_resp, { digest }) => {
      void qc.invalidateQueries({
        queryKey: proxyCacheDigestKeys.signaturesByDigest(digest),
      });
    },
  });
}

// Re-export shared signature primitives so consumers of the digest
// hooks don't need a second import from `./signature`.
export { SIGNING_DISABLED };
export type { SignatureRecord, SignatureStatus } from "./signature";
