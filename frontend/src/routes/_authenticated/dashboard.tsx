/**
 * dashboard.tsx — Layout route for /dashboard and its children.
 *
 * TanStack Router uses this file as a parent layout whenever a dashboard/
 * directory exists alongside it. The <Outlet /> renders either the list page
 * (dashboard/index.tsx at /dashboard) or the detail page
 * (dashboard/$repoName.tsx at /dashboard/$repoName).
 */

import { createFileRoute, Outlet } from '@tanstack/react-router'

export const Route = createFileRoute('/_authenticated/dashboard')({
  component: () => <Outlet />,
})
