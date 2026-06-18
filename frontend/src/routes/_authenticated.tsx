/**
 * _authenticated — pathless layout route that gates every authenticated
 * screen. beforeLoad runs synchronously during routing, so unauth users
 * never see a flash of the protected layout before redirect.
 *
 * Sprint 0: this is a stub that just renders <Outlet />. Sprint 1 will
 * add the app shell (sidebar, topbar, breadcrumbs) here so every child
 * route inherits it for free.
 */
import { createFileRoute, Outlet, redirect } from '@tanstack/react-router'
import { useAuthStore } from '@/store/authStore'

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
  // Sprint 1 wires the full shell here — sidebar + topbar + content slot.
  return <Outlet />
}
