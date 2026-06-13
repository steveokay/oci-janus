/**
 * $repoName/index.tsx — Image Details & Tags screen.
 *
 * Route path: /dashboard/:repoName
 * Displays the repository header, docker pull command, tag table, push
 * frequency bar chart, and recent activity feed.
 *
 * Tags are fetched from GET /api/v1/repositories/{org}/{repo}/tags.
 * Missing fields (architectures, compressedSize) are displayed as '—'.
 * Security status defaults to 'unknown' until scan data is available.
 *
 * @see frontend/design/stitch/image_details_tags/code.html
 */

import { createFileRoute, Link, useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { apiClient } from '@/lib/api/client'

// ---------------------------------------------------------------------------
// Route definition
// ---------------------------------------------------------------------------

export const Route = createFileRoute(
  '/_authenticated/dashboard/$repoName/',
)({
  component: ImageDetailsPage,
})

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Security status of a tag from the scan service. */
type TagSecurityStatus = 'clean' | 'vulnerable' | 'unknown'

/** A single tag entry displayed in the tags table. */
interface TagRow {
  name: string
  architectures: string[]    // e.g. ['linux/amd64', 'linux/arm64']
  compressedSize: string     // pre-formatted, e.g. '42.8 MB', or '—' when unavailable
  security: TagSecurityStatus
  vulnCount?: number         // only set when security === 'vulnerable'
  lastPushed: string         // human-readable relative time
}

/** A single entry in the recent activity feed. */
interface ActivityEntry {
  dotColor: string           // Tailwind bg colour class for the bullet dot
  title: string
  titleTag?: string          // optional monospace tag chip inline in title
  actor: string              // actor + relative time, e.g. 'Jenkins CI Pipeline • 2h ago'
}

/** Bar datum for the push frequency chart. */
interface PushBar {
  day: string                // display label, e.g. 'MON'
  heightPct: number          // 0-100, percentage of chart height
  active?: boolean           // true = current day, uses primary-container colour
}

/** Shape of each tag item returned by the management API. */
interface ApiTag {
  name: string
  manifest_digest: string
  updated_at: string   // ISO-8601 timestamp
  created_at: string   // ISO-8601 timestamp
}

/** Shape of GET /api/v1/repositories/{org}/{repo}/tags response. */
interface TagsApiResponse {
  tags: ApiTag[]
}

// ---------------------------------------------------------------------------
// Mock data — kept as type reference and fallbacks; not used in render
// ---------------------------------------------------------------------------

/** Static push-frequency chart bars — no API equivalent yet. */
const MOCK_PUSH_BARS: PushBar[] = [
  { day: 'MON', heightPct: 30 },
  { day: 'TUE', heightPct: 45 },
  { day: 'WED', heightPct: 90 },
  { day: 'THU', heightPct: 60 },
  { day: 'FRI', heightPct: 100, active: true },
  { day: 'SAT', heightPct: 20 },
  { day: 'SUN', heightPct: 15 },
]

/** Static recent-activity feed — no API equivalent yet. */
const MOCK_ACTIVITY: ActivityEntry[] = [
  {
    dotColor: 'bg-tertiary-fixed-dim',
    title: 'New tag ',
    titleTag: 'beta-nightly',
    actor: 'Jenkins CI Pipeline • 2h ago',
  },
  {
    dotColor: 'bg-secondary',
    title: 'Security scan completed',
    actor: 'Trivy Scanner • 4h ago',
  },
  {
    dotColor: 'bg-outline',
    title: 'Documentation updated',
    actor: 'by admin_user • 1d ago',
  },
]

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Formats an ISO-8601 timestamp as a human-readable relative time string,
 * e.g. '2 hours ago', '3 days ago'. Falls back to the raw string on error.
 */
function formatRelativeTime(isoString: string): string {
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
    if (diffDays < 7) return `${diffDays} day${diffDays !== 1 ? 's' : ''} ago`
    const diffWeeks = Math.floor(diffDays / 7)
    if (diffWeeks < 5) return `${diffWeeks} week${diffWeeks !== 1 ? 's' : ''} ago`
    const diffMonths = Math.floor(diffDays / 30)
    return `${diffMonths} month${diffMonths !== 1 ? 's' : ''} ago`
  } catch {
    return isoString
  }
}

