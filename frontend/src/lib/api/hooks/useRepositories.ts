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

/**
 * GET /api/v1/repositories/{org}/{repo} — single repository fetch.
 * Used by the detail route to render the repo header without
 * round-tripping through the full list.
 */
export function useRepository(org: string, repo: string) {
  return useQuery({
    queryKey: ['repository', org, repo],
    queryFn: async () => {
      const { data } = await apiClient.get<RepoResponse>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}`,
      )
      return data
    },
    enabled: !!org && !!repo,
  })
}

/** One tag on a repository. Shape mirrors Go `TagResponse`. */
export interface TagResponse {
  name: string
  manifest_digest: string
  updated_at: string
  created_at: string
}

/**
 * GET /api/v1/repositories/{org}/{repo}/tags — paged tag list (server
 * caps at 100 per page). Sprint 1d only paints the first page; a
 * page-token paginator wires into the response when tag counts get
 * large in real workspaces.
 */
export function useTags(org: string, repo: string) {
  return useQuery({
    queryKey: ['tags', org, repo],
    queryFn: async () => {
      const { data } = await apiClient.get<{ tags: TagResponse[] }>(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags`,
      )
      return data.tags
    },
    enabled: !!org && !!repo,
  })
}

/**
 * GET /api/v1/repositories/{org}/{repo}/tags/{tag}/scan — latest scan
 * result for a tag.
 *
 * 404 means "no scan has been run yet" (vs a real error), so the hook
 * lifts that into a distinct `notScanned` flag rather than treating it
 * as a query failure. Callers (the per-tag badge) render a neutral
 * "Not scanned" pill in that case.
 */
export interface ScanResponse {
  scan_id: string
  status: string
  scanner_name: string
  scanner_version: string
  severity_counts: Record<string, number>
  started_at: string
  completed_at: string
}

export function useScan(org: string, repo: string, tag: string) {
  return useQuery({
    queryKey: ['scan', org, repo, tag],
    queryFn: async () => {
      try {
        const { data } = await apiClient.get<ScanResponse>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}/scan`,
        )
        return { scanned: true as const, scan: data }
      } catch (err) {
        // axios is configured to reject on 4xx/5xx; AxiosError carries
        // response.status. We import lazily so the helper stays cheap.
        const { AxiosError } = await import('axios')
        if (err instanceof AxiosError && err.response?.status === 404) {
          return { scanned: false as const }
        }
        throw err
      }
    },
    enabled: !!org && !!repo && !!tag,
    // Scans don't change often — once a tag is scanned the result is
    // immutable until a new manifest is pushed (which would invalidate
    // the parent tag list and remount the cell anyway).
    staleTime: 5 * 60_000,
  })
}

/**
 * DELETE /api/v1/repositories/{org}/{repo}/tags/{tag}. Server validates
 * the writer-or-above role on the repo (PENTEST-002); a 403 surfaces
 * back as an Axios error and the dialog renders the matching toast.
 */
export function useDeleteTag(org: string, repo: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (tag: string) => {
      await apiClient.delete(
        `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}`,
      )
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['tags', org, repo] })
      qc.invalidateQueries({ queryKey: ['repository', org, repo] })
      // Storage usage on the parent repo + global stats may shift —
      // re-poll those too so the dashboard tile stays in sync.
      qc.invalidateQueries({ queryKey: ['repositories'] })
      qc.invalidateQueries({ queryKey: ['stats'] })
    },
  })
}
