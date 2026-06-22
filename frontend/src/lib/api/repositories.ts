import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { apiClient } from "./client";
import type {
  CreateRepositoryBody,
  RepositoriesListResponse,
  Repository,
} from "./types";

// Keyfactory pattern — every hook key is namespaced under `repositories` so a
// single `invalidateQueries({ queryKey: repoKeys.all })` fans out cleanly
// after any mutation.
export const repoKeys = {
  all: ["repositories"] as const,
  list: (visibility: "public" | "private" | "all", artifactType: RepoArtifactFilter) =>
    [...repoKeys.all, "list", visibility, artifactType] as const,
  detail: (org: string, repo: string) =>
    [...repoKeys.all, "detail", org, repo] as const,
};

export type RepoVisibilityFilter = "public" | "private" | "all";

// F4 follow-up — narrow the list to repositories holding at least one
// manifest of the given artifact_type. "all" disables the filter and
// returns every repo in the workspace. The values mirror what
// services/metadata's deriveArtifactType emits (and the BFF allowlist
// accepts) — `image`, `helm`, `signature`, `sbom`, `other`.
export type RepoArtifactFilter =
  | "all"
  | "image"
  | "helm"
  | "signature"
  | "sbom"
  | "other";

interface UseRepositoriesParams {
  visibility?: RepoVisibilityFilter;
  artifactType?: RepoArtifactFilter;
  perPage?: number;
}

// Cursor pagination — the management API hands back `next_page_token` only
// when there is more to fetch. `useInfiniteQuery` is the natural fit since
// the response shape doesn't easily collapse to offset paging.
export function useRepositories({
  visibility = "all",
  artifactType = "all",
  perPage = 25,
}: UseRepositoriesParams = {}) {
  return useInfiniteQuery({
    queryKey: repoKeys.list(visibility, artifactType),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const params: Record<string, string> = {
        per_page: String(perPage),
      };
      if (visibility !== "all") params.visibility = visibility;
      if (artifactType !== "all") params.artifact_type = artifactType;
      if (pageParam) params.page_token = pageParam;
      const { data } = await apiClient.get<RepositoriesListResponse>(
        "/repositories",
        { params },
      );
      return data;
    },
    getNextPageParam: (last) =>
      last.next_page_token ? last.next_page_token : undefined,
    staleTime: 15_000,
  });
}

export function useRepository(org: string, repo: string) {
  return useQuery({
    queryKey: repoKeys.detail(org, repo),
    queryFn: async () => {
      const { data } = await apiClient.get<Repository>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}`,
      );
      return data;
    },
    staleTime: 30_000,
    enabled: Boolean(org && repo),
  });
}

export function useCreateRepository() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateRepositoryBody) => {
      const { data } = await apiClient.post<Repository>("/repositories", body);
      return data;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: repoKeys.all });
    },
  });
}

interface DeleteRepoArgs {
  org: string;
  repo: string;
}

export function useDeleteRepository() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ org, repo }: DeleteRepoArgs) => {
      await apiClient.delete(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}`,
      );
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: repoKeys.all });
    },
  });
}
