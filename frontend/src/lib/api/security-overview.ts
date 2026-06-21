import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — Security overview snapshot (FE-API-020).
//
// One round-trip endpoint that powers the workspace-wide security tiles
// on /security → Overview. Backend runs a single three-CTE query so the
// payload is cheap to refresh; we still apply a 60s staleTime because
// the numbers don't move fast enough to warrant aggressive polling.
//
//   GET /api/v1/security/overview
//
// `days_since_last_scan: -1` is the sentinel for "never scanned in this
// tenant". The consuming card maps this to a "Never" pill rather than
// rendering -1.

export interface SeverityCounts {
  CRITICAL: number;
  HIGH: number;
  MEDIUM: number;
  LOW: number;
  NEGLIGIBLE: number;
}

export interface ScanCoverage {
  tags_total: number;
  tags_scanned: number;
  // Server-computed percent (0-100). We render the value verbatim so the
  // card stays consistent with the backend's rounding rules.
  percent: number;
}

export interface SecurityOverview {
  open_vulnerabilities_total: number;
  severity_counts: SeverityCounts;
  scan_coverage: ScanCoverage;
  recent_scans_24h: number;
  // -1 sentinel = "no scans recorded for this tenant".
  days_since_last_scan: number;
}

export const securityOverviewKeys = {
  all: ["security", "overview"] as const,
};

export function useSecurityOverview() {
  return useQuery({
    queryKey: securityOverviewKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<SecurityOverview>(
        "/security/overview",
      );
      return data;
    },
    staleTime: 60_000,
  });
}
