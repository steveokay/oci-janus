import axios from 'axios'
import { useAuthStore } from '@/store/authStore'

export const apiClient = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL ?? '/api/v1',
  timeout: 30_000,
  headers: { 'Content-Type': 'application/json' },
})

/** Attach the in-memory JWT to every outgoing request. */
apiClient.interceptors.request.use((config) => {
  const token = useAuthStore.getState().token
  if (token) config.headers.Authorization = `Bearer ${token}`
  return config
})

/**
 * On 401: clear auth state from the Zustand store and redirect to /login.
 * We use window.location rather than TanStack Router navigate() here because
 * this interceptor lives outside any React component, so the router context
 * is unavailable. The hard navigation also serves as a full memory wipe of
 * any in-flight query cache that might contain sensitive data.
 */
apiClient.interceptors.response.use(
  (res) => res,
  (err) => {
    if (err.response?.status === 401) {
      useAuthStore.getState().clearAuth()
      window.location.href = '/login'
    }
    return Promise.reject(err)
  }
)
