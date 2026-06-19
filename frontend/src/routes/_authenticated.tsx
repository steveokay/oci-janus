/**
 * _authenticated — pathless layout route that gates every authenticated
 * screen. beforeLoad runs synchronously during routing, so unauth users
 * never see a flash of the protected layout before redirect.
 *
 * Sprint 1a: this now wraps `<Outlet />` in the AppShell (sidebar +
 * topbar + content slot). Every child route inherits the chrome for
 * free — page components only need to render their content.
 */
import { createFileRoute, Outlet, redirect } from '@tanstack/react-router'
import { useAuthStore } from '@/store/authStore'
import { AppShell } from '@/components/shell/AppShell'

export const Route = createFileRoute('/_authenticated')({
  beforeLoad: ({ location }) => {
    const { token } = useAuthStore.getState()
    if (!token) {
      throw redirect({
        to: '/login',
        // Round-trip the original target so login can bounce back after auth.
        search: { from: location.pathname },
      })
    }
  },
  component: AuthenticatedLayout,
})

function AuthenticatedLayout() {
  return (
    <AppShell>
      <Outlet />
    </AppShell>
  )
}
