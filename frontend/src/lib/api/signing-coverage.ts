import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — workspace signing coverage rollup (futures.md "Signing coverage
// rollup"). Read-only aggregate powering the Security → Signing tab. The fetch
// is split out as a plain async fn so it can be unit-tested without a
// QueryClient (see signing-coverage.test.ts), mirroring auth.ts.

// allowlist_health classifies a repo's signing enforcement posture:
//   enforced_with_allowlist — require_signature on + a non-empty trusted-key
//     allowlist (the strong posture).
//   enforced_any_signature  — require_signature on but the allowlist is empty
//     (any valid signature passes; weaker than the above).
//   advisory                — require_signature off; signing is tracked but not
//     enforced.
export type AllowlistHealth =
  | "enforced_with_allowlist"
  | "enforced_any_signature"
  | "advisory";

// CoverageSigner — one recent signer surfaced per repo. Mirrors the BFF's
// recentSignerEntry wire shape (see trusted-keys.ts RecentSigner).
export interface CoverageSigner {
  key_id: string;
  signer_id?: string;
  last_signed_at: string;
  tag_count: number;
}

// RepoCoverage — per-repo row in the rollup. signed_pct is a fraction in
// [0,1] over the most recent `window` tags.
export interface RepoCoverage {
  org: string;
  repo: string;
  require_signature: boolean;
  window: number;
  tags_in_window: number;
  signed_tags: number;
  signed_pct: number;
  trusted_key_count: number;
  allowlist_health: AllowlistHealth;
  stale_trusted_keys: number;
  recent_signers: CoverageSigner[];
}

// SigningCoverageSummary — workspace-wide aggregate across all repos.
// workspace_signed_tag_pct is a fraction in [0,1].
export interface SigningCoverageSummary {
  repo_count: number;
  repos_require_signature: number;
  repos_enforced_empty_allowlist: number;
  workspace_signed_tag_pct: number;
}

// SigningCoverage — the full response body from GET /signing/coverage.
export interface SigningCoverage {
  window: number;
  signer_enabled: boolean;
  summary: SigningCoverageSummary;
  repos: RepoCoverage[];
}

export const signingCoverageKeys = {
  all: ["signing-coverage"] as const,
  rollup: (window: number) => [...signingCoverageKeys.all, window] as const,
};

// fetchSigningCoverage — plain async fetch so it can be unit-tested without a
// QueryClient. `window` is the number of most-recent tags per repo to consider.
export async function fetchSigningCoverage(window: number): Promise<SigningCoverage> {
  const { data } = await apiClient.get<SigningCoverage>("/signing/coverage", {
    params: { window },
  });
  return data;
}

// useSigningCoverage — TanStack Query hook powering the Security → Signing tab.
// 60s freshness window: the rollup is an operator-facing aggregate that doesn't
// need to be second-fresh, so a 60s cache keeps tab re-visits snappy.
export function useSigningCoverage(window = 50) {
  return useQuery({
    queryKey: signingCoverageKeys.rollup(window),
    queryFn: () => fetchSigningCoverage(window),
    staleTime: 60_000,
  });
}
