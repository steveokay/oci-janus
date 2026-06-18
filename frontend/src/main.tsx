/**
 * App entry — pure plumbing. All real logic lives in routes/.
 */
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createRouter, RouterProvider } from '@tanstack/react-router'
import { routeTree } from './routeTree.gen'
import './styles/globals.css'

const router = createRouter({
  routeTree,
  // Reset scroll on navigation — sidebar-aware deep links shouldn't
  // inherit the previous page's scroll position.
  defaultPreload: 'intent',
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
)
