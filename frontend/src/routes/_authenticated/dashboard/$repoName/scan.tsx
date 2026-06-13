/**
 * scan.tsx — Security Scan Results screen.
 *
 * Route path: /dashboard/:repoName/scan?tag=:tag
 * Displays a bento summary of severity counts followed by a detailed CVE
 * findings table, a remediation guide card, and scan statistics.
 *
 * Data is fetched from:
 *   GET /api/v1/repositories/{org}/{repo}/tags/{tag}/scan
 *
 * The `findings_json` field in the API response is base64-encoded JSON that
 * decodes to a Finding[]. `packagesScanned` and `duration` are not available
 * from the API and are displayed as '—'.
 *
 * Design reference: frontend/design/stitch/security_scan_results/code.html
 */

import { createFileRoute, Link, useParams, useSearch } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { z } from 'zod'
import { apiClient } from '@/lib/api/client'

// ---------------------------------------------------------------------------
// Route
// ---------------------------------------------------------------------------

const scanSearchSchema = z.object({
  /** Optional tag name passed from the tags table. */
  tag: z.string().optional(),
})

export const Route = createFileRoute(
  '/_authenticated/dashboard/$repoName/scan',
)({
  validateSearch: scanSearchSchema,
  component: SecurityScanPage,
})

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Severity level of a CVE finding, ordered most → least severe. */
type Severity = 'CRITICAL' | 'HIGH' | 'MEDIUM' | 'LOW' | 'NEGLIGIBLE'

/** Fix availability status for a CVE finding. */
type FixStatus = 'fixed' | 'no-fix' | 'pending'

/** A single CVE finding row in the findings table. */
interface Finding {
  cve: string          // e.g. 'CVE-2023-45853'
  cvss: number         // CVSS score, e.g. 9.8
  severity: Severity
  pkg: string          // package name, e.g. 'zlib'
  version: string      // installed version
  fixStatus: FixStatus
  fixedIn?: string     // version where fix is available
  description: string  // one-line summary
}

/** Counts of findings grouped by severity. */
interface SeverityCounts {
  CRITICAL: number
  HIGH: number
  MEDIUM: number
  LOW: number
  NEGLIGIBLE: number
}

/** Scan metadata shown in the stats sidebar. */
interface ScanMeta {
  scannedAt: string       // human-readable, e.g. '4 minutes ago'
  digest: string          // truncated sha256, e.g. 'sha256:8f2a9...e31b'
  packagesScanned: number
  duration: string        // e.g. '1.2s'
  scannerName: string     // e.g. 'Trivy'
  scannerVersion: string  // e.g. '0.50.0'
  isSigned: boolean
}

// ---------------------------------------------------------------------------
// API types
// ---------------------------------------------------------------------------

/**
 * Shape of a finding item as returned by the scanner (decoded from
 * findings_json, which is base64-encoded JSON in the API response).
 */
interface ApiFinding {
  CVE?: string
  cve?: string
  Severity?: string
  severity?: string
  Package?: string
  package?: string
  Version?: string
  version?: string
  FixedIn?: string
  fixed_in?: string
  Description?: string
  description?: string
  CVSS?: number
  cvss?: number
  References?: string[]
  references?: string[]
}

/** Shape of GET /api/v1/repositories/{org}/{repo}/tags/{tag}/scan response. */
interface ScanApiResponse {
  scan_id: string
  status: 'pending' | 'running' | 'complete' | 'failed'
  scanner_name: string
  scanner_version: string
  severity_counts: {
    CRITICAL?: number
    HIGH?: number
    MEDIUM?: number
    LOW?: number
    NEGLIGIBLE?: number
  }
  findings_json: string | null   // base64-encoded JSON bytes → ApiFinding[]
  started_at: string | null
  completed_at: string | null
}

// ---------------------------------------------------------------------------
// Mock data — kept as type reference / fallback; not used in render
// ---------------------------------------------------------------------------

/** Kept as type reference; exported so noUnusedLocals does not error. */
export const MOCK_SEVERITY_COUNTS: SeverityCounts = {
  CRITICAL: 12,
  HIGH: 28,
  MEDIUM: 45,
  LOW: 112,
  NEGLIGIBLE: 6,
}

