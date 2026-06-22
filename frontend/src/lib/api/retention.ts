import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";

// Beacon — Per-repo retention policies (FE-API-037 + FE-API-038).
//
// Slice 1 of S11 only consumes the GET routes — write/dry-run/run/delete
// hooks land in slices 2/3 on top of the same key factory and types.
//
// Backend routes:
//   GET    /api/v1/repositories/{org}/{repo}/policies/retention
//   GET    /api/v1/repositories/{org}/{repo}/policies/retention/preview
//
// GET returns the per-repo policy when one exists. When no per-repo row
// exists, the BFF first tries the org default and returns it with
// `inherited_from: "org"`. Only when BOTH are missing does it return 404
// with a sentinel body `{ code: "no-policy" }`. The hook surfaces that as
// a `notFound: true` flag rather than an error so the UI can render a
// "no policy yet" empty state without a try/catch in the component.

// ── Types ───────────────────────────────────────────────────────────────────

// RetentionRuleKind is the allowlist enforced server-side by the metadata
// gRPC handler. Frontend stays in sync so the rule-editor chip menu
// matches what the server accepts.
//
// FE-API-043 added `max_idle_days` (pull-activity-based eviction). The
// other four landed in FE-API-037.
export type RetentionRuleKind =
  | "max_age_days"
  | "max_count"
  | "max_size_bytes"
  | "dangling_grace_days"
  | "max_idle_days";

export interface RetentionRule {
  kind: RetentionRuleKind;
  // Value semantics depend on kind:
  //   max_age_days, dangling_grace_days, max_idle_days → days
  //   max_count                                         → manifest count
  //   max_size_bytes                                    → bytes
  // Always int64 on the wire; clamping/validation is server-side.
  value: number;
}

// InheritanceSource — set by the BFF on every GET response so the UI can
// label the policy source without a follow-up call.
//   "repo" — the response carries the per-repo row.
//   "org"  — no per-repo row; the response carries the parent-org default.
//   ""     — pre-FE-API-039 servers may omit the field. Treat as "repo".
export type InheritanceSource = "repo" | "org" | "";

export interface RetentionPolicy {
  repo_id: string;
  // Populated when the policy represents an org default returned via
  // inheritance. Empty for a pure per-repo policy.
  org_id?: string;
  tenant_id: string;
  enabled: boolean;
  rules: RetentionRule[];
  protected_tag_patterns: string[];
  // RFC3339; absent (or absent string field) when the policy is past its
  // 24h preview window. UI uses this to decide whether to render the
  // "preview ends in N" banner above the editor.
  preview_until?: string;
  created_at: string;
  updated_at: string;
  updated_by?: string;
  inherited_from?: InheritanceSource;
}

// PreviewState is the GET .../preview shape. Carries the dashboard-ready
// derived fields so the banner can render without re-parsing
// `preview_until` against the wall clock.
export interface PreviewState {
  enabled: boolean;
  preview_until?: string;
  // Server-derived "preview_until is in the future" — single source of
  // truth so the FE never drifts off a clock skew.
  in_preview_window: boolean;
  // Mirrors the dry-run totals; useful both during and after the preview
  // window so the banner can show "would delete N manifests now".
  would_delete_count: number;
  would_delete_bytes: number;
  policy_updated_at?: string;
}

// RetentionQueryResult is the shape returned by useRepoRetention so the
// component can branch on (loaded, no-policy, error) without unpacking
// TanStack's discriminated union for every case. `notFound` collapses the
// 404+"no-policy" body into a single boolean.
export interface RetentionQueryResult {
  policy: RetentionPolicy | undefined;
  notFound: boolean;
  isLoading: boolean;
  isError: boolean;
  // refetch is exposed so the slice-2 PUT/DELETE mutations can invalidate
  // surgically without pulling in useQueryClient at every call site.
  refetch: () => void;
}

// ── Key factory ─────────────────────────────────────────────────────────────

