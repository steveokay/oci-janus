import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// token-policy — TanStack Query hooks over the FUT-003 token-policy REST
// surface exposed by services/management (BFF) at
// /api/v1/access/token-policy.
//
// The BFF proxies each call to services/auth via gRPC. Vite dev proxy has
// an EXPLICIT `/api/v1/access/token-policy` → http://localhost:8091 entry
// (management BFF) that MUST come before the more general
// `/api/v1/access` → :8080 (auth) catchall, or Vite's first-match rule
// sends the request to auth and every call silently 404s. See
// vite.config.ts.
//
// Route reference (BE):
//   GET /api/v1/access/token-policy   Fetch the current workspace policy
//   PUT /api/v1/access/token-policy   Replace the current workspace policy
//
// Both PUT + GET use nullable numeric fields. `null` is a real value that
// distinguishes "policy disabled for this dimension" from "policy set to
// N days". JSON `null` becomes proto `Int32Value` absence on the wire.
//
// The PUT mutation invalidates the ["token-policy"] query so the panel
// re-renders with the freshly persisted values without a manual refetch.

// TokenPolicy mirrors the JSON shape returned by the BE handler. Field
// names use snake_case to match the proto-generated JSON. Any of the
// three numeric fields may be null — that encodes "this dimension of the
// policy is disabled".
export interface TokenPolicy {
  tenant_id: string;
  max_ttl_days: number | null;
  rotation_interval_days: number | null;
  idle_revoke_days: number | null;
  updated_at: string;
  updated_by_user_id: string | null;
}

// PutTokenPolicyInput is the request body shape for PUT. Callers pass
// null to disable a dimension; the BE persists that as an unset proto
// Int32Value.
export interface PutTokenPolicyInput {
  max_ttl_days: number | null;
  rotation_interval_days: number | null;
  idle_revoke_days: number | null;
}

// Query key factory — one namespace so future policy-related hooks
// (grandfather list, audit log, etc.) can share invalidation.
export const tokenPolicyKeys = {
  all: ["token-policy"] as const,
};

// ── GET /api/v1/access/token-policy ───────────────────────────────────────

// useTokenPolicy fetches the current workspace token policy. Admin-only
// server-side; the hook doesn't enforce that (the BE returns 403 for
// non-admin callers).
export function useTokenPolicy() {
  return useQuery({
    queryKey: tokenPolicyKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<TokenPolicy>(
        "/access/token-policy",
      );
      return data;
    },
    staleTime: 20_000,
  });
}

// ── PUT /api/v1/access/token-policy ───────────────────────────────────────

// usePutTokenPolicy replaces the current workspace policy. Invalidates
// the list query so the panel re-renders with the newly persisted values.
export function usePutTokenPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: PutTokenPolicyInput) => {
      const { data } = await apiClient.put<TokenPolicy>(
        "/access/token-policy",
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: tokenPolicyKeys.all });
    },
  });
}