/**
 * Splits the `repoName` param (which is encoded as `org%2Frepo` in the URL
 * but arrives decoded as `org/repo`) into its org and repo parts.
 */
function splitRepoName(repoName: string): { org: string; repo: string } {
  const slash = repoName.indexOf('/')
  if (slash === -1) return { org: '', repo: repoName }
  return { org: repoName.slice(0, slash), repo: repoName.slice(slash + 1) }
}

// ---------------------------------------------------------------------------
// Page component
// ---------------------------------------------------------------------------

function ImageDetailsPage() {
  const { repoName } = useParams({ from: '/_authenticated/dashboard/$repoName' })
  const [copied, setCopied] = useState(false)

  const { org, repo } = splitRepoName(repoName)

  // Fetch real tag list from the management API.
  const {
    data: tagsData,
    isLoading: tagsLoading,
    isError: tagsError,
  } = useQuery<TagsApiResponse>({
    queryKey: ['tags', org, repo],
    queryFn: async () => {
      const res = await apiClient.get<TagsApiResponse>(
        `/repositories/${org}/${repo}/tags`,
      )
      return res.data
    },
  })

  // Map API response to the TagRow shape used by the table.
  const tagRows: TagRow[] = (tagsData?.tags ?? []).map((t) => ({
    name: t.name,
    architectures: [],          // not provided by the tags API
    compressedSize: '—',        // not provided by the tags API
    security: 'unknown',        // default until scan data is available
    lastPushed: formatRelativeTime(t.updated_at),
  }))

  const dockerPullCmd = `docker pull cr.io/prod/${repoName}:latest`

  async function handleCopyPull() {
    try {
      await navigator.clipboard.writeText(dockerPullCmd)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard API unavailable in non-secure contexts — silently ignore
    }
  }

  return (
    <div>
      {/* ── Breadcrumb ─────────────────────────────────────────────────── */}
      <nav className="flex items-center gap-sm text-on-surface-variant mb-lg font-body-md text-body-md">
        <Link to="/dashboard" className="hover:text-primary transition-colors">
          Repositories
        </Link>
        <span className="material-symbols-outlined text-sm">chevron_right</span>
        <span className="text-on-surface font-bold">{repoName}</span>
      </nav>

      {/* ── Hero bento: repo card + stats sidebar ──────────────────────── */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-lg mb-xl">

        {/* Repository info card */}
        <div className="lg:col-span-2 bg-surface-container-lowest border border-outline-variant rounded-xl p-xl relative overflow-hidden">
          <div className="relative z-10">
            {/* Title row — repo name + PUBLIC badge */}
            <div className="flex items-center gap-md mb-md">
              <h1 className="text-display text-on-surface">{repoName}</h1>
              <span className="bg-secondary-fixed text-on-secondary-fixed px-sm py-xs rounded text-label-caps font-bold uppercase">
                PUBLIC
              </span>
            </div>

            {/* Description */}
            <p className="text-body-lg text-on-surface-variant mb-xl" style={{ maxWidth: '42rem' }}>
              Core application container for the global production cluster. Optimized
              for high-throughput processing and secure data handling. Updated daily
              via automated CI/CD pipelines.
            </p>

            {/* Docker pull command card */}
            <div className="bg-primary-container rounded-lg p-lg border border-on-primary-container shadow-lg">
              <div className="flex justify-between items-center mb-sm">
                <span className="text-on-primary-container font-label-caps text-label-caps uppercase">
                  Docker Pull Command
                </span>
                {copied && (
                  <span className="text-tertiary-fixed-dim text-xs font-bold">COPIED!</span>
                )}
              </div>
              <div className="flex items-center justify-between bg-black/30 rounded px-md py-sm">
                <code className="font-code-md text-code-md text-tertiary-fixed-dim">
                  {dockerPullCmd}
                </code>
                <button
                  type="button"
                  onClick={handleCopyPull}
                  aria-label="Copy docker pull command"
                  className="text-on-primary hover:text-tertiary-fixed-dim transition-colors ml-md flex-shrink-0"
                >
                  <span className="material-symbols-outlined">content_copy</span>
                </button>
              </div>
            </div>
          </div>

          {/* Decorative repo icon watermark */}
          <div
            aria-hidden="true"
            className="absolute right-0 bottom-0 opacity-5 -mb-8 -mr-8 pointer-events-none"
          >
            <span
              className="material-symbols-outlined text-[200px]"
              style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
            >
              inventory_2
            </span>
          </div>
        </div>

        {/* Stats sidebar — stacked 2 cards */}
        <div className="grid grid-rows-2 gap-lg">
          {/* Total Pulls */}
          <div className="bg-surface-container-lowest border border-outline-variant rounded-xl p-lg flex flex-col justify-center">
            <span className="font-label-caps text-label-caps text-on-surface-variant mb-sm uppercase">
              Total Pulls
            </span>
            <div className="flex items-end gap-sm">
              <span className="text-4xl font-bold text-on-surface">1.2M</span>
              {/* Trending indicator — tertiary-fixed-variant colour from Stitch reference */}
              <span className="flex items-center text-sm font-bold mb-1 text-tertiary-fixed-dim">
                <span className="material-symbols-outlined text-sm">trending_up</span>
                12%
              </span>
            </div>
          </div>

          {/* Vulnerability Scan status */}
          <div className="bg-surface-container-lowest border border-outline-variant rounded-xl p-lg flex flex-col justify-center">
            <span className="font-label-caps text-label-caps text-on-surface-variant mb-sm uppercase">
              Vulnerability Scan
            </span>
            <div className="flex items-center gap-md">
              <div className="flex items-center gap-sm bg-tertiary-container/10 text-on-tertiary-container px-md py-sm rounded-lg">
                <span
                  className="material-symbols-outlined"
                  style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
                >
                  verified_user
                </span>
                <span className="font-bold">Passed</span>
              </div>
              <span className="text-xs text-on-surface-variant">Last scan: 2h ago</span>
            </div>
          </div>
        </div>
      </div>

      {/* ── Tags table ─────────────────────────────────────────────────── */}
      <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden">
        {/* Table toolbar */}
        <div className="px-xl py-lg border-b border-outline-variant flex items-center justify-between">
          <h2 className="text-headline-md text-on-surface">Available Tags</h2>
          <div className="flex items-center gap-sm">
            <button
              type="button"
              className="flex items-center gap-xs px-md py-sm text-body-md border border-outline-variant rounded hover:bg-surface-variant transition-colors"
            >
              <span className="material-symbols-outlined text-sm">filter_list</span>
              Filter
            </button>
            <button
              type="button"
              className="flex items-center gap-xs px-md py-sm text-body-md border border-outline-variant rounded hover:bg-surface-variant transition-colors"
            >
              <span className="material-symbols-outlined text-sm">sort</span>
              Sort
            </button>
          </div>
        </div>

        {/* Table body — loading skeleton, error state, or real rows */}
        <div className="overflow-x-auto">
          {tagsLoading ? (
            <TagsTableSkeleton />
          ) : tagsError ? (
            <div className="px-xl py-xl text-center text-on-surface-variant text-body-md">
              <span className="material-symbols-outlined text-[32px] block mb-sm text-error">
                error_outline
              </span>
              Failed to load tags. Please try refreshing the page.
            </div>
          ) : (
            <table className="w-full text-left border-collapse">
              <thead className="bg-surface-container-low border-b border-outline-variant">
                <tr>
                  <th className="px-xl py-md font-label-caps text-label-caps text-on-surface-variant uppercase">Tag Name</th>
                  <th className="px-xl py-md font-label-caps text-label-caps text-on-surface-variant uppercase">OS/Architecture</th>
                  <th className="px-xl py-md font-label-caps text-label-caps text-on-surface-variant uppercase">Compressed Size</th>
                  <th className="px-xl py-md font-label-caps text-label-caps text-on-surface-variant uppercase">Security Status</th>
                  <th className="px-xl py-md font-label-caps text-label-caps text-on-surface-variant uppercase">Last Pushed</th>
                  <th className="px-xl py-md" aria-hidden="true" />
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant">
                {tagRows.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="px-xl py-xl text-center text-on-surface-variant text-body-md">
                      No tags found for this repository.
                    </td>
                  </tr>
                ) : (
                  tagRows.map((tag) => (
                    <TagTableRow key={tag.name} tag={tag} repoName={repoName} />
                  ))
                )}
              </tbody>
            </table>
          )}
        </div>

        {/* Pagination footer */}
        {!tagsLoading && !tagsError && (
          <div className="px-xl py-md bg-surface-container-low border-t border-outline-variant flex items-center justify-between">
            <p className="text-xs text-on-surface-variant font-medium">
              Showing 1 to {tagRows.length} of {tagRows.length} tags
            </p>
            <div className="flex items-center gap-sm">
              <button
                type="button"
                disabled
                aria-label="Previous page"
                className="p-1 border border-outline-variant rounded disabled:opacity-30"
              >
                <span className="material-symbols-outlined text-lg">chevron_left</span>
              </button>
              <button
                type="button"
                aria-label="Next page"
                className="p-1 border border-outline-variant rounded hover:bg-surface-variant transition-colors"
              >
                <span className="material-symbols-outlined text-lg">chevron_right</span>
              </button>
            </div>
          </div>
        )}
      </div>

      {/* ── Bottom row: push frequency + recent activity ────────────────── */}
      <div className="mt-xl grid grid-cols-1 md:grid-cols-2 gap-lg">

        {/* Push Frequency chart */}
        <div className="bg-surface-container-lowest border border-outline-variant rounded-xl p-lg">
          <h3 className="text-headline-md text-on-surface mb-md flex items-center gap-sm">
            <span className="material-symbols-outlined text-primary">analytics</span>
            Push Frequency
          </h3>
          {/* Bar chart — plain CSS bars matching the Stitch reference */}
          <div className="h-48 flex items-end gap-2 px-md">
            {MOCK_PUSH_BARS.map((bar) => (
              <div
                key={bar.day}
                title={bar.day}
                className={`flex-1 rounded-t ${bar.active ? 'bg-primary-container' : 'bg-secondary-container'}`}
                style={{ height: `${bar.heightPct}%` }}
              />
            ))}
          </div>
          <div className="flex justify-between mt-sm text-[10px] text-on-surface-variant uppercase font-bold px-md">
            {MOCK_PUSH_BARS.map((bar) => (
              <span key={bar.day}>{bar.day}</span>
            ))}
          </div>
        </div>

        {/* Recent Activity feed */}
        <div className="bg-surface-container-lowest border border-outline-variant rounded-xl p-lg">
          <h3 className="text-headline-md text-on-surface mb-md flex items-center gap-sm">
            <span className="material-symbols-outlined text-primary">history</span>
            Recent Activity
          </h3>
          <div className="space-y-md">
            {MOCK_ACTIVITY.map((entry, i) => (
              <div key={i} className="flex items-start gap-md">
                {/* Coloured bullet dot */}
                <div className={`mt-1 w-2 h-2 rounded-full flex-shrink-0 ${entry.dotColor}`} />
                <div>
                  <p className="text-body-md font-bold">
                    {entry.title}
                    {entry.titleTag && (
                      <span className="font-code-md text-code-sm bg-secondary-fixed px-1 rounded font-normal">
                        {entry.titleTag}
                      </span>
                    )}
                    {/* append "pushed" after the tag chip if titleTag present */}
                    {entry.titleTag ? ' pushed' : ''}
                  </p>
                  <p className="text-xs text-on-surface-variant">{entry.actor}</p>
                </div>
              </div>
            ))}
          </div>
        </div>

      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// TagTableRow
// ---------------------------------------------------------------------------

/**
 * Renders a single row in the tags table.
 * Vulnerable rows get a left red border accent (border-l-4 border-error).
 */
function TagTableRow({ tag, repoName }: { tag: TagRow; repoName: string }) {
  const isVulnerable = tag.security === 'vulnerable'

  return (
    <tr
      className={[
        'hover:bg-surface-variant/30 transition-colors group',
        isVulnerable ? 'border-l-4 border-error' : '',
      ].join(' ')}
    >
      {/* Tag name chip — links to the scan screen for this tag */}
      <td className="px-xl py-md">
        <Link
          to="/dashboard/$repoName/scan"
          params={{ repoName }}
          search={{ tag: tag.name }}
          className="font-code-md text-code-md bg-secondary-fixed/50 px-sm py-xs rounded hover:bg-secondary-fixed transition-colors"
        >
          {tag.name}
        </Link>
      </td>

      {/* OS/Architecture — '—' when not available from tags API */}
      <td className="px-xl py-md">
        {tag.architectures.length > 0 ? (
          <div className="flex flex-col">
            <span className="text-body-md font-medium">{tag.architectures[0]}</span>
            {tag.architectures[1] && (
              <span className="text-xs text-on-surface-variant">{tag.architectures[1]}</span>
            )}
          </div>
        ) : (
          <span className="text-body-md text-on-surface-variant">—</span>
        )}
      </td>

      {/* Compressed size — '—' when not available from tags API */}
      <td className="px-xl py-md text-body-md">
        {tag.compressedSize}
      </td>

      {/* Security status badge */}
      <td className="px-xl py-md">
        <SecurityStatusBadge tag={tag} />
      </td>

      {/* Last pushed */}
      <td className="px-xl py-md text-body-md text-on-surface-variant">{tag.lastPushed}</td>

      {/* Row action button */}
      <td className="px-xl py-md text-right">
        <button
          type="button"
          aria-label={`More options for tag ${tag.name}`}
          className="p-2 text-on-surface-variant hover:text-primary opacity-0 group-hover:opacity-100 transition-all"
        >
          <span className="material-symbols-outlined">more_vert</span>
        </button>
      </td>
    </tr>
  )
}

// ---------------------------------------------------------------------------
// SecurityStatusBadge
// ---------------------------------------------------------------------------

/**
 * Inline badge for tag security status.
 * - clean    → green check badge
 * - vulnerable → red warning badge with count
 * - unknown  → grey shield badge (default when scan data not yet available)
 *
 * The filled icon ensures status is not communicated by colour alone.
 */
function SecurityStatusBadge({ tag }: { tag: TagRow }) {
  if (tag.security === 'clean') {
    return (
      <div className="inline-flex items-center gap-xs px-sm py-1 rounded bg-tertiary-container/10 text-on-tertiary-container text-xs font-bold uppercase">
        <span
          className="material-symbols-outlined text-sm"
          style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
        >
          check_circle
        </span>
        Clean
      </div>
    )
  }

  if (tag.security === 'vulnerable') {
    return (
      <div className="inline-flex items-center gap-xs px-sm py-1 rounded bg-error-container text-on-error-container text-xs font-bold uppercase">
        <span
          className="material-symbols-outlined text-sm"
          style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
        >
          warning
        </span>
        {tag.vulnCount} Vulnerabilities Found
      </div>
    )
  }

  // unknown — grey shield badge shown when no scan result is available yet
  return (
    <div className="inline-flex items-center gap-xs px-sm py-1 rounded bg-surface-container text-on-surface-variant text-xs font-bold uppercase">
      <span
        className="material-symbols-outlined text-sm"
        style={{ fontVariationSettings: "'FILL' 0, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
      >
        shield
      </span>
      Unknown
    </div>
  )
}

// ---------------------------------------------------------------------------
// TagsTableSkeleton
// ---------------------------------------------------------------------------

/**
 * Pulse skeleton placeholder for the tags table while data is loading.
 * Mirrors the table structure so layout shift is minimal on data arrival.
 */
function TagsTableSkeleton() {
  return (
    <div className="animate-pulse">
      {/* Fake thead */}
      <div className="bg-surface-container-low border-b border-outline-variant px-xl py-md h-12" />
      {/* Fake rows */}
      {Array.from({ length: 4 }).map((_, i) => (
        <div
          key={i}
          className="flex items-center gap-xl px-xl py-md border-b border-outline-variant"
        >
          <div className="h-6 w-24 bg-surface-container rounded" />
          <div className="h-4 w-20 bg-surface-container rounded" />
          <div className="h-4 w-16 bg-surface-container rounded" />
          <div className="h-6 w-20 bg-surface-container rounded" />
          <div className="h-4 w-24 bg-surface-container rounded" />
        </div>
      ))}
    </div>
  )
}
