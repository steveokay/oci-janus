import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// oidc-trust — TanStack Query hooks over the FUT-001 OIDC-trust REST
// surface exposed by services/management (BFF) at /api/v1/access/oidc-trust.
//
// The BFF proxies each call to services/auth via gRPC. Vite dev proxy has
// an EXPLICIT `/api/v1/access/oidc-trust` → http://localhost:8091 entry
// (management BFF) that MUST come before the more general
// `/api/v1/access` → :8080 (auth) catchall, or Vite's first-match rule
// sends the request to auth and every call silently 404s. See
// vite.config.ts + commit 25be20c.
//
// Route reference (BE):
//   GET    /api/v1/access/oidc-trust        List all trusts for the tenant
//   POST   /api/v1/access/oidc-trust        Create a new trust
//   PATCH  /api/v1/access/oidc-trust/:id    Partial update
//   DELETE /api/v1/access/oidc-trust/:id    Delete
//
// Every mutation invalidates the ["oidc-trusts"] query so cached UIs
// refresh immediately without a manual refetch.

// OIDCTrust mirrors the JSON shape returned by the BE handler. Fields use
// snake_case to match the proto-generated JSON. `last_used_at` is nullable
// (server omits or emits null when the trust has never been exchanged).
export interface OIDCTrust {
  id: string;
  tenant_id: string;
  service_account_id: string;
  display_name: string;
  issuer_url: string;
  audience: string;
  subject_pattern: string;
  jwks_cache_ttl_seconds: number;
  created_at: string;
  updated_at: string;
  last_used_at?: string | null;
}

// Query key factory — keeps every OIDC-trust key under one namespace so
// invalidation stays consistent as more hooks are added.
export const oidcTrustKeys = {
  all: ["oidc-trusts"] as const,
  one: (id: string) => ["oidc-trusts", id] as const,
};

// ── GET /api/v1/access/oidc-trust ─────────────────────────────────────────

// ListOIDCTrustsResponse mirrors the BE handler's paginated shape. Only
// `trusts` is consumed today — pagination is not surfaced in the UI yet.
interface ListOIDCTrustsResponse {
  trusts: OIDCTrust[];
  next_page_token?: string;
}

// useOIDCTrusts fetches every OIDC trust config for the caller's tenant.
// Admin-only server-side; the hook does not enforce that — the backend
// returns 403 for non-admin callers.
export function useOIDCTrusts() {
  return useQuery({
    queryKey: oidcTrustKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<ListOIDCTrustsResponse>(
        "/access/oidc-trust",
      );
      return data.trusts;
    },
    staleTime: 20_000,
  });
}

// ── POST /api/v1/access/oidc-trust ────────────────────────────────────────

// CreateOIDCTrustBody is the request shape for POST. Field names match the
// BE proto exactly. `jwks_cache_ttl_seconds` is optional — when omitted
// the BE uses its configured default.
export interface CreateOIDCTrustBody {
  service_account_id: string;
  display_name: string;
  issuer_url: string;
  audience: string;
  subject_pattern: string;
  jwks_cache_ttl_seconds?: number;
}

// useCreateOIDCTrust creates a new OIDC trust config. Invalidates the
// list query so the new row appears immediately.
export function useCreateOIDCTrust() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateOIDCTrustBody) => {
      const { data } = await apiClient.post<OIDCTrust>(
        "/access/oidc-trust",
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: oidcTrustKeys.all });
    },
  });
}

// ── PATCH /api/v1/access/oidc-trust/:id ───────────────────────────────────

// UpdateOIDCTrustBody is the partial-update shape. Any field left
// unspecified is preserved server-side.
export interface UpdateOIDCTrustBody {
  id: string;
  display_name?: string;
  issuer_url?: string;
  audience?: string;
  subject_pattern?: string;
  jwks_cache_ttl_seconds?: number;
}

// useUpdateOIDCTrust sends a partial PATCH. Invalidates both the list and
// the single-item cache so every consumer refreshes.
export function useUpdateOIDCTrust() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, ...body }: UpdateOIDCTrustBody) => {
      const { data } = await apiClient.patch<OIDCTrust>(
        `/access/oidc-trust/${encodeURIComponent(id)}`,
        body,
      );
      return data;
    },
    onSuccess: (_data, { id }) => {
      void qc.invalidateQueries({ queryKey: oidcTrustKeys.all });
      void qc.invalidateQueries({ queryKey: oidcTrustKeys.one(id) });
    },
  });
}

// ── DELETE /api/v1/access/oidc-trust/:id ──────────────────────────────────

// useDeleteOIDCTrust hard-deletes a trust config. Returns 204 No Content
// on success. Invalidates the list so the row disappears.
export function useDeleteOIDCTrust() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await apiClient.delete(
        `/access/oidc-trust/${encodeURIComponent(id)}`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: oidcTrustKeys.all });
    },
  });
}