// Keyed by (org, repo) so a single tenant viewing many repos in tabs gets
// independent cache entries; mirrors the pattern used by scan / signature /
// members queries elsewhere.
export const retentionKeys = {
  all: ["retention"] as const,
  repo: (org: string, repo: string) =>
    [...retentionKeys.all, "repo", org, repo] as const,
  repoPreview: (org: string, repo: string) =>
    [...retentionKeys.all, "repo", org, repo, "preview"] as const,
};

// ── Hooks ───────────────────────────────────────────────────────────────────

// useRepoRetention — GET the per-repo policy (or the inherited org default).
//
// Treats 404 with `code: "no-policy"` as a successful "no policy anywhere"
// state instead of an error, because the empty-state UI needs to know the
// difference between "server couldn't answer" and "operator hasn't
// configured a policy yet". Every other failure path stays an error.
//
// 30s staleTime mirrors usePolicy() — retention policies change at human
// speed, polling faster only burns network.
export function useRepoRetention(
  org: string,
  repo: string,
): RetentionQueryResult {
  const q = useQuery({
    queryKey: retentionKeys.repo(org, repo),
    queryFn: async () => {
      try {
        const { data } = await apiClient.get<RetentionPolicy>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/retention`,
        );
        return { policy: data, notFound: false } as const;
      } catch (err) {
        // The BFF responds 404 with a body like `{code:"no-policy"...}` when
        // neither a per-repo override nor an org default exists. Collapse
        // that into a non-error "no policy" state; everything else (real
        // server errors, network, 401/403) re-throws so TanStack flips to
        // `isError`.
        if (err instanceof AxiosError && err.response?.status === 404) {
          const code = (err.response.data as { code?: string } | undefined)?.code;
          if (code === "no-policy") {
            return { policy: undefined, notFound: true } as const;
          }
        }
        throw err;
      }
    },
    staleTime: 30_000,
  });

  return {
    policy: q.data?.policy,
    notFound: q.data?.notFound ?? false,
    isLoading: q.isLoading,
    isError: q.isError,
    refetch: () => void q.refetch(),
  };
}

// useRepoRetentionPreview — GET the preview-window state (FE-API-038).
//
// Only meaningful once a policy exists; slice-1 components gate the call
// behind `policy !== undefined`. The hook itself stays unconditional so
// the cache key is stable across the tab's mount lifecycle and slice 2's
// "Save policy" mutation can invalidate without re-thinking the key.
//
// A 30s poll keeps the countdown banner roughly current without being
// chatty — the executor only runs on a multi-minute cadence anyway.
export function useRepoRetentionPreview(
  org: string,
  repo: string,
  enabled: boolean,
) {
  return useQuery({
    queryKey: retentionKeys.repoPreview(org, repo),
    queryFn: async () => {
      const { data } = await apiClient.get<PreviewState>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/retention/preview`,
      );
      return data;
    },
    enabled,
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}

// ── Helpers ────────────────────────────────────────────────────────────────

// formatRule renders one rule as the operator would describe it. Used by
// the read-only summary card and (later) the rule editor's chip labels so
// the wording matches both surfaces.
//
// `value` semantics by kind:
//   - days kinds            → "30 days"
//   - max_count             → "50 manifests"
//   - max_size_bytes        → "10.5 GB" (units rounded by formatBytes upstream)
//
// max_size_bytes is intentionally NOT byte-formatted here — the caller
// passes `value` through `formatBytes` because the component already
// imports it. Keeping the helper unit-agnostic avoids a circular import
// with the format util.
export function describeRule(rule: RetentionRule): string {
  switch (rule.kind) {
    case "max_age_days":
      return `Delete manifests older than ${rule.value} days`;
    case "max_count":
      return `Keep at most ${rule.value} manifests`;
    case "max_size_bytes":
      // Caller renders the byte total separately; this string carries the
      // suffix so the editor chip can show "max 10.5 GB stored" by
      // composing the prefix + formatBytes(value).
      return `Cap total storage at`;
    case "dangling_grace_days":
      return `Sweep untagged manifests after ${rule.value} days`;
    case "max_idle_days":
      return `Delete manifests with no pulls in ${rule.value} days`;
    default:
      // Forward-compat guard — a new rule kind from a future backend
      // shouldn't break the panel. Render the raw kind so an operator
      // sees something rather than a blank row.
      return `${rule.kind}: ${rule.value}`;
  }
}

// ruleLabel — the chip-friendly short name used in the rule editor. Kept
// next to describeRule so a new kind only needs one PR to add both.
export function ruleLabel(kind: RetentionRuleKind): string {
  switch (kind) {
    case "max_age_days":
      return "Max age";
    case "max_count":
      return "Max count";
    case "max_size_bytes":
      return "Max storage";
    case "dangling_grace_days":
      return "Dangling grace";
    case "max_idle_days":
      return "Max idle";
    default:
      return kind;
  }
}

// ── Slice 2 — write/delete/dry-run ─────────────────────────────────────────

// UpdateRetentionBody is the PUT shape. `updated_by` is intentionally
// absent — the BFF reads it from the JWT and the client cannot
// impersonate. Naming matches the snake_case JSON the BFF accepts.
export interface UpdateRetentionBody {
  enabled: boolean;
  rules: RetentionRule[];
  protected_tag_patterns: string[];
}

// DryRunDeletion is one entry of the would-delete preview. `tags` /
// `reasons` are always non-null arrays on the wire.
export interface DryRunDeletion {
  manifest_id: string;
  manifest_digest: string;
  tags: string[];
  pushed_at: string;
  size_bytes: number;
  reasons: string[];
}

// DryRunProtected is one entry of the protected-skipped preview. The BFF
// returns the FIRST pattern that matched — the UI only needs to show why
// this manifest was spared.
export interface DryRunProtected {
  manifest_id: string;
  manifest_digest: string;
  tags: string[];
  matched_pattern: string;
}

// DryRunResponse is the full POST .../retention/dry-run body. The
// `truncated` flag is the FE's cue to render "showing 1000 of N
// candidates" honestly — total_count remains the un-truncated total.
export interface DryRunResponse {
  would_delete: DryRunDeletion[];
  protected_skipped: DryRunProtected[];
  total_count: number;
  total_bytes: number;
  evaluated_at: string;
  truncated: boolean;
}

// useUpdateRepoRetention — PUT the policy. Seeds the cache with the
// server's canonical response so the editor's "dirty" baseline shifts to
// the new saved state without an extra round-trip, then invalidates the
// preview key so the 24h banner stamps refresh.
export function useUpdateRepoRetention(org: string, repo: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: UpdateRetentionBody) => {
      const { data } = await apiClient.put<RetentionPolicy>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/retention`,
        body,
      );
      return data;
    },
    onSuccess: (fresh) => {
      // Seed the cache (mirroring useUpdatePolicy in scan-policy.ts).
      // Wrap in the { policy, notFound } shape that useRepoRetention
      // returns so the consumer doesn't see a transient "not-found"
      // state caused by structural drift.
      qc.setQueryData(retentionKeys.repo(org, repo), {
        policy: fresh,
        notFound: false,
      });
      void qc.invalidateQueries({ queryKey: retentionKeys.repo(org, repo) });
      void qc.invalidateQueries({
        queryKey: retentionKeys.repoPreview(org, repo),
      });
    },
  });
}

// useDeleteRepoRetention — DELETE the per-repo override. Backend is
// non-idempotent (returns 404 when there's nothing to delete) so a
// pre-existing inherited policy is unaffected — the BFF only ever
// removes the per-repo row.
export function useDeleteRepoRetention(org: string, repo: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      await apiClient.delete(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/retention`,
      );
    },
    onSuccess: () => {
      // Invalidate everything — the next GET will fall back to the org
      // default (if any) and we want the panel to re-render against the
      // server's truth, not optimistic state.
      void qc.invalidateQueries({ queryKey: retentionKeys.repo(org, repo) });
      void qc.invalidateQueries({
        queryKey: retentionKeys.repoPreview(org, repo),
      });
    },
  });
}

