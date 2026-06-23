import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";
import type { TrustedKey } from "./types";

// TanStack Query surface for futures.md Tier 1 #3 Phase 2 — per-repo
// trusted-key allowlist. Separate module from repositories.ts because
// the keys are a sibling collection of the repo (their CRUD doesn't
// invalidate `repoKeys.detail`) and keeping them apart makes the
// repo hooks file lighter.
//
// Also hosts the recent-signers picker hook (2026-06-23 follow-up): the
// data is a BFF-orchestrated rollup over signer.ListSignatures, used
// only by the Approve dialog's "Pick from recent signers" mode. The
// hook lives here instead of in signature.ts because its consumer is
// the trusted-keys dialog and the query key shares the same root.

export const trustedKeyKeys = {
  all: ["trusted-keys"] as const,
  list: (org: string, repo: string) =>
    [...trustedKeyKeys.all, "list", org, repo] as const,
  recentSigners: (org: string, repo: string) =>
    [...trustedKeyKeys.all, "recent-signers", org, repo] as const,
};

interface TrustedKeysResponse {
  keys: TrustedKey[];
}

export function useTrustedKeys(org: string, repo: string) {
  return useQuery({
    queryKey: trustedKeyKeys.list(org, repo),
    queryFn: async () => {
      const { data } = await apiClient.get<TrustedKeysResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/trusted-keys`,
      );
      return data.keys ?? [];
    },
  });
}

interface AddTrustedKeyArgs {
  org: string;
  repo: string;
  key_id: string;
  display_name?: string;
}

export function useAddTrustedKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ org, repo, key_id, display_name }: AddTrustedKeyArgs): Promise<TrustedKey> => {
      const { data } = await apiClient.post<TrustedKey>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/trusted-keys`,
        { key_id, display_name },
      );
      return data;
    },
    onSuccess: (_, { org, repo }) => {
      void qc.invalidateQueries({ queryKey: trustedKeyKeys.list(org, repo) });
    },
  });
}

interface RemoveTrustedKeyArgs {
  org: string;
  repo: string;
  key_id: string;
}

export function useRemoveTrustedKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ org, repo, key_id }: RemoveTrustedKeyArgs): Promise<void> => {
      await apiClient.delete(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/trusted-keys/${encodeURIComponent(key_id)}`,
      );
    },
    onSuccess: (_, { org, repo }) => {
      void qc.invalidateQueries({ queryKey: trustedKeyKeys.list(org, repo) });
    },
  });
}

// RecentSigner — one entry returned by GET /recent-signers. Mirrors the
// BFF's recentSignerEntry wire shape. The fields are picker-hint
// metadata: `key_id` is what the operator approves, `signer_id`
// auto-fills the display_name input, `last_signed_at` drives the "Xm
// ago" relative-time line, and `tag_count` lets the operator sanity-
// check they're approving an actively-used key vs a one-shot accident.
export interface RecentSigner {
  key_id: string;
  signer_id?: string;
  last_signed_at: string;
  tag_count: number;
}

interface RecentSignersResponse {
  signers: RecentSigner[];
}

// useRecentSigners — BFF-orchestrated "recently signed in this repo" list
// powering the Approve dialog's "Pick from recent signers" mode.
//
// Cache window: 60s. The dialog opens infrequently and the data is a
// rollup over the most recent ~20 tags; a 60s cache keeps the dialog
// snappy on re-open without staleness pain (a key that just signed
// becomes pickable on the next minute boundary). `enabled` gates the
// fetch so the request only fires when the dialog is open — saves a
// per-page-mount call when the operator never opens the dialog.
export function useRecentSigners(
  org: string,
  repo: string,
  enabled: boolean,
) {
  return useQuery({
    queryKey: trustedKeyKeys.recentSigners(org, repo),
    queryFn: async (): Promise<RecentSigner[]> => {
      const { data } = await apiClient.get<RecentSignersResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/recent-signers`,
      );
      return data.signers ?? [];
    },
    // 60s freshness window — see comment block above.
    staleTime: 60_000,
    enabled,
  });
}
