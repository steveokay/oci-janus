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
 *
 * isRedirecting prevents multiple concurrent 401 responses from firing
 * multiple redirects. It is reset on popstate so re-entering the app after
 * navigating back works correctly.
 */
let isRedirecting = false

apiClient.interceptors.response.use(
  (res) => res,
  (err) => {
    if (err.response?.status === 401 && !isRedirecting) {
      isRedirecting = true
      useAuthStore.getState().clearAuth()
      // Append reason so the login page can display a session-expired banner.
      window.location.href = '/login?reason=session_expired'
    }
    return Promise.reject(err)
  }
)

// Reset the flag when the user navigates back so future 401s redirect again.
window.addEventListener('popstate', () => {
  isRedirecting = false
})
