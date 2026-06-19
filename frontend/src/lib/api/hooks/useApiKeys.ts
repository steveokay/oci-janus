/**
 * API key hooks — list / create / revoke against /api/v1/apikeys.
 *
 * Shape mirrors `apiKeyResponse` in
 * `services/auth/internal/handler/http.go`. On `create`, the response
 * includes `key` — the raw secret, returned exactly once and never
 * recoverable afterwards. Callers MUST surface that value to the user
 * (one-time secret dialog) before the dialog closes; the list endpoint
 * never returns it again.
 *
 * Routing note: vite.config.ts proxies `/api/v1/apikeys` to the auth
 * service (`:8080`), not management. The shared `apiClient` handles
 * the prefix automatically via its `baseURL`.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '@/lib/api/client'

export interface ApiKey {
  id: string
  name: string
  /** Human-readable prefix (e.g. "janus_") that helps the user spot which
      raw key in their secrets manager maps to which row here. */
  prefix: string
  scopes: string[]
  expires_at: string | null
  created_at: string
}

export interface CreatedApiKey extends ApiKey {
  /** Raw secret. Only populated on the create response. Never persisted. */
  key: string
}

export function useApiKeys() {
  return useQuery({
    queryKey: ['apiKeys'],
    queryFn: async () => {
      const { data } = await apiClient.get<ApiKey[]>('/apikeys')
      // Defensive — auth handler returns null on no rows, not [].
      return Array.isArray(data) ? data : []
    },
  })
}

export interface CreateApiKeyInput {
  name: string
  scopes: string[]
  /** ISO-8601 string. Optional — keys without an expiry never expire. */
  expires_at?: string
}

export function useCreateApiKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: CreateApiKeyInput) => {
      const { data } = await apiClient.post<CreatedApiKey>('/apikeys', {
        name: input.name,
        scopes: input.scopes,
        expires_at: input.expires_at,
      })
      return data
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['apiKeys'] })
    },
  })
}

export function useDeleteApiKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: string) => {
      await apiClient.delete(`/apikeys/${encodeURIComponent(id)}`)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['apiKeys'] })
    },
  })
}
