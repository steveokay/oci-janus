import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";

// Beacon — Compliance reports (FE-API-019).
//
// Backend routes (all under /api/v1/security/reports — 404 when
// SCANNER_GRPC_ADDR is unset):
//   POST /generate                  → { report_id, status: "pending" }
//   GET  /                          → paginated list (per_page + page_token)
//   GET  /{id}                      → single report record
//   GET  /{id}/download/pdf         → file stream
//   GET  /{id}/download/sbom        → file stream (SPDX 2.3 JSON)

// ── Types ───────────────────────────────────────────────────────────────────

// ReportStatus tracks the async pipeline. `pending` (in queue) and
// `running` (claimed by a worker) are the in-flight states we poll on;
// `succeeded` and `failed` are terminal.
export type ReportStatus = "pending" | "running" | "succeeded" | "failed";

export interface ComplianceReport {
  report_id: string;
  tenant_id: string;
  requested_by: string;
  // RFC3339; omitted when the field is the zero value server-side.
  requested_at?: string;
  started_at?: string;
  completed_at?: string;
  status: string; // ReportStatus on the happy path, untyped to forward-compat
  error_message?: string;
  // Backend exposes these as URL/path hints. The actual download flow uses
  // /reports/{id}/download/{pdf|sbom} regardless — the strings here are
  // just an indication that the artifact exists.
  download_pdf_url?: string;
  download_sbom_url?: string;
}

export interface ReportsListResponse {
  reports: ComplianceReport[];
  next_page_token: string;
}

export interface GenerateReportResponse {
  report_id: string;
  status: string;
}

export type ReportFormat = "pdf" | "sbom";

// ── Key factory ─────────────────────────────────────────────────────────────

export const reportKeys = {
  all: ["compliance-reports"] as const,
  list: (perPage: number) => [...reportKeys.all, "list", perPage] as const,
  detail: (id: string) => [...reportKeys.all, "detail", id] as const,
};

// ── Hooks ───────────────────────────────────────────────────────────────────

interface ReportsArgs {
  perPage?: number;
  // Polling cadence — overridden by the panel when at least one report
  // row is still in-flight. `false` disables polling.
  refetchInterval?: number | false;
}

// useReports — paginated list. We force the BFF page_size to 50 by default
// so the panel can comfortably show the "last week" of reports without a
// click. Polling is opt-in via `refetchInterval`.
export function useReports({
  perPage = 50,
  refetchInterval = false,
}: ReportsArgs = {}) {
  return useInfiniteQuery({
    queryKey: reportKeys.list(perPage),
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const params = new URLSearchParams();
      if (pageParam) params.set("page_token", String(pageParam));
      params.set("per_page", String(perPage));
      const { data } = await apiClient.get<ReportsListResponse>(
        `/security/reports?${params.toString()}`,
      );
      return data;
    },
    getNextPageParam: (last) =>
      last.next_page_token ? last.next_page_token : undefined,
    refetchInterval,
    staleTime: 10_000,
  });
}

// useReport — single report. Useful for a future detail panel; not used
// directly by reports-panel today (the list response carries everything).
export function useReport(id: string | undefined) {
  return useQuery({
    queryKey: reportKeys.detail(id ?? ""),
    enabled: Boolean(id),
    queryFn: async () => {
      const { data } = await apiClient.get<ComplianceReport>(
        `/security/reports/${encodeURIComponent(id as string)}`,
      );
      return data;
    },
    staleTime: 10_000,
  });
}

// useGenerateReport — POST /generate. On success we invalidate the list
// so the new `pending` row appears immediately; the panel's own polling
// will then carry it through running → succeeded.
export function useGenerateReport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (): Promise<GenerateReportResponse> => {
      const { data } = await apiClient.post<GenerateReportResponse>(
        "/security/reports/generate",
        {},
      );
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: reportKeys.all });
    },
  });
}

interface DownloadError {
  status: "not-ready" | "not-found" | "network";
  message: string;
}

// useDownloadReport — fetches the artifact as a blob and triggers a
// browser download. Mirrors useDownloadSbom (sbom.ts) — same blob → object
// URL → anchor click flow so the user never sees a navigation.
//
// Error mapping:
//   404 → not-found    (no such report id, or wrong tenant)
//   409 → not-ready    (scanner returned a row but status != "succeeded")
//   *   → network      (catch-all)
export function useDownloadReport() {
  return useMutation<
    void,
    DownloadError,
    { id: string; format: ReportFormat }
  >({
    mutationFn: async ({ id, format }) => {
      try {
        const res = await apiClient.get<Blob>(
          `/security/reports/${encodeURIComponent(id)}/download/${format}`,
          { responseType: "blob" },
        );
        const ext = format === "pdf" ? ".pdf" : ".spdx.json";
        const blob = res.data;
        const url = window.URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = `compliance-report-${id}${ext}`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        window.setTimeout(() => window.URL.revokeObjectURL(url), 1_000);
      } catch (e) {
        if (e instanceof AxiosError) {
          const s = e.response?.status;
          if (s === 404) {
            throw {
              status: "not-found",
              message: "Report not found — it may have been pruned.",
            } satisfies DownloadError;
          }
          if (s === 409) {
            throw {
              status: "not-ready",
              message:
                "Report isn't ready yet — wait for the row to flip to succeeded.",
            } satisfies DownloadError;
          }
        }
        throw {
          status: "network",
          message: "Couldn't download the report. Check the BFF logs.",
        } satisfies DownloadError;
      }
    },
  });
}

// isInFlight — single source of truth for "this row is still being
// produced". Shared between the polling gate and the per-row UI so the
// two never disagree.
export function isInFlight(status: string): boolean {
  return status === "pending" || status === "running";
}
