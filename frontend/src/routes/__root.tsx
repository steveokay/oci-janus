/**
 * Root route — wraps the whole app with the providers every screen needs:
 *   * QueryClientProvider for TanStack Query
 *   * Sonner Toaster for global toasts
 *
 * Auth guarding lives on the per-route level (_authenticated layout)
 * so unauthenticated routes like /login don't pay the redirect cost.
 */
import { createRootRoute, Outlet } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      // Don't refetch on every tab focus — too chatty for an ops dashboard
      // where the user often opens a tab and reads it for a long time.
      refetchOnWindowFocus: false,
      staleTime: 30_000,
    },
  },
})

export const Route = createRootRoute({
  component: RootLayout,
})

function RootLayout() {
  return (
    <QueryClientProvider client={queryClient}>
      <Outlet />
      <Toaster
        position="bottom-right"
        richColors
        closeButton
        toastOptions={{
          classNames: {
            toast: 'rounded-md shadow-lg border border-border',
          },
        }}
      />
    </QueryClientProvider>
  )
}
