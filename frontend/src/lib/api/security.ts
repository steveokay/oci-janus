import { useInfiniteQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Workspace-wide security data — FE-API-014 (vulnerabilities) and
// FE-API-015 (scan history).
//
// Both endpoints are page_token paginated with a stable 50-row default and
// a hard 200 cap. We use `useInfiniteQuery` so the UI can "Load more"
// without us having to thread cursors through component state.
//
// Tenant isolation: BFF reads tenant_id from the auth context — we never
// send it on the wire.

// ── Vulnerabilities (FE-API-014) ────────────────────────────────────────────

export type Severity =
  | "CRITICAL"
  | "HIGH"
  | "MEDIUM"
  | "LOW"
  | "NEGLIGIBLE";

export const SEVERITIES: Severity[] = [
  "CRITICAL",
  "HIGH",
  "MEDIUM",
  "LOW",
  "NEGLIGIBLE",
];

export interface AffectedTag {
  repo: string;
  tag: string;
  digest: string;
}

export interface Vulnerability {
  cve_id: string;
  severity: string;
  title: string;
  description: string;
  fixed_in: string;
  package_name: string;
  package_version: string;
  affected: AffectedTag[];
  first_seen: string;
  last_seen: string;
}

export interface VulnerabilitiesListResponse {
  vulnerabilities: Vulnerability[];
  next_page_token: string;
}

interface VulnerabilitiesArgs {
  severity?: Severity | "";
  limit?: number;
}

export const securityKeys = {
  vulns: (severity: string) => ["security", "vulns", severity] as const,
  scans: (since: string) => ["security", "scans", since] as const,
};

export function useVulnerabilities({
  severity = "",
  limit = 50,
}: VulnerabilitiesArgs = {}) {
  return useInfiniteQuery({
    queryKey: securityKeys.vulns(severity),
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const params = new URLSearchParams();
      if (severity) params.set("severity", severity);
      if (pageParam) params.set("page_token", String(pageParam));
      params.set("limit", String(limit));
      const { data } = await apiClient.get<VulnerabilitiesListResponse>(
        `/security/vulnerabilities?${params.toString()}`,
      );
      return data;
    },
    getNextPageParam: (last) =>
      last.next_page_token ? last.next_page_token : undefined,
    staleTime: 30_000,
  });
}

// ── Scan history (FE-API-015) ───────────────────────────────────────────────

export type ScanStatus = "complete" | "running" | "pending" | "failed";
export type ScanTrigger = "push" | "manual" | "scheduled";

export interface ScanSeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  negligible: number;
}

export interface ScanHistoryEntry {
  scan_id: string;
  repo: string;
  tag: string;
  manifest_digest: string;
  scanner: string;
  started_at: string;
  // completed_at is a nullable timestamp — pending/running rows omit it.
  completed_at?: string | null;
  status: string;
  severity_counts: ScanSeverityCounts;
  trigger: string;
}

export interface ScanHistoryListResponse {
  scans: ScanHistoryEntry[];
  next_page_token: string;
}

interface ScanHistoryArgs {
  since?: string;
  limit?: number;
}

export function useScanHistory({
  since = "",
  limit = 50,
}: ScanHistoryArgs = {}) {
  return useInfiniteQuery({
    queryKey: securityKeys.scans(since),
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const params = new URLSearchParams();
      if (since) params.set("since", since);
      if (pageParam) params.set("page_token", String(pageParam));
      params.set("limit", String(limit));
      const { data } = await apiClient.get<ScanHistoryListResponse>(
        `/security/scans?${params.toString()}`,
      );
      return data;
    },
    getNextPageParam: (last) =>
      last.next_page_token ? last.next_page_token : undefined,
    staleTime: 15_000,
  });
}

// Map backend lowercase severity_counts → the SeverityBar's uppercase keys.
// SeverityBar already exists and consumes Partial<Record<"CRITICAL"...>>;
// this keeps the scan-history rows compatible with it.
export function toSeverityBarCounts(c: ScanSeverityCounts | null | undefined): {
  CRITICAL: number;
  HIGH: number;
  MEDIUM: number;
  LOW: number;
} {
  // Backend may return null severity_counts on pending / failed scans —
  // collapse to all-zero so callers (e.g. ScanRow's `.CRITICAL + .HIGH + ...`)
  // never crash on a null read.
  if (!c) return { CRITICAL: 0, HIGH: 0, MEDIUM: 0, LOW: 0 };
  return {
    CRITICAL: c.critical ?? 0,
    HIGH: c.high ?? 0,
    MEDIUM: c.medium ?? 0,
    LOW: c.low ?? 0,
  };
}

// severityTone — mapping a severity string to the Badge `tone` so a single
// helper handles the table cell. Returns 'neutral' for unknowns so a
// future severity rename doesn't crash the page.
export function severityTone(
  severity: string,
): "critical" | "high" | "medium" | "low" | "neutral" {
  switch (severity.toUpperCase()) {
    case "CRITICAL":
      return "critical";
    case "HIGH":
      return "high";
    case "MEDIUM":
      return "medium";
    case "LOW":
    case "NEGLIGIBLE":
      return "low";
    default:
      return "neutral";
  }
}