// useDryRunRetention — POST a candidate policy and get back the
// would-delete + protected-skipped preview. Read-only on the server —
// nothing persists — so the hook is a plain mutation without any cache
// side-effects.
export function useDryRunRetention(org: string, repo: string) {
  return useMutation({
    mutationFn: async (body: UpdateRetentionBody) => {
      const { data } = await apiClient.post<DryRunResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/retention/dry-run`,
        body,
      );
      return data;
    },
  });
}

// ── Slice 3 — executor trigger + run status polling ────────────────────────

// RetentionRunStatus is the JSON shape of GET .../retention/runs/{run_id}.
// Mirrors `retentionRunStatusResponse` on the BFF; timestamps are RFC3339
// strings so the dashboard parses them like every other API surface.
export interface RetentionRunStatus {
  run_id: string;
  repo_id?: string;
  // "retention" | "retention_grace" — the BFF surfaces both so the same
  // hook works for the per-repo executor (mode="retention") and a
  // hypothetical per-repo grace finaliser (not exposed yet).
  mode: string;
  // "queued" | "running" | "completed" | "failed"
  status: string;
  requested_at?: string;
  started_at?: string;
  completed_at?: string;
  manifests_marked: number;
  manifests_deleted: number;
  blobs_freed: number;
  bytes_freed: number;
  error_message?: string;
  triggered_by?: string;
}

// TriggerRetentionResponse is the 202 body from POST .../retention/run.
// Always returns status="queued" — the worker pool picks up the row on
// the next tick.
export interface TriggerRetentionResponse {
  run_id: string;
  // Always "queued" on accept; here as a string for forward compat with
  // any future synchronous-mode addition.
  status: string;
}

// isTerminalRunStatus — returns true when the run is in a state that
// won't progress without another trigger. Used to stop polling once the
// run lands in "completed" or "failed".
export function isTerminalRunStatus(s: string | undefined): boolean {
  return s === "completed" || s === "failed";
}

// useTriggerRetentionRun — fire-and-watch. POST returns immediately with
// a run_id; the caller is responsible for plugging that id into
// useRetentionRunStatus to render progression. No cache invalidation
// here — the policy itself isn't changing.
export function useTriggerRetentionRun(org: string, repo: string) {
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<TriggerRetentionResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/retention/run`,
        {},
      );
      return data;
    },
  });
}

