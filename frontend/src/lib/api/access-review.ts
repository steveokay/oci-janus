import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// access-review — TanStack Query hooks over the FUT-004 access-review
// REST surface exposed by services/management (BFF) at
// /api/v1/access/review.
//
// The BFF proxies each call to services/auth via gRPC. Vite dev proxy
// has an EXPLICIT `/api/v1/access/review` → http://localhost:8091 entry
// (management BFF) that MUST come before the more general
// `/api/v1/access` → :8080 (auth) catchall, or Vite's first-match rule
// sends the request to auth and every call silently 404s. See
// vite.config.ts.
//
// Route reference (BE):
//   GET  /api/v1/access/review/stale    List keys flagged for review
//   POST /api/v1/access/review/snooze   Snooze a key for N days
//
// Semantic notes:
//   - `suggested_action` mirrors the BE heuristic. `UNSPECIFIED` covers
//     any future value the FE doesn't recognise so we never render an
//     empty button — the fallback is "Keep".
//   - `reason` is a free-form string on the wire; the current BE values
//     are "idle" (last_used_at old), "rotation_lapsed" (rotation_due_at
//     in the past), or "both". Empty string is treated as unknown.
//   - The snooze mutation invalidates ["stale-keys"] so the row falls
//     off the list without a manual refetch.

// SuggestedAction — matches the BE's proto enum. Rendered as a hint to
// the operator on which button to emphasise per row.
export type SuggestedAction = "REVOKE" | "KEEP" | "SNOOZE" | "UNSPECIFIED";

// StaleKey — one row in the stale-keys table. Field names use
// snake_case to match the proto-generated JSON on the wire.
//
// `last_used_at` and `rotation_due_at` may be null (never used, or no
// rotation policy applicable).
export interface StaleKey {
  id: string;
  tenant_id: string;
  owner_user_id: string;
  name: string;
  last_used_at: string | null;
  rotation_due_at: string | null;
  review_snoozed_until: string | null;
  suggested_action: SuggestedAction;
  reason: "idle" | "rotation_lapsed" | "both" | "";
}

// SnoozeInput — request body shape for POST /access/review/snooze.
// `days` is bounded on the BE to [1, 90]; the FE defaults to 30 but
// callers may override.
export interface SnoozeInput {
  key_id: string;
  days: number;
}

// Query key factory — one namespace so future review-related hooks
// (e.g. history / audit view) can share invalidation.
export const accessReviewKeys = {
  all: ["stale-keys"] as const,
};

// ── GET /api/v1/access/review/stale ───────────────────────────────────

// useStaleKeys fetches the list of API keys currently flagged for
// review. Admin gate is enforced server-side; non-admin callers see the
// subset of keys they own.
export function useStaleKeys() {
  return useQuery({
    queryKey: accessReviewKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<{ keys: StaleKey[] }>(
        "/access/review/stale",
      );
      // The BE wraps the list in a { keys: [...] } envelope; unwrap so
      // callers work with a plain array (mirrors PoliciesPanel /
      // TrustPanel).
      return data.keys ?? [];
    },
    staleTime: 20_000,
  });
}

// ── POST /api/v1/access/review/snooze ─────────────────────────────────

// useSnoozeKey snoozes a stale key by N days. The BE persists the
// resulting `review_snoozed_until` timestamp on the api_keys row and
// records an audit event. On success we invalidate the stale-keys
// query so the row falls off the list.
export function useSnoozeKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: SnoozeInput) => {
      await apiClient.post("/access/review/snooze", body);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: accessReviewKeys.all });
    },
  });
}
