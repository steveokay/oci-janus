/**
 * apiClient — single axios instance for the whole app.
 *
 * Why one instance:
 *   * One place to attach the Bearer token interceptor.
 *   * One place to detect 401s and bounce to /login (auto-expire UX).
 *   * Per-request configs still compose via the .get/.post second arg.
 *
 * The Vite dev proxy (vite.config.ts) forwards /api → auth (8080) +
 * management (8091) so this file doesn't care which host serves a path.
 * In production, nginx (frontend Dockerfile) does the same routing.
 */
import axios, { AxiosError } from 'axios'
import { useAuthStore } from '@/store/authStore'

export const apiClient = axios.create({
  baseURL: '/api/v1',
  // Reasonable upper bound — slowest backend RPC is a fresh tag listing on
  // a cold metadata cache, which still completes well under this.
  timeout: 15000,
})

// Request interceptor: attach the Bearer token if we have one.
apiClient.interceptors.request.use((config) => {
  const { token } = useAuthStore.getState()
  if (token) {
    config.headers.set('Authorization', `Bearer ${token}`)
  }
  return config
})

// Response interceptor: 401 → clear session.
// We DON'T auto-redirect here because the routing layer's auth guards
// re-derive the redirect target (login + `?from=<current>`); doing it
// here would race with TanStack Router's beforeLoad.
apiClient.interceptors.response.use(
  (resp) => resp,
  (err: AxiosError) => {
    if (err.response?.status === 401) {
      useAuthStore.getState().clearSession()
    }
    return Promise.reject(err)
  },
)
