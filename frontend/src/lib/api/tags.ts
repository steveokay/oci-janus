import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";
import type { TagsListResponse } from "./types";

export const tagKeys = {
  all: ["tags"] as const,
  list: (org: string, repo: string) =>
    [...tagKeys.all, "list", org, repo] as const,
};

export function useTags(org: string, repo: string) {
  return useQuery({
    queryKey: tagKeys.list(org, repo),
    queryFn: async () => {
      const { data } = await apiClient.get<TagsListResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags`,
      );
      return data.tags;
    },
    staleTime: 15_000,
    enabled: Boolean(org && repo),
  });
}

interface DeleteTagArgs {
  org: string;
  repo: string;
  tag: string;
}

export function useDeleteTag() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ org, repo, tag }: DeleteTagArgs) => {
      await apiClient.delete(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}`,
      );
    },
    onSuccess: (_, { org, repo }) => {
      void qc.invalidateQueries({ queryKey: tagKeys.list(org, repo) });
    },
  });
}

// FE-API-036 — bulk tag delete.
//
// The BFF performs per-tag sub-transactions and returns a per-tag result
// (deleted: bool, reason?: string) so we can show "deleted 47/50, 3
// failed" instead of an all-or-nothing toast. Capped 100 tags per
// request server-side; we mirror that on the client so the UI never
// builds a request the server will reject up front.

export interface BulkDeleteResult {
  tag_name: string;
  deleted: boolean;
  reason?: string;
}

interface BulkDeleteResponse {
  results: BulkDeleteResult[];
}

interface BulkDeleteArgs {
  org: string;
  repo: string;
  tagNames: string[];
}

export const BULK_DELETE_MAX = 100;

export function useBulkDeleteTags() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      org,
      repo,
      tagNames,
    }: BulkDeleteArgs): Promise<BulkDeleteResult[]> => {
      const { data } = await apiClient.delete<BulkDeleteResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags`,
        { data: { tag_names: tagNames } },
      );
      return data.results;
    },
    onSuccess: (_, { org, repo }) => {
      void qc.invalidateQueries({ queryKey: tagKeys.list(org, repo) });
    },
  });
}
