/**
 * Root index ("/") — never rendered as a page. Bounces logged-in users to
 * /dashboard and everyone else to /login. Putting the decision here means
 * deep links to "/" don't dead-end on a blank page.
 */
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useAuthStore } from '@/store/authStore'

export const Route = createFileRoute('/')({
  beforeLoad: () => {
    const { token } = useAuthStore.getState()
    throw redirect({ to: token ? '/dashboard' : '/login' })
  },
})
