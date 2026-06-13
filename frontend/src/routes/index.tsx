import { createFileRoute, redirect } from '@tanstack/react-router'
import { useAuthStore } from '@/store/authStore'

export const Route = createFileRoute('/')({
  /**
   * beforeLoad runs before the component renders, making it the right place
   * for auth-based redirects. Token lives in Zustand memory (FE-SEC-001/002).
   * Page reload clears the store, so returning users always land on /login.
   */
  beforeLoad: () => {
    if (useAuthStore.getState().isAuthenticated()) {
      throw redirect({ to: '/dashboard' })
    }
    throw redirect({ to: '/login' })
  },
})
