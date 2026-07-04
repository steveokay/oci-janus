import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — Admin garbage collection (FE-API-032).
//
// All three routes require the platform-admin marker grant; the BFF
// returns 403 for under-privileged callers and 404 when GC_GRPC_ADDR is
// not configured. The route surface mirrors the gRPC GCService:
//
//   GET  /api/v1/admin/gc/status                — last run snapshot
//   GET  /api/v1/admin/gc/runs                  — paginated run history
//   POST /api/v1/admin/gc/run  {mode}           — async enqueue
//
// `mode` allowlist matches the gc_run_mode SQL enum on services/gc; we
// surface it here so the UI's "Run now" dialog picks from a typed set.

export type GCMode = "dry-run" | "manifests" | "blobs" | "full";

export const GC_MODES: ReadonlyArray<{
  value: GCMode;
  label: string;
  description: string;
  // `destructive=true` shows a stronger warning copy in the confirm
  // dialog. dry-run is the only safe mode; the rest mutate storage.
  destructive: boolean;
}> = [
  {
    value: "dry-run",
    label: "Dry run",
    description: "Report what would be freed without touching storage.",
    destructive: false,
  },
  {
    value: "manifests",
    label: "Manifests only",
    description: "Delete orphaned manifest rows; blobs survive until a later sweep.",
    destructive: true,
  },
  {
    value: "blobs",
    label: "Blobs only",
    description: "Delete orphaned blobs; manifests untouched.",
    destructive: true,
  },
  {
    value: "full",
    label: "Full sweep",
    description: "Mark-sweep manifests + blobs in one pass. The nightly cron's mode.",
    destructive: true,
  },
];

// ── Types ───────────────────────────────────────────────────────────────────

export interface GCStatus {
  last_run_id?: string;
  last_run_mode?: string;
  last_run_status?: string;
  last_run_started_at?: string;
  last_run_completed_at?: string;
  last_run_duration_ms: number;
  last_run_blobs_freed: number;
  last_run_manifests_deleted: number;
  last_run_bytes_freed: number;
  last_run_error?: string;
  last_run_triggered_by?: string;
  next_scheduled_at?: string;
}

export interface GCRun {
  run_id: string;
  mode: string;
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

export interface GCRunsListResponse {
  runs: GCRun[];
  next_page_token: string;
}

export interface GCRunNowResponse {
  run_id: string;
  status: string;
}

// ── Key factory ─────────────────────────────────────────────────────────────

export const adminGcKeys = {
  all: ["admin", "gc"] as const,
  status: () => [...adminGcKeys.all, "status"] as const,
  runs: (limit: number) => [...adminGcKeys.all, "runs", limit] as const,
};

// ── Hooks ───────────────────────────────────────────────────────────────────

// useGCStatus — 30s poll. Cheap query (one row), and the operator on this
// page wants to see "Run now" outcomes reflected without a manual refresh.
export function useGCStatus() {
  return useQuery({
    queryKey: adminGcKeys.status(),
    queryFn: async () => {
      const { data } = await apiClient.get<GCStatus>("/admin/gc/status");
      return data;
    },
    refetchInterval: 30_000,
    staleTime: 10_000,
  });
}

interface RunsArgs {
  limit?: number;
  // S-MAINT-1 F2 — server-side search filters. Empty/undefined values
  // produce a query with no filter, preserving the pre-F2 behaviour.
  // The BFF forwards these straight to gc.ListRunsRequest; validation
  // (RFC3339 on the timestamps) happens on the gc service side.
  triggeredBy?: string;
  dateFrom?: string; // RFC3339 lower bound (inclusive)
  dateTo?: string; // RFC3339 upper bound (exclusive)
}

// useGCRuns — paginated history. 10-row default keeps the card readable
// without a giant table; "Load more" exposes the rest. The S-MAINT-1 F2
// filter params widen the query key so a new filter triggers a refetch
// rather than reusing cached unfiltered rows.
export function useGCRuns({
  limit = 10,
  triggeredBy,
  dateFrom,
  dateTo,
}: RunsArgs = {}) {
  return useInfiniteQuery({
    // Filters are part of the key so cache entries don't bleed between
    // searches. Normalise undefined → "" so the key shape stays stable
    // across renders when the operator hasn't set a filter yet.
    queryKey: [
      ...adminGcKeys.runs(limit),
      triggeredBy ?? "",
      dateFrom ?? "",
      dateTo ?? "",
    ] as const,
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const params = new URLSearchParams();
      if (pageParam) params.set("page_token", String(pageParam));
      params.set("limit", String(limit));
      if (triggeredBy) params.set("triggered_by", triggeredBy);
      if (dateFrom) params.set("date_from", dateFrom);
      if (dateTo) params.set("date_to", dateTo);
      const { data } = await apiClient.get<GCRunsListResponse>(
        `/admin/gc/runs?${params.toString()}`,
      );
      return data;
    },
    getNextPageParam: (last) =>
      last.next_page_token ? last.next_page_token : undefined,
    staleTime: 15_000,
    // Poll every 10s ONLY while a run is still in a non-terminal state
    // (queued/running) — without this, a "Run now" row froze on its
    // enqueue-time snapshot until the operator manually refreshed. Once
    // every visible run is terminal we stop polling (false) so an idle
    // history table costs nothing. Mirrors useGCStatus's poll, but
    // conditional because the runs list is a heavier query.
    refetchInterval: (query) => {
      const hasActiveRun = query.state.data?.pages.some((page) =>
        page.runs.some(
          (r) => r.status === "queued" || r.status === "running",
        ),
      );
      return hasActiveRun ? 10_000 : false;
    },
  });
}

interface RunNowBody {
  mode: GCMode;
}

// useTriggerGCRun — enqueue a manual sweep. Invalidates both status + runs
// because the new row appears in the history *and* may immediately become
// the "last run" once the worker drains it.
export function useTriggerGCRun() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: RunNowBody): Promise<GCRunNowResponse> => {
      const { data } = await apiClient.post<GCRunNowResponse>(
        "/admin/gc/run",
        body,
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: adminGcKeys.status() });
      void qc.invalidateQueries({ queryKey: adminGcKeys.all });
    },
  });
}