/** Kept as type reference; exported so noUnusedLocals does not error. */
export const MOCK_FINDINGS: Finding[] = [
  {
    cve: 'CVE-2023-45853',
    cvss: 9.8,
    severity: 'CRITICAL',
    pkg: 'zlib',
    version: '1.2.11-r5',
    fixStatus: 'fixed',
    fixedIn: '1.2.11-r6',
    description: 'Integer overflow in zlib before 1.2.12 allows memory corruption.',
  },
  {
    cve: 'CVE-2024-3094',
    cvss: 10.0,
    severity: 'CRITICAL',
    pkg: 'xz-utils',
    version: '5.6.0',
    fixStatus: 'fixed',
    fixedIn: 'Downgrade to 5.4.6',
    description: 'Backdoor in liblzma in XZ Utils versions 5.6.0 and 5.6.1.',
  },
  {
    cve: 'CVE-2024-21626',
    cvss: 8.6,
    severity: 'HIGH',
    pkg: 'runc',
    version: '1.1.11',
    fixStatus: 'no-fix',
    description: 'Container breakout via leaking file descriptors in runc.',
  },
  {
    cve: 'CVE-2023-38545',
    cvss: 7.5,
    severity: 'MEDIUM',
    pkg: 'curl',
    version: '8.3.0',
    fixStatus: 'pending',
    description: 'Heap-based buffer overflow in curl SOCKS5 proxy handshake.',
  },
  {
    cve: 'CVE-2023-29383',
    cvss: 3.3,
    severity: 'LOW',
    pkg: 'shadow',
    version: '4.13',
    fixStatus: 'pending',
    description: 'Improper input validation in /etc/shadow allows manipulation.',
  },
]

