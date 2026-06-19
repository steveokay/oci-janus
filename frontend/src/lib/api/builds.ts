import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";
import type { BuildsListResponse } from "./types";

export const buildKeys = {
  all: ["builds"] as const,
  list: (org: string, repo: string, tag: string) =>
    [...buildKeys.all, "list", org, repo, tag] as const,
};

export function useBuilds(org: string, repo: string, tag: string) {
  return useQuery({
    queryKey: buildKeys.list(org, repo, tag),
    queryFn: async () => {
      const { data } = await apiClient.get<BuildsListResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/builds`,
      );
      return data;
    },
    staleTime: 30_000,
    enabled: Boolean(org && repo && tag),
  });
}
