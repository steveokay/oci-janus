import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { AxiosError } from "axios";
import { apiClient } from "./client";
import type { ScanResult } from "./types";

// Beacon — vulnerability scan hooks.
//
// Polling model: `useScan` polls every 4s while a scan is `pending` or
// `running`. It ALSO polls briefly when the server returns 404 ("no scan
// row yet") because two flows can land us there with a scan actually
// running in the background:
//
//   1. The operator just clicked "Trigger scan" — useTriggerScan writes
//      an optimistic `pending` stub so polling kicks in immediately.
//
//   2. The operator navigated to a freshly-pushed tag and registry-scanner
//      is auto-scanning via the push.completed consumer. We don't know
//      one is running, but it's cheap to poll for the first ~30s after
//      mount and very expensive (no result visible at all) to skip.
//
// After the null-poll window expires we stop refetching so a tag that is
// genuinely never going to be scanned doesn't hammer the BFF forever.
//
// PENDING_STATUSES lists the wire values that mean "still working". Kept
// here so both the hook and the trigger optimistic stub agree on what
// counts as in-flight — drift between them produces a stuck-pending UI.
const PENDING_STATUSES: ReadonlyArray<ScanResult["status"]> = [
  "pending",
  "running",
];

const NULL_POLL_WINDOW_MS = 30_000;
const POLL_INTERVAL_MS = 4_000;

export const scanKeys = {
  all: ["scan"] as const,
  result: (org: string, repo: string, tag: string) =>
    [...scanKeys.all, "result", org, repo, tag] as const,
};

export function useScan(org: string, repo: string, tag: string) {
  return useQuery({
    queryKey: scanKeys.result(org, repo, tag),
    queryFn: async () => {
      try {
        const { data } = await apiClient.get<ScanResult>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/scan`,
        );
        return data;
      } catch (e) {
        // 404 → no scan yet. Return null and let the UI render the
        // "trigger a scan" CTA instead of an error state.
        if (e instanceof AxiosError && e.response?.status === 404) {
          return null;
        }
        throw e;
      }
    },
    refetchInterval: (q) => {
      const status = q.state.data?.status;
      if (status && PENDING_STATUSES.includes(status)) {
        return POLL_INTERVAL_MS;
      }
      // Null result + recent mount → keep polling so a background scan
      // (push.completed auto-trigger, or a trigger fired in another tab)
      // surfaces without a manual refresh.
      if (q.state.data === null) {
        const sinceMount = Date.now() - q.state.dataUpdatedAt;
        if (sinceMount < NULL_POLL_WINDOW_MS) {
          return POLL_INTERVAL_MS;
        }
      }
      return false;
    },
    staleTime: 5_000,
    enabled: Boolean(org && repo && tag),
  });
}

interface TriggerArgs {
  org: string;
  repo: string;
  tag: string;
}

export function useTriggerScan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ org, repo, tag }: TriggerArgs) => {
      const { data } = await apiClient.post<{ status: string; message: string }>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/scan`,
        {},
      );
      return data;
    },
    onSuccess: (_, { org, repo, tag }) => {
      // Optimistic pending stub. Without this, `useScan` returns null
      // (the GET 404s because registry-scanner hasn't written the row
      // yet) and the refetchInterval gate would never flip to "polling"
      // — the operator would stare at the "no scan yet" CTA forever
      // even though the scanner is actively working. Once the scanner
      // writes the real row, the next refetch supersedes this stub.
      const key = scanKeys.result(org, repo, tag);
      const existing = qc.getQueryData<ScanResult | null>(key);
      // Preserve a real prior scan's metadata (scanner_name, version,
      // severity_counts) so a "Rescan" click doesn't blank the panel
      // — only flip the status + clear completed_at so the in-flight
      // card renders.
      const stub: ScanResult = {
        scan_id: existing?.scan_id ?? "pending",
        status: "pending",
        scanner_name: existing?.scanner_name ?? "",
        scanner_version: existing?.scanner_version ?? "",
        severity_counts: existing?.severity_counts ?? {},
        findings_json: existing?.findings_json,
        started_at: new Date().toISOString(),
        completed_at: undefined,
      };
      qc.setQueryData(key, stub);
      // Still kick a refetch in case the row landed faster than the
      // optimistic write (rare, but the network round-trip leaves room).
      void qc.invalidateQueries({ queryKey: key });
    },
  });
}

// ── Severity helpers ────────────────────────────────────────────────────────

export const SEVERITY_ORDER = ["CRITICAL", "HIGH", "MEDIUM", "LOW"] as const;
export type SeverityKey = (typeof SEVERITY_ORDER)[number];

export function totalSeverityCount(
  counts: ScanResult["severity_counts"] | undefined,
): number {
  if (!counts) return 0;
  return SEVERITY_ORDER.reduce((sum, k) => sum + (counts[k] ?? 0), 0);
}

// Trivy findings_json is an array of JSON-serialised findings. We don't make
// schema assumptions beyond what the backend currently emits — fields are
// optional so this stays forward-compatible.
export interface ScanFinding {
  vulnerability_id?: string;
  severity?: string;
  package_name?: string;
  installed_version?: string;
  fixed_version?: string;
  title?: string;
  description?: string;
  primary_url?: string;
}

export function parseFindings(raw?: string): ScanFinding[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? (parsed as ScanFinding[]) : [];
  } catch {
    return [];
  }
}
