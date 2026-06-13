/**
 * scan.tsx — Security Scan Results screen (stub).
 *
 * Route path: /dashboard/:repoName/scan?tag=:tag
 * Full implementation in Sprint 5 — this stub registers the route so the
 * tag table in the Image Details page can link here with correct types.
 *
 * @see frontend/design/stitch/security_scan_results/code.html
 */

import { createFileRoute, useParams, useSearch } from '@tanstack/react-router'
import { z } from 'zod'

const scanSearchSchema = z.object({
  tag: z.string().optional(),
})

export const Route = createFileRoute(
  '/_authenticated/dashboard/$repoName/scan',
)({
  validateSearch: scanSearchSchema,
  component: SecurityScanPage,
})

function SecurityScanPage() {
  const { repoName } = useParams({
    from: '/_authenticated/dashboard/$repoName/scan',
  })
  const { tag } = useSearch({ from: '/_authenticated/dashboard/$repoName/scan' })

  return (
    <div className="p-gutter text-on-surface">
      <p className="text-headline-lg">
        Security Scan — {repoName}{tag ? `:${tag}` : ''}
      </p>
      <p className="text-body-md text-on-surface-variant mt-md">
        Coming soon — full implementation in Sprint 5.
      </p>
    </div>
  )
}