// ── REM-013 gap 2 — per-repo retention run history ─────────────────────────

// RepoRetentionRun is the JSON shape returned by GET .../retention/runs.
// Mirrors GCRunResponse on the BFF; one entry per gc_runs row scoped to
// the addressed repo + the two retention modes.
export interface RepoRetentionRun {
  run_id: string;
  mode: string; // "retention" | "retention_grace"
  status: string;
  requested_at?: string;
  started_at?: string;
  completed_at?: string;
  duration_ms: number;
  blobs_freed: number;
  manifests_deleted: number;
  bytes_freed: number;
  error_message?: string;
  triggered_by?: string;
}

interface RepoRetentionRunsPage {
  runs: RepoRetentionRun[];
  next_page_token?: string;
}

// useRepoRetentionRuns — paginated history for the per-repo Retention
// tab's "Run history" panel. Infinite query so the panel can render a
// "Load more" affordance without re-architecting later.
//
// The GET route is reader-grade on the BFF, so non-admins also see this
// — same RBAC as the existing GetRetentionRunStatus sibling.
export function useRepoRetentionRuns(org: string, repo: string) {
  return useInfiniteQuery({
    queryKey: [...retentionKeys.repo(org, repo), "runs"] as const,
    queryFn: async ({ pageParam }) => {
      const params = new URLSearchParams();
      params.set("limit", "20");
      if (pageParam) params.set("page_token", pageParam as string);
      const { data } = await apiClient.get<RepoRetentionRunsPage>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/retention/runs?${params.toString()}`,
      );
      return data;
    },
    initialPageParam: "",
    getNextPageParam: (last) => last.next_page_token || undefined,
    staleTime: 15_000,
  });
}

// useRetentionRunStatus — poll one run's status until it lands in a
// terminal state. Polls every 5s while pending/running; stops once
// completed/failed (TanStack supports a function in refetchInterval that
// returns false to halt polling).
//
// Passing `runId === null` disables the query entirely — used by the
// RetentionRunCard before the operator has triggered anything.
export function useRetentionRunStatus(
  org: string,
  repo: string,
  runId: string | null,
) {
  return useQuery({
    queryKey: ["retention", "repo", org, repo, "run", runId ?? ""],
    queryFn: async () => {
      const { data } = await apiClient.get<RetentionRunStatus>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/policies/retention/runs/${encodeURIComponent(runId!)}`,
      );
      return data;
    },
    enabled: !!runId,
    // 5s while non-terminal, then stop. Mirrors the polling cadence of
    // the compliance-reports panel for a similar "async job in flight"
    // UX. Two consecutive terminal reads still happen (one to flip the
    // refetchInterval predicate, one is already in-flight) — that's
    // fine, the BFF's GET is cheap.
    refetchInterval: (q) =>
      isTerminalRunStatus(q.state.data?.status) ? false : 5_000,
  });
}

