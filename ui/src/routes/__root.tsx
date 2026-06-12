import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'
import { QueryClient } from '@tanstack/react-query'
import { Toaster } from 'sonner'

interface RouterContext {
  queryClient: QueryClient
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: RootComponent,
})

function RootComponent() {
  return (
    <>
      {/* Outlet renders whatever the active child route provides */}
      <Outlet />
      {/*
       * Toaster lives here so it persists across route transitions.
       * top-right is the least intrusive position for auth/action feedback.
       */}
      <Toaster position="top-right" richColors />
    </>
  )
}
