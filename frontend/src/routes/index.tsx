import { createFileRoute, redirect } from '@tanstack/react-router'

export const Route = createFileRoute('/')({
  /**
   * beforeLoad runs before the component renders, making it the right place
   * for auth-based redirects. We avoid rendering anything at "/" — the user
   * should always land on either /login or /dashboard.
   */
  beforeLoad: () => {
    const token = localStorage.getItem('access_token')
    if (token) {
      throw redirect({ to: '/dashboard' })
    }
    throw redirect({ to: '/login' })
  },
})
