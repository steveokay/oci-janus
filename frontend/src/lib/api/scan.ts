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
// `useScan` polls while the scan is pending/running so the UI updates without
// the operator hitting refresh. The poll interval drops back to undefined
// once the status terminates.

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
      // Poll every 4s while a scan is in-flight; idle otherwise.
      return status === "pending" || status === "running" ? 4_000 : false;
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
      // Force an immediate refetch — the poll will then catch the transition
      // to running → complete without us reaching back in.
      void qc.invalidateQueries({ queryKey: scanKeys.result(org, repo, tag) });
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
