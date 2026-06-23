import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";
import type { TrustedKey } from "./types";

// TanStack Query surface for futures.md Tier 1 #3 Phase 2 — per-repo
// trusted-key allowlist. Separate module from repositories.ts because
// the keys are a sibling collection of the repo (their CRUD doesn't
// invalidate `repoKeys.detail`) and keeping them apart makes the
// repo hooks file lighter.

export const trustedKeyKeys = {
  all: ["trusted-keys"] as const,
  list: (org: string, repo: string) =>
    [...trustedKeyKeys.all, "list", org, repo] as const,
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
