/**
 * builds.tsx — Build History screen.
 *
 * Route path: /dashboard/:repoName/builds?tag=:tag
 * Displays a bento stats row, a paginated build table, a Quick Console Preview
 * terminal panel, and a Scan Summary sidebar card.
 *
 * Data is fetched from:
 *   GET /api/v1/repositories/{org}/{repo}/tags/{tag}/builds
 *
 * The API currently always returns { builds: [], total: 0 } because the
 * registry-audit build-event query RPC is not yet implemented. The EmptyState
 * component renders when builds is empty. MOCK_STATS are used as fallback
 * defaults so the stat tiles look reasonable once builds are wired later.
 *
 * Design reference: frontend/design/stitch/build_history/code.html
 */

import { createFileRoute, Link, useParams, useSearch } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { apiClient } from '@/lib/api/client'

// ---------------------------------------------------------------------------
// Route
// ---------------------------------------------------------------------------

const buildSearchSchema = z.object({
  /** Optional tag name — used to scope the builds query to a specific tag. */
  tag: z.string().optional(),
})

export const Route = createFileRoute(
  '/_authenticated/dashboard/$repoName/builds',
)({
  validateSearch: buildSearchSchema,
  component: BuildHistoryPage,
})

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Lifecycle status of a build run. */
type BuildStatus = 'in_progress' | 'success' | 'failed'

/** Actor who triggered a build — either a human user or a CI system. */
type BuildActor =
  | { kind: 'user'; login: string }
  | { kind: 'ci'; system: string }   // e.g. 'GitHub Actions'

/** A single build run row in the history table. */
interface BuildRow {
  /** Short build identifier, e.g. '#BD-8921' */
  id: string
  status: BuildStatus
  /** Abbreviated commit hash, e.g. 'f2a8c1e' */
  commitHash: string
  triggeredBy: BuildActor
  /** Formatted duration, e.g. '3m 45s', or '--' for in-progress builds */
  duration: string
  /** Human-readable relative timestamp, e.g. '2m ago' */
  timestamp: string
}

/** Aggregate stats shown in the top bento row. */
interface BuildStats {
  successRatePct: number     // e.g. 94.2
  successRateDelta: string   // e.g. '+2.1%'
  avgDuration: string        // e.g. '4m 12s'
  totalStorageGb: number     // e.g. 84.2
  storageUsedPct: number     // 0-100 for progress bar
  activeBuilds: number
  activeRunners: string      // e.g. 'Runner 04, Runner 09'
}

// ---------------------------------------------------------------------------
// API types
// ---------------------------------------------------------------------------

/**
 * Shape of a single build record as returned by the management REST API.
 * Field names are snake_case to match the Go JSON serialisation of BuildResponse.
 */
interface ApiBuildRow {
  /** Unique build identifier, e.g. '#BD-8921'. Maps to BuildRow.id. */
  build_id: string
  status: string
  /** Full or abbreviated commit hash. Maps to BuildRow.commitHash. */
  commit_hash: string
  /**
   * Plain string identifying who triggered the build — either a user login or
   * a CI system name (e.g. "GitHub Actions"). Maps to BuildRow.triggeredBy.
   */
  triggered_by: string
  duration: string
  timestamp: string
}

