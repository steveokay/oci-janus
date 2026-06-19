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