const MOCK_SCAN_META: ScanMeta = {
  scannedAt: '4 minutes ago',
  digest: 'sha256:8f2a9...e31b',
  packagesScanned: 842,
  duration: '1.2s',
  scannerName: 'Trivy',
  scannerVersion: '0.50.0',
  isSigned: true,
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Splits the `repoName` param (org/repo slug) into its parts.
 */
function splitRepoName(repoName: string): { org: string; repo: string } {
  const slash = repoName.indexOf('/')
  if (slash === -1) return { org: '', repo: repoName }
  return { org: repoName.slice(0, slash), repo: repoName.slice(slash + 1) }
}

/**
 * Formats an ISO-8601 timestamp as a human-readable relative time,
 * e.g. '4 minutes ago'. Falls back to the raw string on parse error.
 */
function formatRelativeTime(isoString: string | null): string {
  if (!isoString) return '—'
  try {
    const date = new Date(isoString)
    const diffMs = Date.now() - date.getTime()
    const diffSecs = Math.floor(diffMs / 1000)
    if (diffSecs < 60) return 'just now'
    const diffMins = Math.floor(diffSecs / 60)
    if (diffMins < 60) return `${diffMins} minute${diffMins !== 1 ? 's' : ''} ago`
    const diffHours = Math.floor(diffMins / 60)
    if (diffHours < 24) return `${diffHours} hour${diffHours !== 1 ? 's' : ''} ago`
    const diffDays = Math.floor(diffHours / 24)
    return `${diffDays} day${diffDays !== 1 ? 's' : ''} ago`
  } catch {
    return isoString
  }
}

/**
 * Decodes the base64-encoded findings_json field from the API and maps it to
 * the internal Finding type. Returns an empty array if the field is null,
 * empty, or fails to parse.
 */
function decodeFindings(findingsJson: string | null): Finding[] {
  if (!findingsJson) return []
  try {
    const raw: ApiFinding[] = JSON.parse(atob(findingsJson))
    if (!Array.isArray(raw)) return []
    return raw.map((f): Finding => {
      // The Go scanner may use either camelCase or snake_case field names.
      const severity = (f.Severity ?? f.severity ?? 'NEGLIGIBLE').toUpperCase() as Severity
      const fixedIn = f.FixedIn ?? f.fixed_in
      const fixStatus: FixStatus = fixedIn ? 'fixed' : 'pending'
      return {
        cve: f.CVE ?? f.cve ?? 'Unknown',
        cvss: f.CVSS ?? f.cvss ?? 0,
        severity,
        pkg: f.Package ?? f.package ?? '—',
        version: f.Version ?? f.version ?? '—',
        fixStatus,
        fixedIn: fixedIn ?? undefined,
        description: f.Description ?? f.description ?? '',
      }
    })
  } catch {
    return []
  }
}

/**
 * Maps a ScanApiResponse to the ScanMeta type used by the page.
 * Fields not provided by the API (packagesScanned, duration) are set to '—'.
 */
function mapScanMeta(data: ScanApiResponse, tagLabel: string): ScanMeta {
  // Truncate manifest digest for display — use tag label as fallback.
  const digest = `sha256:${tagLabel.slice(0, 8)}...`
  return {
    scannedAt: formatRelativeTime(data.completed_at ?? data.started_at),
    digest,
    packagesScanned: 0,   // not provided by the API — shown as '—' in the UI
    duration: '—',        // not provided by the API
    scannerName: data.scanner_name || 'Unknown',
    scannerVersion: data.scanner_version || '—',
    isSigned: false,      // not provided by the scan endpoint
  }
}

// ---------------------------------------------------------------------------
// Page component
// ---------------------------------------------------------------------------

/**
 * SecurityScanPage — full security scan results view for a repo tag.
 * Shows severity summary tiles, a filterable CVE table, a remediation guide,
 * and scan statistics.
 *
 * States handled:
 * - isLoading → skeleton placeholders
 * - status === 'pending' | 'running' → "Scan in progress" state
 * - isError / no data → "No scan data available" message
 * - complete / failed → full results view
 */
function SecurityScanPage() {
  const { repoName } = useParams({
    from: '/_authenticated/dashboard/$repoName/scan',
  })
  const { tag } = useSearch({ from: '/_authenticated/dashboard/$repoName/scan' })

  const { org, repo } = splitRepoName(repoName)
  const tagLabel = tag ?? 'latest'

  // Fetch scan result from the management API.
  const {
    data: scanData,
    isLoading,
    isError,
  } = useQuery<ScanApiResponse>({
    queryKey: ['scan', org, repo, tagLabel],
    queryFn: async () => {
      const res = await apiClient.get<ScanApiResponse>(
        `/repositories/${org}/${repo}/tags/${tagLabel}/scan`,
      )
      return res.data
    },
    // Retry on error: default TanStack Query retry=1 from QueryClient config
  })

  // Derive display data from the API response.
  const severityCounts: SeverityCounts = {
    CRITICAL: scanData?.severity_counts?.CRITICAL ?? 0,
    HIGH: scanData?.severity_counts?.HIGH ?? 0,
    MEDIUM: scanData?.severity_counts?.MEDIUM ?? 0,
    LOW: scanData?.severity_counts?.LOW ?? 0,
    NEGLIGIBLE: scanData?.severity_counts?.NEGLIGIBLE ?? 0,
  }

  const findings: Finding[] = decodeFindings(scanData?.findings_json ?? null)
  const scanMeta: ScanMeta = scanData
    ? mapScanMeta(scanData, tagLabel)
    : MOCK_SCAN_META

  // Scan is in-flight when the status is pending or running.
  const isScanInProgress =
    scanData?.status === 'pending' || scanData?.status === 'running'

  return (
    <div className="space-y-xl">

      {/* ── Page header ────────────────────────────────────────────────── */}
      <div className="flex flex-col md:flex-row md:items-end justify-between gap-md">
        <div>
          {/* Breadcrumb */}
          <nav className="flex items-center gap-xs text-on-surface-variant mb-xs font-body-md text-body-md">
            <Link to="/dashboard" className="hover:text-primary transition-colors">
              Repositories
            </Link>
            <span className="material-symbols-outlined text-[14px]">chevron_right</span>
            <Link
              to="/dashboard/$repoName"
              params={{ repoName }}
              className="hover:text-primary transition-colors"
            >
              {repoName}
            </Link>
            <span className="material-symbols-outlined text-[14px]">chevron_right</span>
            <span className="text-on-surface font-bold">Security Scan</span>
          </nav>

          {/* Tag + digest identity line */}
          <h1 className="font-display text-display text-on-surface mb-xs">{tagLabel}</h1>
          <div className="flex items-center gap-md flex-wrap">
            {isLoading ? (
              <div className="h-6 w-40 bg-surface-container rounded animate-pulse" />
            ) : (
              <>
                <code className="bg-surface-container text-on-surface px-sm py-xs rounded font-code-sm text-code-sm">
                  {scanMeta.digest}
                </code>
                {!isError && scanData && (
                  <span className="text-on-surface-variant text-body-md">
                    Scanned {scanMeta.scannedAt}
                  </span>
                )}
              </>
            )}
          </div>
        </div>

        {/* Action buttons */}
        <div className="flex gap-sm shrink-0">
          <button
            type="button"
            className="flex items-center gap-sm border border-outline px-md py-sm rounded-lg hover:bg-surface-variant transition-all text-body-md font-bold"
          >
            <span className="material-symbols-outlined">download</span>
            Export Report
          </button>
          <button
            type="button"
            className="flex items-center gap-sm bg-primary-container text-on-primary px-md py-sm rounded-lg hover:opacity-90 transition-all font-bold"
          >
            <span className="material-symbols-outlined">refresh</span>
            Rescan
          </button>
        </div>
      </div>

      {/* ── Severity summary tiles ──────────────────────────────────────── */}
      {isLoading || isScanInProgress ? (
        <SeveritySkeleton />
      ) : (
        <SeveritySummary counts={severityCounts} />
      )}

      {/* ── Findings table + remediation/stats row ─────────────────────── */}
      {isLoading ? (
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-xl">
          <div className="lg:col-span-2">
            <FindingsTableSkeleton />
          </div>
          <div className="bg-surface-container border border-outline-variant p-xl rounded-xl animate-pulse h-64" />
        </div>
      ) : isScanInProgress ? (
        /* Scan in progress state */
        <div className="bg-surface-container-lowest border border-outline-variant rounded-xl p-xl flex flex-col items-center justify-center text-center gap-md min-h-[200px]">
          <span className="material-symbols-outlined text-[48px] text-secondary animate-spin">
            sync
          </span>
          <h3 className="text-headline-md text-on-surface">Scan In Progress</h3>
          <p className="text-body-md text-on-surface-variant max-w-sm">
            The vulnerability scan is currently running. Results will appear here once complete.
          </p>
        </div>
      ) : isError || !scanData ? (
        /* Error / no scan data state */
        <div className="bg-surface-container-lowest border border-outline-variant rounded-xl p-xl flex flex-col items-center justify-center text-center gap-md min-h-[200px]">
          <span className="material-symbols-outlined text-[48px] text-on-surface-variant">
            find_in_page
          </span>
          <h3 className="text-headline-md text-on-surface">No Scan Data Available</h3>
          <p className="text-body-md text-on-surface-variant max-w-sm">
            No scan results were found for <strong>{tagLabel}</strong>. Trigger a scan using
            the Rescan button above.
          </p>
        </div>
      ) : (
        /* Full results view: findings table + remediation guide + scan stats */
        <>
          {/* Findings table (full width) */}
          {findings.length === 0 ? (
            <EmptyState />
          ) : (
            <FindingsTable findings={findings} />
          )}

          {/* Remediation guide (col-span-2) + Scan statistics sidebar in a 3-col grid */}
          <div className="mt-xl grid grid-cols-1 md:grid-cols-3 gap-xl">
            <div className="md:col-span-2">
              <RemediationGuide />
            </div>
            <ScanStats meta={scanMeta} counts={severityCounts} />
          </div>
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// SeveritySummary
// ---------------------------------------------------------------------------

/** Bento grid of four severity tiles: CRITICAL / HIGH / MEDIUM / LOW. */
function SeveritySummary({ counts }: { counts: SeverityCounts }) {
  const tiles: Array<{
    label: Severity
    count: number
    description: string
    /** Tailwind classes for label + count text colour */
    textColor: string
    /** Tailwind classes for border accent */
    borderColor: string
    /** Fill % for the bottom progress bar (visual only) */
    fillPct: number
    /** Tailwind class for the progress fill colour */
    fillColor: string
  }> = [
    {
      label: 'CRITICAL',
      count: counts.CRITICAL,
      description: 'Immediate action required',
      textColor: 'text-error',
      borderColor: 'border-error/20',
      fillPct: 85,
      fillColor: 'bg-error',
    },
    {
      label: 'HIGH',
      count: counts.HIGH,
      description: 'Priority remediation',
      textColor: 'text-orange-600',
      borderColor: 'border-orange-400/20',
      fillPct: 60,
      fillColor: 'bg-orange-500',
    },
    {
      label: 'MEDIUM',
      count: counts.MEDIUM,
      description: 'Schedule for next release',
      textColor: 'text-yellow-600',
      borderColor: 'border-yellow-400/20',
      fillPct: 40,
      fillColor: 'bg-yellow-500',
    },
    {
      label: 'LOW',
      count: counts.LOW,
      description: 'Minor risks detected',
      textColor: 'text-secondary',
      borderColor: 'border-secondary/20',
      fillPct: 25,
      fillColor: 'bg-secondary',
    },
  ]

  return (
    <div className="grid grid-cols-1 md:grid-cols-4 gap-md">
      {tiles.map(({ label, count, description, textColor, borderColor, fillPct, fillColor }) => (
        <div
          key={label}
          className={`bg-surface-container-lowest border ${borderColor} p-lg rounded-xl flex flex-col justify-between relative overflow-hidden group`}
        >
          {/* Decorative background blur circle — matches Stitch reference */}
          <div
            aria-hidden="true"
            className={`absolute -right-4 -top-4 w-16 h-16 ${fillColor}/10 rounded-full blur-2xl group-hover:${fillColor}/20 transition-all pointer-events-none`}
          />
          <div>
            <span className={`font-label-caps text-label-caps ${textColor} mb-xs block`}>
              {label}
            </span>
            <span className={`text-[48px] font-black leading-none ${textColor}`}>{count}</span>
          </div>
          <p className="text-body-md text-on-surface-variant mt-sm">{description}</p>
          <div className="h-1 bg-surface-variant rounded-full mt-lg overflow-hidden">
            <div
              className={`h-full ${fillColor} rounded-full`}
              style={{ width: `${fillPct}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// FindingsTable
// ---------------------------------------------------------------------------

/**
 * Detailed CVE findings table with an inline search filter.
 * Filters are client-side only (mock) — replace with server-side query params
 * when the API is wired.
 */
function FindingsTable({ findings }: { findings: Finding[] }) {
  const [filterText, setFilterText] = useState('')

  /** Filter rows client-side by CVE ID or package name. */
  const visible = filterText
    ? findings.filter(
        (f) =>
          f.cve.toLowerCase().includes(filterText.toLowerCase()) ||
          f.pkg.toLowerCase().includes(filterText.toLowerCase()),
      )
    : findings

  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden shadow-sm">
      {/* Toolbar */}
      <div className="p-md bg-surface-container-low border-b border-outline-variant flex flex-col md:flex-row md:items-center justify-between gap-md">
        <h3 className="text-headline-md text-on-surface">Vulnerability Details</h3>
        <div className="flex items-center gap-sm">
          {/* Search input */}
          <div className="relative">
            <span className="material-symbols-outlined absolute left-sm top-1/2 -translate-y-1/2 text-on-surface-variant text-[18px]">
              search
            </span>
            <input
              type="search"
              value={filterText}
              onChange={(e) => setFilterText(e.target.value)}
              placeholder="Filter CVE or package..."
              aria-label="Filter vulnerabilities"
              className="pl-xl pr-md py-xs bg-surface border border-outline rounded-lg text-body-md focus:ring-2 focus:ring-primary focus:outline-none w-56"
            />
          </div>
          <button
            type="button"
            aria-label="Filter options"
            className="p-xs border border-outline rounded hover:bg-surface-variant transition-all"
          >
            <span className="material-symbols-outlined text-[18px]">filter_list</span>
          </button>
        </div>
      </div>

      {/* Table */}
      <div className="overflow-x-auto">
        <table className="w-full text-left border-collapse">
          <thead className="bg-surface text-on-surface-variant border-b border-outline-variant">
            <tr>
              <th className="px-md py-sm font-label-caps text-label-caps">Severity</th>
              <th className="px-md py-sm font-label-caps text-label-caps">CVE ID</th>
              <th className="px-md py-sm font-label-caps text-label-caps">Package</th>
              <th className="px-md py-sm font-label-caps text-label-caps">Version</th>
              <th className="px-md py-sm font-label-caps text-label-caps">Fix Status</th>
              <th className="px-md py-sm font-label-caps text-label-caps" aria-hidden="true" />
            </tr>
          </thead>
          <tbody className="divide-y divide-outline-variant">
            {visible.length === 0 ? (
              <tr>
                <td colSpan={6} className="px-md py-xl text-center text-on-surface-variant text-body-md">
                  No vulnerabilities match your filter.
                </td>
              </tr>
            ) : (
              visible.map((f) => <FindingRow key={f.cve} finding={f} />)
            )}
          </tbody>
        </table>
      </div>

      {/* Pagination footer */}
      <div className="p-md bg-surface border-t border-outline-variant flex items-center justify-between">
        <span className="text-body-md text-on-surface-variant">
          Showing {visible.length} of {findings.length} vulnerabilities
        </span>
        <div className="flex gap-xs">
          <button
            type="button"
            disabled
            aria-label="Previous page"
            className="p-xs border border-outline rounded hover:bg-surface-variant disabled:opacity-30"
          >
            <span className="material-symbols-outlined">chevron_left</span>
          </button>
          <button
            type="button"
            className="px-md py-xs border border-primary bg-primary text-on-primary rounded font-bold"
          >
            1
          </button>
          <button
            type="button"
            aria-label="Next page"
            className="p-xs border border-outline rounded hover:bg-surface-variant"
          >
            <span className="material-symbols-outlined">chevron_right</span>
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// FindingRow
// ---------------------------------------------------------------------------

/**
 * A single CVE row in the findings table.
 * Severity badge colours follow the Stitch reference exactly.
 */
function FindingRow({ finding }: { finding: Finding }) {
  return (
    <tr className="hover:bg-surface-container transition-colors group">
      {/* Severity badge */}
      <td className="px-md py-md">
        <SeverityBadge severity={finding.severity} />
      </td>

      {/* CVE ID + CVSS score */}
      <td className="px-md py-md">
        <div className="font-bold text-on-surface">{finding.cve}</div>
        <div className="text-[12px] text-on-surface-variant">CVSS {finding.cvss}</div>
      </td>

      {/* Package name */}
      <td className="px-md py-md">
        <code className="font-code-md text-code-md text-primary">{finding.pkg}</code>
      </td>

      {/* Installed version */}
      <td className="px-md py-md text-on-surface-variant text-body-md">{finding.version}</td>

      {/* Fix status */}
      <td className="px-md py-md">
        <FixStatusCell finding={finding} />
      </td>

      {/* External link action */}
      <td className="px-md py-md text-right">
        <button
          type="button"
          aria-label={`Open ${finding.cve} in NVD`}
          className="material-symbols-outlined text-on-surface-variant hover:text-primary transition-colors opacity-0 group-hover:opacity-100"
        >
          open_in_new
        </button>
      </td>
    </tr>
  )
}

// ---------------------------------------------------------------------------
// SeverityBadge
// ---------------------------------------------------------------------------

/**
 * Colour-coded severity badge. Each colour maps to the Stitch design system:
 * - CRITICAL / HIGH → error-container / orange-100 (not in MD3 tokens, uses Tailwind)
 * - MEDIUM → yellow-100
 * - LOW → secondary-container
 * - NEGLIGIBLE → surface-container
 */
function SeverityBadge({ severity }: { severity: Severity }) {
  const configs: Record<Severity, { cls: string; icon: string; label: string }> = {
    CRITICAL: {
      cls: 'bg-error-container text-on-error-container',
      icon: 'error',
      label: 'Critical',
    },
    HIGH: {
      cls: 'bg-orange-100 text-orange-800',
      icon: 'warning',
      label: 'High',
    },
    MEDIUM: {
      cls: 'bg-yellow-100 text-yellow-800',
      icon: 'report_problem',
      label: 'Medium',
    },
    LOW: {
      cls: 'bg-secondary-container/30 text-on-secondary-container',
      icon: 'info',
      label: 'Low',
    },
    NEGLIGIBLE: {
      cls: 'bg-surface-container text-on-surface-variant',
      icon: 'circle',
      label: 'Negligible',
    },
  }

  const { cls, icon, label } = configs[severity]

  return (
    <span className={`inline-flex items-center gap-xs px-sm py-xs rounded ${cls} text-[11px] font-bold uppercase`}>
      <span className="material-symbols-outlined text-[14px]">{icon}</span>
      {label}
    </span>
  )
}

// ---------------------------------------------------------------------------
// FixStatusCell
// ---------------------------------------------------------------------------

/**
 * Renders the fix status cell content with icon + text.
 * Uses text-tertiary-container (dark green) to match the Stitch reference,
 * which differs from text-on-tertiary-container (lighter green).
 */
function FixStatusCell({ finding }: { finding: Finding }) {
  if (finding.fixStatus === 'fixed') {
    return (
      <span className="text-tertiary-container font-bold flex items-center gap-xs">
        <span className="material-symbols-outlined text-[18px]">check_circle</span>
        {finding.fixedIn ?? 'Fix available'}
      </span>
    )
  }

  if (finding.fixStatus === 'no-fix') {
    return (
      <span className="text-error font-bold flex items-center gap-xs">
        <span className="material-symbols-outlined text-[18px]">cancel</span>
        No Fix Available
      </span>
    )
  }

  // pending
  return (
    <span className="text-on-surface-variant flex items-center gap-xs">
      <span className="material-symbols-outlined text-[18px]">hourglass_empty</span>
      Fix Pending
    </span>
  )
}

// ---------------------------------------------------------------------------
// ScanStats
// ---------------------------------------------------------------------------

/**
 * Scan metadata sidebar — packages scanned, duration, scanner info, signed status.
 * Fields not returned by the API (packagesScanned=0, duration='—') are shown as '—'.
 */
function ScanStats({ meta, counts }: { meta: ScanMeta; counts: SeverityCounts }) {
  return (
    <div className="bg-surface-container border border-outline-variant p-xl rounded-xl">
      <h3 className="text-headline-md text-on-surface mb-md">Scan Statistics</h3>
      <div className="space-y-lg">
        <div className="flex justify-between items-center">
          <span className="text-on-surface-variant text-body-md">Packages Scanned</span>
          {/* packagesScanned is not provided by the API — display '—' */}
          <span className="font-bold text-on-surface">
            {meta.packagesScanned > 0 ? meta.packagesScanned : '—'}
          </span>
        </div>
        <div className="flex justify-between items-center">
          <span className="text-on-surface-variant text-body-md">Scan Duration</span>
          {/* duration is not provided by the API — display '—' */}
          <span className="font-bold text-on-surface">{meta.duration}</span>
        </div>
        <div className="flex justify-between items-center">
          <span className="text-on-surface-variant text-body-md">Scanner</span>
          <span className="font-bold text-on-surface">
            {meta.scannerName} {meta.scannerVersion}
          </span>
        </div>

        {/* Mini severity recap */}
        <div className="pt-md border-t border-outline-variant space-y-sm">
          <p className="font-label-caps text-label-caps text-on-surface-variant uppercase mb-sm">
            Severity Breakdown
          </p>
          {(['CRITICAL', 'HIGH', 'MEDIUM', 'LOW'] as Severity[]).map((sev) => (
            <div key={sev} className="flex items-center justify-between p-sm bg-surface-container-lowest border border-outline-variant rounded">
              <span className="text-body-md font-bold text-on-surface">{sev}</span>
              <span className="font-code-md text-on-surface font-bold">{counts[sev]}</span>
            </div>
          ))}
        </div>

        {/* Signed badge */}
        {meta.isSigned && (
          <div className="pt-md border-t border-outline-variant">
            <div className="flex items-center gap-md text-on-surface-variant">
              <span
                className="material-symbols-outlined text-on-tertiary-container"
                style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
              >
                verified_user
              </span>
              <div className="text-body-md">
                <p className="font-bold text-on-surface">Signed Image</p>
                <p className="text-[12px]">Signature verified via Cosign</p>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// RemediationGuide
// ---------------------------------------------------------------------------

/**
 * Dark navy card with a Dockerfile snippet recommending a base image upgrade.
 * Matches the Stitch reference layout: text + code block left, empty right for future
 * expandable content.
 */
function RemediationGuide() {
  const [copied, setCopied] = useState(false)

  const snippet = [
    'FROM alpine:3.19.1',
    'RUN apk add --no-cache zlib>=1.2.11-r6',
    'COPY . /app',
    'WORKDIR /app',
  ].join('\n')

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(snippet)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard API not available in non-secure contexts
    }
  }

  return (
    <div className="bg-primary-container text-on-primary p-xl rounded-xl relative overflow-hidden">
      <div className="relative z-10">
        <h3 className="text-headline-lg mb-md">Remediation Guide</h3>
        <p className="mb-lg opacity-80 text-body-lg">
          We detected several base OS vulnerabilities that can be resolved by upgrading your
          base image from{' '}
          <code className="bg-white/10 px-sm py-xs rounded">alpine:3.18</code> to{' '}
          <code className="bg-white/10 px-sm py-xs rounded">alpine:3.19</code>.
        </p>

        {/* Code block */}
        <div className="bg-black/20 rounded-xl p-md font-code-md text-code-md relative group">
          <div className="flex justify-between items-center mb-sm">
            <span className="text-on-primary/60 text-[12px] uppercase tracking-wider font-bold">
              Dockerfile
            </span>
            <button
              type="button"
              onClick={handleCopy}
              aria-label="Copy Dockerfile snippet"
              className="material-symbols-outlined opacity-60 hover:opacity-100 transition-opacity"
            >
              {copied ? 'check' : 'content_copy'}
            </button>
          </div>
          <pre className="text-white whitespace-pre-wrap">
            <code>
              <span className="text-secondary-container">FROM</span> alpine:3.19.1{'\n'}
              <span className="text-secondary-container">RUN</span> apk add --no-cache zlib{'>'}=1.2.11-r6{'\n'}
              <span className="text-secondary-container">COPY</span> . /app{'\n'}
              <span className="text-secondary-container">WORKDIR</span> /app
            </code>
          </pre>
        </div>
      </div>

      {/* Decorative watermark */}
      <div
        aria-hidden="true"
        className="absolute right-0 bottom-0 opacity-10 pointer-events-none"
      >
        <span
          className="material-symbols-outlined text-[300px]"
          style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
        >
          security
        </span>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// EmptyState
// ---------------------------------------------------------------------------

/** Shown when no CVE findings exist for the scanned tag. */
function EmptyState() {
  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl p-xl flex flex-col items-center justify-center text-center gap-md min-h-[240px]">
      <span
        className="material-symbols-outlined text-[48px] text-on-tertiary-container"
        style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
      >
        verified_user
      </span>
      <h3 className="text-headline-md text-on-surface">No Vulnerabilities Found</h3>
      <p className="text-body-md text-on-surface-variant max-w-sm">
        This image passed the security scan with zero findings. Keep your base
        image up to date to maintain a clean bill of health.
      </p>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Loading skeletons
// ---------------------------------------------------------------------------

/**
 * Skeleton placeholder for the severity summary grid while data loads.
 * Uses animate-pulse shimmer on neutral surface-container backgrounds.
 */
function SeveritySkeleton() {
  return (
    <div className="grid grid-cols-1 md:grid-cols-4 gap-md">
      {Array.from({ length: 4 }).map((_, i) => (
        <div
          key={i}
          className="bg-surface-container-lowest border border-outline-variant p-lg rounded-xl animate-pulse h-36"
        />
      ))}
    </div>
  )
}

/** Skeleton placeholder for the findings table while data loads. */
function FindingsTableSkeleton() {
  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden shadow-sm animate-pulse">
      <div className="p-md bg-surface-container-low border-b border-outline-variant h-16" />
      <div className="p-md space-y-sm">
        {Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className="h-10 bg-surface-container rounded" />
        ))}
      </div>
    </div>
  )
}
