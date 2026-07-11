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
  list: (
    visibility: "public" | "private" | "all",
    artifactType: RepoArtifactFilter,
    org?: string,
  ) => [...repoKeys.all, "list", visibility, artifactType, org ?? ""] as const,
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
  // Optional org scope — when set the BFF returns only repos in this org
  // (the per-environment /repositories/$org list). Absent means the flat
  // whole-catalogue view.
  org?: string;
  perPage?: number;
}

// Cursor pagination — the management API hands back `next_page_token` only
// when there is more to fetch. `useInfiniteQuery` is the natural fit since
// the response shape doesn't easily collapse to offset paging.
export function useRepositories({
  visibility = "all",
  artifactType = "all",
  org,
  perPage = 25,
}: UseRepositoriesParams = {}) {
  return useInfiniteQuery({
    queryKey: repoKeys.list(visibility, artifactType, org),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const params: Record<string, string> = {
        per_page: String(perPage),
      };
      if (visibility !== "all") params.visibility = visibility;
      if (artifactType !== "all") params.artifact_type = artifactType;
      if (org) params.org = org;
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

// PATCH /repositories/{org}/{repo} — updates mutable repository fields.
// Today: description + tag immutability flag (futures.md Tier 1 #2).
// Other fields (visibility, quota) live on dedicated routes for audit
// clarity.
//
// `immutable_tags` is encoded as an optional field (omitted from the
// JSON body when `undefined`) so a PATCH that touches only description
// doesn't accidentally turn immutability off — the BFF treats a nil
// pointer as "leave alone" vs a non-nil false as "explicit reset".
interface UpdateRepoArgs {
  org: string;
  repo: string;
  description?: string;
  immutable_tags?: boolean;
  // Signed-image admission (futures.md Tier 1 #3). Same "omit to leave
  // alone, send to flip" contract as `immutable_tags` — a missing key
  // never accidentally resets the security policy.
  require_signature?: boolean;
  // FUT-021 — CVSS admission threshold. Three-state field:
  //   - undefined  → omitted from the PATCH body (leave alone)
  //   - null       → clear the threshold (SQL NULL on the row)
  //   - integer    → set the threshold (0-100 CVSS band midpoints)
  // Sent as JSON `null` when the caller explicitly clears; `undefined`
  // is skipped by the Object.assign path below so the BFF receives no
  // key at all.
  max_cvss_score?: number | null;
}

export function useUpdateRepository() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      org,
      repo,
      description,
      immutable_tags,
      require_signature,
      max_cvss_score,
    }: UpdateRepoArgs): Promise<Repository> => {
      const body: Record<string, unknown> = {};
      if (description !== undefined) body.description = description;
      if (immutable_tags !== undefined) body.immutable_tags = immutable_tags;
      if (require_signature !== undefined) body.require_signature = require_signature;
      // FUT-021 — distinguish explicit `null` (clear the gate) from
      // `undefined` (leave alone). The `in` check preserves the
      // three-state contract on the wire.
      if (max_cvss_score !== undefined) body.max_cvss_score = max_cvss_score;
      const { data } = await apiClient.patch<Repository>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}`,
        body,
      );
      return data;
    },
    onSuccess: (_, { org, repo }) => {
      void qc.invalidateQueries({ queryKey: repoKeys.detail(org, repo) });
      void qc.invalidateQueries({ queryKey: repoKeys.all });
    },
  });
}
