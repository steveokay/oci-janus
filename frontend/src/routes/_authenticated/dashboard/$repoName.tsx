/**
 * $repoName.tsx — Layout route for /dashboard/:repoName and its children.
 *
 * Renders an <Outlet /> that is filled by either:
 *  - $repoName/index.tsx (Image Details & Tags at /dashboard/:repoName)
 *  - $repoName/scan.tsx  (Security Scan Results at /dashboard/:repoName/scan)
 */

import { createFileRoute, Outlet } from '@tanstack/react-router'

export const Route = createFileRoute('/_authenticated/dashboard/$repoName')({
  component: () => <Outlet />,
})
