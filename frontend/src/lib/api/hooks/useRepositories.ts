/**
 * Repository hooks — list / create / delete against /api/v1/repositories.
 *
 * Shape mirrors the Go `RepoResponse` + `createRepositoryBody` types in
 * `services/management/internal/handler/handler.go`. There's no codegen,
 * so renaming a field on either side requires a matching change here.
 *
 * Validation regexes mirror the server's allowlists (CLAUDE.md §7) — we
 * surface validation errors client-side via react-hook-form + zod so the
 * dialog can complain before round-tripping to the API. The server is
 * still the authority and will reject anything the client misses.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '@/lib/api/client'

export interface RepoResponse {
  repo_id: string
  org_id: string
  /** "org/repo" form — the canonical full name. */
  name: string
  is_public: boolean
  storage_used_bytes: number
  storage_quota_bytes: number
  created_at: string
}

export interface ListRepositoriesResponse {
  repositories: RepoResponse[]
  total: number
}

export type VisibilityFilter = 'public' | 'private' | 'all'

export function useRepositories(visibility: VisibilityFilter = 'all') {
  return useQuery({
    queryKey: ['repositories', visibility],
    queryFn: async () => {
      // The server-side filter is server-applied; we use it instead of
      // pulling everything and filtering client-side so wide tenants
      // don't pay for unnecessary rows.
      const params: Record<string, string | number> = { per_page: 100 }
      if (visibility !== 'all') params.visibility = visibility
      const { data } = await apiClient.get<ListRepositoriesResponse>(
        '/repositories',
        { params },
      )
      return data
    },
  })
}

export interface CreateRepositoryInput {
  org: string
  name: string
  is_public: boolean
  /** Bytes; 0 uses the metadata default (10 GB). */
  storage_quota?: number
}

export function useCreateRepository() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: CreateRepositoryInput) => {
      const { data } = await apiClient.post<RepoResponse>('/repositories', {
        org: input.org,
        name: input.name,
        is_public: input.is_public,
        storage_quota: input.storage_quota ?? 0,
      })
      return data
    },
    onSuccess: () => {
      // Invalidate every variant of the list query so all open
      // visibility filters refetch.
      qc.invalidateQueries({ queryKey: ['repositories'] })
      // The stats card pulls /api/v1/stats.total_repos — refresh it too.
      qc.invalidateQueries({ queryKey: ['stats'] })
    },
  })
}

export function useDeleteRepository() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (fullName: string) => {
      // fullName is "org/repo" form — split and validate before sending.
      const [org, repo] = fullName.split('/', 2)
      if (!org || !repo) throw new Error(`invalid repo name: ${fullName}`)
      await apiClient.delete(`/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}`)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['repositories'] })
      qc.invalidateQueries({ queryKey: ['stats'] })
    },
  })
}

// Validation patterns mirror services/management/internal/handler/validate.go.
// Keep these in sync with that file when patterns change there.
export const ORG_NAME_PATTERN = /^[a-z0-9-]{2,64}$/
export const REPO_NAME_PATTERN = /^[a-z0-9]+([._-][a-z0-9]+)*$/
export const REPO_NAME_MAX = 128