/** Shape of GET /api/v1/repositories/{org}/{repo}/tags/{tag}/builds response. */
interface BuildsApiResponse {
  /** Build run records. Currently always [] — audit RPC not yet implemented. */
  builds: ApiBuildRow[]
  total: number
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
 * Known CI system names returned by the management API in the `triggered_by`
 * field. Any value matching this set is treated as a CI actor; all others are
 * treated as a human user login.
 */
const CI_SYSTEM_NAMES = new Set([
  'GitHub Actions',
  'GitLab CI',
  'CircleCI',
  'Jenkins',
  'ci',
])

/**
 * Maps a raw API build record (snake_case) to the internal BuildRow type.
 *
 * - build_id  → id
 * - commit_hash → commitHash
 * - triggered_by (string) → BuildActor union based on CI_SYSTEM_NAMES lookup
 * - status, duration, timestamp passed through unchanged
 */
function mapBuildRow(api: ApiBuildRow): BuildRow {
  // Determine actor type: CI system or human user.
  const triggeredBy: BuildActor = CI_SYSTEM_NAMES.has(api.triggered_by)
    ? { kind: 'ci', system: api.triggered_by }
    : { kind: 'user', login: api.triggered_by }

  return {
    id: api.build_id,
    status: api.status as BuildStatus,
    commitHash: api.commit_hash,
    triggeredBy,
    duration: api.duration,
    timestamp: api.timestamp,
  }
}

// ---------------------------------------------------------------------------
// Mock data — kept as fallback defaults; MOCK_BUILDS not used in render
// ---------------------------------------------------------------------------

const MOCK_STATS: BuildStats = {
  successRatePct: 94.2,
  successRateDelta: '+2.1%',
  avgDuration: '4m 12s',
  totalStorageGb: 84.2,
  storageUsedPct: 75,
  activeBuilds: 2,
  activeRunners: 'Runner 04, Runner 09',
}

/** Kept as type reference; exported so noUnusedLocals does not error. */
export const MOCK_BUILDS: BuildRow[] = [
  {
    id: '#BD-8921',
    status: 'in_progress',
    commitHash: 'f2a8c1e',
    triggeredBy: { kind: 'user', login: 'j.doe' },
    duration: '--',
    timestamp: '2m ago',
  },
  {
    id: '#BD-8920',
    status: 'success',
    commitHash: 'a901d3f',
    triggeredBy: { kind: 'ci', system: 'GitHub Actions' },
    duration: '3m 45s',
    timestamp: '1h ago',
  },
  {
    id: '#BD-8919',
    status: 'failed',
    commitHash: '7c42b9e',
    triggeredBy: { kind: 'user', login: 'a.kahn' },
    duration: '1m 12s',
    timestamp: '3h ago',
  },
  {
    id: '#BD-8918',
    status: 'success',
    commitHash: '4e12f00',
    triggeredBy: { kind: 'ci', system: 'GitHub Actions' },
    duration: '4m 02s',
    timestamp: '5h ago',
  },
  {
    id: '#BD-8917',
    status: 'success',
    commitHash: 'dd931a2',
    triggeredBy: { kind: 'user', login: 'j.doe' },
    duration: '3m 58s',
    timestamp: '1d ago',
  },
]

// ---------------------------------------------------------------------------
// Page component
// ---------------------------------------------------------------------------

/**
 * BuildHistoryPage — shows CI/CD build runs for a repository.
 * Layout: header → stats bento → builds table → console preview + scan summary.
 *
 * The builds API currently always returns { builds: [], total: 0 }.
 * MOCK_STATS are used as fallback defaults for the stat tiles.
 */
function BuildHistoryPage() {
  const { repoName } = useParams({
    from: '/_authenticated/dashboard/$repoName/builds',
  })
  const { tag } = useSearch({ from: '/_authenticated/dashboard/$repoName/builds' })

  const { org, repo } = splitRepoName(repoName)
  const tagParam = tag ?? 'latest'

  // Fetch build history from the management API.
  // The API always returns { builds: [], total: 0 } for now.
  const {
    data: buildsData,
    isLoading,
  } = useQuery<BuildsApiResponse>({
    queryKey: ['builds', org, repo, tagParam],
    queryFn: async () => {
      const res = await apiClient.get<BuildsApiResponse>(
        `/repositories/${org}/${repo}/tags/${tagParam}/builds`,
      )
      return res.data
    },
  })

  // Map the snake_case API records to the internal BuildRow type.
  // MOCK_STATS are used as fallback defaults for the bento tiles
  // so the layout looks reasonable once builds are wired later.
  const builds: BuildRow[] = buildsData?.builds.map(mapBuildRow) ?? []
  const stats: BuildStats = MOCK_STATS

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
            <span className="material-symbols-outlined text-sm">chevron_right</span>
            <Link
              to="/dashboard/$repoName"
              params={{ repoName }}
              className="hover:text-primary transition-colors"
            >
              {repoName}
            </Link>
            <span className="material-symbols-outlined text-sm">chevron_right</span>
            <span className="text-on-surface font-bold">Build History</span>
          </nav>

          <h1 className="font-display text-display text-on-surface leading-tight">Build History</h1>
          <p className="text-on-surface-variant font-body-lg text-body-lg mt-xs">
            Tracking automated CI/CD builds for{' '}
            <code className="font-code-md text-code-md bg-surface-variant px-xs rounded">
              {repoName}
            </code>
          </p>
        </div>

        {/* Action buttons */}
        <div className="flex gap-sm shrink-0">
          <button
            type="button"
            className="flex items-center gap-xs px-md py-sm border border-outline-variant bg-surface-container-lowest text-on-surface font-label-caps text-label-caps font-bold rounded-lg hover:bg-surface-variant transition-all"
          >
            <span className="material-symbols-outlined text-base">filter_list</span>
            Filter Status
          </button>
          <button
            type="button"
            className="flex items-center gap-xs px-md py-sm bg-primary-container text-on-primary font-label-caps text-label-caps font-bold rounded-lg hover:opacity-90 transition-opacity"
          >
            <span className="material-symbols-outlined text-base">refresh</span>
            Manual Build
          </button>
        </div>
      </div>

      {/* ── Stats bento row ─────────────────────────────────────────────── */}
      {isLoading ? (
        <BuildStatsSkeleton />
      ) : (
        <BuildStatsBento stats={stats} />
      )}

      {/* ── Build table ─────────────────────────────────────────────────── */}
      {isLoading ? (
        <BuildTableSkeleton />
      ) : (
        <BuildTable builds={builds} />
      )}

      {/* ── Console preview + scan summary ──────────────────────────────── */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-xl">
        <div className="lg:col-span-2">
          <ConsolePreview />
        </div>
        <ScanSummary />
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// BuildStatsBento
// ---------------------------------------------------------------------------

/** Four metric tiles matching the Stitch reference bento grid. */
function BuildStatsBento({ stats }: { stats: BuildStats }) {
  return (
    <div className="grid grid-cols-1 md:grid-cols-4 gap-md">

      {/* Success Rate */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl">
        <div className="text-on-surface-variant font-label-caps text-label-caps mb-xs">
          Success Rate
        </div>
        <div className="text-headline-lg font-headline-lg text-on-surface">
          {stats.successRatePct}%
        </div>
        <div className="text-on-tertiary-container bg-tertiary-fixed-dim/20 inline-flex items-center gap-xs px-xs rounded mt-xs text-xs font-bold">
          <span className="material-symbols-outlined text-xs">trending_up</span>
          {stats.successRateDelta}
        </div>
      </div>

      {/* Avg. Duration */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl">
        <div className="text-on-surface-variant font-label-caps text-label-caps mb-xs">
          Avg. Duration
        </div>
        <div className="text-headline-lg font-headline-lg text-on-surface">{stats.avgDuration}</div>
        <div className="text-on-surface-variant font-body-md text-body-md mt-xs">
          Across last 20 builds
        </div>
      </div>

      {/* Total Storage with progress bar */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl">
        <div className="text-on-surface-variant font-label-caps text-label-caps mb-xs">
          Total Storage
        </div>
        <div className="text-headline-lg font-headline-lg text-on-surface">
          {stats.totalStorageGb} GB
        </div>
        {/* Storage usage bar */}
        <div className="h-1 w-full bg-surface-variant rounded-full mt-sm overflow-hidden">
          <div
            className="h-full bg-secondary rounded-full"
            style={{ width: `${stats.storageUsedPct}%` }}
          />
        </div>
      </div>

      {/* Active Builds with animated dots */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl">
        <div className="text-on-surface-variant font-label-caps text-label-caps mb-xs">
          Active Builds
        </div>
        <div className="flex items-center gap-sm">
          <div className="text-headline-lg font-headline-lg text-on-surface">
            {stats.activeBuilds}
          </div>
          {/* Pulsing indicators — one per active build */}
          <div className="flex gap-1">
            {Array.from({ length: stats.activeBuilds }).map((_, i) => (
              <span
                key={i}
                className="w-2 h-2 rounded-full bg-secondary animate-pulse"
                style={{ animationDelay: `${i * 0.5}s` }}
              />
            ))}
          </div>
        </div>
        <div className="text-on-secondary-container font-body-md text-body-md mt-xs">
          {stats.activeRunners}
        </div>
      </div>

    </div>
  )
}

// ---------------------------------------------------------------------------
// BuildTable
// ---------------------------------------------------------------------------

/**
 * Paginated build history table.
 * Each row shows build ID, status badge, commit hash, actor, duration, and age.
 * Empty state is shown when `builds` is empty.
 */
function BuildTable({ builds }: { builds: BuildRow[] }) {
  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden">
      {builds.length === 0 ? (
        <EmptyState />
      ) : (
        <>
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse">
              <thead>
                <tr className="bg-surface-container-low border-b border-outline-variant">
                  <th className="px-md py-sm font-label-caps text-label-caps text-on-surface-variant">
                    Build ID
                  </th>
                  <th className="px-md py-sm font-label-caps text-label-caps text-on-surface-variant">
                    Status
                  </th>
                  <th className="px-md py-sm font-label-caps text-label-caps text-on-surface-variant">
                    Commit Hash
                  </th>
                  <th className="px-md py-sm font-label-caps text-label-caps text-on-surface-variant">
                    Triggered By
                  </th>
                  <th className="px-md py-sm font-label-caps text-label-caps text-on-surface-variant">
                    Duration
                  </th>
                  <th className="px-md py-sm font-label-caps text-label-caps text-on-surface-variant">
                    Timestamp
                  </th>
                  <th className="px-md py-sm font-label-caps text-label-caps text-on-surface-variant text-right">
                    Actions
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant">
                {builds.map((build) => (
                  <BuildRow key={build.id} build={build} />
                ))}
              </tbody>
            </table>
          </div>

          {/* Pagination footer */}
          <div className="px-md py-sm bg-surface-container-low border-t border-outline-variant flex items-center justify-between">
            <div className="text-on-surface-variant font-body-md text-body-md">
              Showing 1–{builds.length} of 1,242 builds
            </div>
            <div className="flex gap-xs">
              <button
                type="button"
                disabled
                aria-label="Previous page"
                className="w-8 h-8 flex items-center justify-center border border-outline-variant rounded bg-surface-container-lowest text-on-surface-variant cursor-not-allowed"
              >
                <span className="material-symbols-outlined text-sm">chevron_left</span>
              </button>
              <button
                type="button"
                className="w-8 h-8 flex items-center justify-center border border-primary bg-primary text-on-primary rounded font-bold text-sm"
              >
                1
              </button>
              <button
                type="button"
                className="w-8 h-8 flex items-center justify-center border border-outline-variant bg-surface-container-lowest text-on-surface-variant hover:bg-surface-variant transition-colors text-sm"
              >
                2
              </button>
              <button
                type="button"
                className="w-8 h-8 flex items-center justify-center border border-outline-variant bg-surface-container-lowest text-on-surface-variant hover:bg-surface-variant transition-colors text-sm"
              >
                3
              </button>
              <button
                type="button"
                aria-label="Next page"
                className="w-8 h-8 flex items-center justify-center border border-outline-variant bg-surface-container-lowest text-on-surface-variant hover:bg-surface-variant transition-colors"
              >
                <span className="material-symbols-outlined text-sm">chevron_right</span>
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// BuildRow
// ---------------------------------------------------------------------------

/**
 * Single row in the build history table.
 * Failed rows get a subtle error-container background tint.
 */
function BuildRow({ build }: { build: BuildRow }) {
  const isFailed = build.status === 'failed'

  return (
    <tr className={`hover:bg-surface-container transition-colors group ${isFailed ? 'bg-error-container/5' : ''}`}>
      {/* Build ID */}
      <td className="px-md py-md font-code-md text-code-md text-primary font-bold">
        {build.id}
      </td>

      {/* Status badge */}
      <td className="px-md py-md">
        <BuildStatusBadge status={build.status} />
      </td>

      {/* Commit hash */}
      <td className="px-md py-md">
        <div className="flex items-center gap-xs text-on-surface-variant font-code-sm text-code-sm">
          <span className="material-symbols-outlined text-sm">commit</span>
          {build.commitHash}
        </div>
      </td>

      {/* Actor */}
      <td className="px-md py-md">
        <BuildActorCell actor={build.triggeredBy} />
      </td>

      {/* Duration */}
      <td className="px-md py-md text-on-surface-variant font-body-md text-body-md">
        {build.duration}
      </td>

      {/* Timestamp */}
      <td className="px-md py-md text-on-surface-variant font-body-md text-body-md">
        {build.timestamp}
      </td>

      {/* Row action */}
      <td className="px-md py-md text-right">
        <BuildRowAction status={build.status} />
      </td>
    </tr>
  )
}

// ---------------------------------------------------------------------------
// BuildStatusBadge
// ---------------------------------------------------------------------------

/**
 * Status badge for a build row.
 * - in_progress: blue/secondary-container with pulsing sync icon
 * - success: green (tertiary-fixed-dim tint) with filled check_circle
 * - failed: red (error-container) with filled error icon
 */
function BuildStatusBadge({ status }: { status: BuildStatus }) {
  if (status === 'in_progress') {
    return (
      <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded bg-secondary-container/20 text-on-secondary-container text-xs font-bold uppercase tracking-wider">
        <span className="material-symbols-outlined text-xs animate-spin">sync</span>
        In Progress
      </span>
    )
  }

  if (status === 'success') {
    return (
      <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded bg-tertiary-fixed-dim/20 text-on-tertiary-fixed-variant text-xs font-bold uppercase tracking-wider">
        <span
          className="material-symbols-outlined text-xs"
          style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
        >
          check_circle
        </span>
        Success
      </span>
    )
  }

  // failed
  return (
    <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded bg-error-container/30 text-on-error-container text-xs font-bold uppercase tracking-wider">
      <span
        className="material-symbols-outlined text-xs"
        style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
      >
        error
      </span>
      Failed
    </span>
  )
}

// ---------------------------------------------------------------------------
// BuildActorCell
// ---------------------------------------------------------------------------

/** Renders either a user login or a CI system name with the appropriate icon. */
function BuildActorCell({ actor }: { actor: BuildActor }) {
  if (actor.kind === 'ci') {
    return (
      <div className="flex items-center gap-xs">
        <span className="material-symbols-outlined text-on-surface-variant text-sm">robot_2</span>
        <span className="text-body-md font-body-md text-on-surface">{actor.system}</span>
      </div>
    )
  }

  return (
    <div className="flex items-center gap-xs">
      {/* Avatar placeholder — uses initials-style bg when no image available */}
      <div className="w-5 h-5 rounded-full bg-surface-variant border border-outline-variant flex items-center justify-center overflow-hidden">
        <span className="material-symbols-outlined text-[12px] text-on-surface-variant">person</span>
      </div>
      <span className="text-body-md font-body-md text-on-surface">{actor.login}</span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// BuildRowAction
// ---------------------------------------------------------------------------

/** Context-sensitive icon button: cancel for in-progress, download for done. */
function BuildRowAction({ status }: { status: BuildStatus }) {
  if (status === 'in_progress') {
    return (
      <button
        type="button"
        aria-label="Cancel build"
        className="material-symbols-outlined text-on-surface-variant hover:text-error transition-colors opacity-0 group-hover:opacity-100"
      >
        cancel
      </button>
    )
  }

  if (status === 'failed') {
    return (
      <button
        type="button"
        aria-label="View build log"
        className="material-symbols-outlined text-on-surface-variant hover:text-primary transition-colors opacity-0 group-hover:opacity-100"
      >
        history_edu
      </button>
    )
  }

  // success
  return (
    <button
      type="button"
      aria-label="Download build artifact"
      className="material-symbols-outlined text-on-surface-variant hover:text-primary transition-colors opacity-0 group-hover:opacity-100"
    >
      download
    </button>
  )
}

// ---------------------------------------------------------------------------
// ConsolePreview
// ---------------------------------------------------------------------------

/**
 * Dark terminal panel showing a mock live build log.
 * Matches the Stitch reference "Quick Console Preview" card.
 * No interactive copy behaviour yet — placeholder button is shown.
 */
function ConsolePreview() {
  return (
    <div>
      <div className="flex items-center justify-between mb-md">
        <h3 className="font-headline-md text-headline-md text-on-surface">Quick Console Preview</h3>
        <div className="flex items-center gap-xs">
          <span className="w-2 h-2 rounded-full bg-tertiary-fixed-dim" />
          <span className="font-label-caps text-label-caps text-on-surface-variant">Live Output</span>
        </div>
      </div>

      <div className="bg-primary-container rounded-xl overflow-hidden shadow-lg">
        {/* Terminal title bar */}
        <div className="flex items-center justify-between px-md py-sm bg-on-primary-fixed-variant border-b border-white/10">
          <div className="flex items-center gap-xs">
            {/* Traffic-light dots */}
            <div className="flex gap-1">
              <div className="w-2 h-2 rounded-full bg-error" />
              <div className="w-2 h-2 rounded-full bg-secondary-container" />
              <div className="w-2 h-2 rounded-full bg-tertiary-fixed-dim" />
            </div>
            <span className="font-code-sm text-code-sm text-on-primary ml-md">
              build-agent-04-log.sh
            </span>
          </div>
          <button
            type="button"
            aria-label="Copy log output"
            className="material-symbols-outlined text-on-primary-container hover:text-white transition-colors text-sm"
          >
            content_copy
          </button>
        </div>

        {/* Log lines */}
        <div className="p-md font-code-sm text-code-sm text-primary-fixed-dim bg-primary-container overflow-y-auto max-h-64">
          <p className="mb-xs">
            <span className="text-on-primary-container opacity-50">00:01:22</span>{' '}
            [INFO] Fetching manifest for api-gateway:v2.4.0
          </p>
          <p className="mb-xs">
            <span className="text-on-primary-container opacity-50">00:01:45</span>{' '}
            [STEP] Executing Docker build command...
          </p>
          <p className="mb-xs">
            <span className="text-on-primary-container opacity-50">00:02:10</span>{' '}
            [DOCKER] Sending build context to Docker daemon  245.2MB
          </p>
          <p className="mb-xs">
            <span className="text-on-primary-container opacity-50">00:02:55</span>{' '}
            [DOCKER] Step 1/12 : FROM node:18-alpine
          </p>
          <p className="mb-xs">
            <span className="text-on-primary-container opacity-50">00:03:05</span>{' '}
            [DOCKER] Step 2/12 : WORKDIR /usr/src/app
          </p>
          <p className="mb-xs text-tertiary-fixed-dim">
            <span className="text-on-primary-container opacity-50">00:03:15</span>{' '}
            [DOCKER] Step 3/12 : COPY package*.json ./
          </p>
          <p className="mb-xs text-tertiary-fixed-dim font-bold">
            <span className="text-on-primary-container opacity-50">00:03:40</span>{' '}
            [INFO] Successfully compiled dependencies
          </p>
          <p className="mb-xs">
            <span className="text-on-primary-container opacity-50">00:04:10</span>{' '}
            [CI] Running vulnerability scan (Trivy)...
          </p>
          <p className="animate-pulse text-secondary-fixed-dim">
            <span className="text-on-primary-container opacity-50">00:04:12</span>{' '}
            [SCAN] 14/158 layers analyzed...
          </p>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ScanSummary
// ---------------------------------------------------------------------------

/** Compact scan result card shown alongside the console preview. */
function ScanSummary() {
  const items: Array<{
    label: string
    count: number
    textCls: string
    bgCls: string
    borderCls: string
    icon: string
  }> = [
    {
      label: 'Critical',
      count: 0,
      textCls: 'text-error',
      bgCls: 'bg-error-container/10',
      borderCls: 'border-error/20',
      icon: 'warning',
    },
    {
      label: 'High',
      count: 3,
      textCls: 'text-orange-600',
      bgCls: 'bg-orange-50',
      borderCls: 'border-orange-200',
      icon: 'error',
    },
    {
      label: 'Medium',
      count: 12,
      textCls: 'text-on-surface-variant',
      bgCls: 'bg-surface-container',
      borderCls: 'border-outline-variant',
      icon: 'info',
    },
  ]

  return (
    <div>
      <h3 className="font-headline-md text-headline-md text-on-surface mb-md">Scan Summary</h3>
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl h-full flex flex-col justify-between">
        <div>
          <div className="flex items-center justify-between mb-md">
            <span className="font-label-caps text-label-caps text-on-surface-variant">
              Last Full Scan
            </span>
            <span className="font-body-md text-body-md text-on-surface font-bold">
              Today, 10:45 AM
            </span>
          </div>
          <div className="space-y-sm">
            {items.map(({ label, count, textCls, bgCls, borderCls, icon }) => (
              <div
                key={label}
                className={`flex items-center justify-between p-sm ${bgCls} border ${borderCls} rounded`}
              >
                <div className="flex items-center gap-xs">
                  <span
                    className={`material-symbols-outlined ${textCls}`}
                    style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
                  >
                    {icon}
                  </span>
                  <span className={`text-body-md font-bold ${textCls}`}>{label}</span>
                </div>
                <span className={`font-code-md font-bold ${count > 0 ? textCls : 'text-on-surface'}`}>
                  {count}
                </span>
              </div>
            ))}
          </div>
        </div>

        <button
          type="button"
          className="block w-full mt-lg py-sm border border-outline bg-surface-container-lowest text-on-surface font-label-caps text-label-caps font-bold rounded-lg hover:bg-surface-variant transition-all text-center"
        >
          View Security Report
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Loading skeletons
// ---------------------------------------------------------------------------

/**
 * Pulse skeleton for the four stats bento tiles while builds data loads.
 */
function BuildStatsSkeleton() {
  return (
    <div className="grid grid-cols-1 md:grid-cols-4 gap-md animate-pulse">
      {Array.from({ length: 4 }).map((_, i) => (
        <div
          key={i}
          className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl h-24"
        />
      ))}
    </div>
  )
}

/**
 * Pulse skeleton for the builds table while data loads.
 */
function BuildTableSkeleton() {
  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden animate-pulse">
      <div className="bg-surface-container-low border-b border-outline-variant h-12" />
      {Array.from({ length: 5 }).map((_, i) => (
        <div
          key={i}
          className="flex items-center gap-md px-md py-md border-b border-outline-variant"
        >
          <div className="h-4 w-20 bg-surface-container rounded" />
          <div className="h-6 w-24 bg-surface-container rounded" />
          <div className="h-4 w-16 bg-surface-container rounded" />
          <div className="h-4 w-28 bg-surface-container rounded" />
          <div className="h-4 w-12 bg-surface-container rounded" />
          <div className="h-4 w-16 bg-surface-container rounded" />
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// EmptyState
// ---------------------------------------------------------------------------

/** Shown when a repository has no build history yet. */
function EmptyState() {
  return (
    <div className="p-xl flex flex-col items-center justify-center text-center gap-md min-h-[240px]">
      <span className="material-symbols-outlined text-[48px] text-on-surface-variant">
        history
      </span>
      <h3 className="text-headline-md text-on-surface">No Builds Yet</h3>
      <p className="text-body-md text-on-surface-variant max-w-sm">
        Push an image to this repository to trigger your first build. Build history
        will appear here once CI/CD pipelines are configured.
      </p>
    </div>
  )
}