// ── Slice 4 — org default retention (FE-API-039) ───────────────────────────

// OrgRetentionQueryResult mirrors RetentionQueryResult — collapses the
// "no-org-default" 404 into a boolean so the empty-state branch is
// switchable without a try/catch in the component.
export interface OrgRetentionQueryResult {
  policy: RetentionPolicy | undefined;
  notFound: boolean;
  isLoading: boolean;
  isError: boolean;
  refetch: () => void;
}

// Key factory extension. Kept on `retentionKeys.all` so invalidating the
// whole family (e.g. after a multi-policy mutation in a hypothetical
// future bulk-tool) still works.
export const orgRetentionKey = (org: string) =>
  [...retentionKeys.all, "org", org] as const;

// useOrgRetention — GET the org default. 404 "no-org-default" collapses
// to notFound; everything else flips to isError. Mirrors useRepoRetention
// so the editor + summary surfaces share one mental model.
export function useOrgRetention(org: string): OrgRetentionQueryResult {
  const q = useQuery({
    queryKey: orgRetentionKey(org),
    queryFn: async () => {
      try {
        const { data } = await apiClient.get<RetentionPolicy>(
          `/orgs/${encodeURIComponent(org)}/policies/retention`,
        );
        return { policy: data, notFound: false } as const;
      } catch (err) {
        if (err instanceof AxiosError && err.response?.status === 404) {
          const code = (err.response.data as { code?: string } | undefined)
            ?.code;
          if (code === "no-org-default") {
            return { policy: undefined, notFound: true } as const;
          }
        }
        throw err;
      }
    },
    staleTime: 30_000,
  });
  return {
    policy: q.data?.policy,
    notFound: q.data?.notFound ?? false,
    isLoading: q.isLoading,
    isError: q.isError,
    refetch: () => void q.refetch(),
  };
}

// useUpdateOrgRetention — PUT the org default. Cache invalidation fans
// out to every per-repo retention key under this tenant so a downstream
// repo summary card flips from "no policy" to "inherited" on the next
// fetch. We use predicate-based invalidation rather than naming each key
// because the operator may have many tabs open; better safe than stale.
export function useUpdateOrgRetention(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: UpdateRetentionBody) => {
      const { data } = await apiClient.put<RetentionPolicy>(
        `/orgs/${encodeURIComponent(org)}/policies/retention`,
        body,
      );
      return data;
    },
    onSuccess: (fresh) => {
      qc.setQueryData(orgRetentionKey(org), {
        policy: fresh,
        notFound: false,
      });
      void qc.invalidateQueries({ queryKey: orgRetentionKey(org) });
      // Bust every per-repo retention cache — they may flip from
      // "no-policy" to "inherited" once the default lands.
      void qc.invalidateQueries({
        predicate: (q) =>
          q.queryKey[0] === "retention" && q.queryKey[1] === "repo",
      });
    },
  });
}

// useDeleteOrgRetention — DELETE the org default. Returns 204 on success,
// 404 when no default exists (non-idempotent — the FE treats both as
// "now you have no default", but surfaces the 404 as a distinct toast).
export function useDeleteOrgRetention(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      await apiClient.delete(
        `/orgs/${encodeURIComponent(org)}/policies/retention`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: orgRetentionKey(org) });
      void qc.invalidateQueries({
        predicate: (q) =>
          q.queryKey[0] === "retention" && q.queryKey[1] === "repo",
      });
    },
  });
}
