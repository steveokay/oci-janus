import { useInfiniteQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — Remediation suggestions (FE-API-017).
//
// Backend rolls up open CVEs by upgrade path: each row keys on
// (package_name, from_version, to_version) and lists the CVEs that the
// upgrade closes plus the affected (repo, tag, digest) triples.
//
// Why a separate hook (vs. piggy-backing on useVulnerabilities): the wire
// shape is different (no `cve_id` at the row level, but a `cves_fixed[]`
// list), the sort key prioritises "impact per upgrade" rather than
// severity, and the response is paginated under a different opaque cursor.
// Keeping the two hooks side-by-side makes the difference legible.
//
// Pagination: opaque page_token (base64 of the 5-tuple cursor on the
// server) — useInfiniteQuery so "Load more" is one button click.

export interface RemediationAffected {
  repo: string;
  tag: string;
  digest: string;
}

export interface Remediation {
  package_name: string;
  from_version: string;
  to_version: string;
  max_severity: string; // CRITICAL / HIGH / MEDIUM / LOW
  cves_fixed: string[]; // CVE-IDs the upgrade closes
  cves_fixed_count: number;
  // `affected[]` is capped at 10 server-side; `affected_count` is the true
  // total so the UI can render "showing 10 of 47" when capped.
  affected: RemediationAffected[];
  affected_count: number;
}

export interface RemediationsListResponse {
  remediations: Remediation[];
  next_page_token: string;
}

interface RemediationArgs {
  limit?: number;
}

export const remediationKeys = {
  all: ["security", "remediation"] as const,
  list: (limit: number) => [...remediationKeys.all, "list", limit] as const,
};

// useRemediations — same shape + staleTime as useVulnerabilities so the
// two tabs feel consistent when switched back-to-back.
export function useRemediations({ limit = 50 }: RemediationArgs = {}) {
  return useInfiniteQuery({
    queryKey: remediationKeys.list(limit),
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const params = new URLSearchParams();
      if (pageParam) params.set("page_token", String(pageParam));
      params.set("limit", String(limit));
      const { data } = await apiClient.get<RemediationsListResponse>(
        `/security/remediation?${params.toString()}`,
      );
      return data;
    },
    getNextPageParam: (last) =>
      last.next_page_token ? last.next_page_token : undefined,
    staleTime: 30_000,
  });
}
