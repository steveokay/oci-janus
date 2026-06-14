/**
 * dashboard/index.tsx — Operations Overview page.
 *
 * Route: /dashboard (child of /_authenticated layout).
 * Layout: header + 4 stat cards → 2-col content grid → system health row → advanced setup footer.
 * All data is static/mock. Replace with TanStack Query hooks when the management API is ready.
 */

import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiClient } from '@/lib/api/client'

export const Route = createFileRoute('/_authenticated/dashboard/')({
  component: RepositoryDashboard,
})

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type FeaturedFilter = 'ALL' | 'PUBLIC'

/** A single repository item returned by GET /api/v1/repositories */
interface RepoItem {
  name: string               // "org/repo" format
  is_public: boolean
  storage_used_bytes: number
  created_at: string         // ISO 8601 timestamp
}

/** Response shape for GET /api/v1/repositories */
interface RepositoriesResponse {
  repositories: RepoItem[]
  total: number
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

/** Fetch the repository list from the management API. */
async function fetchRepositories(): Promise<RepositoriesResponse> {
  const { data } = await apiClient.get<RepositoriesResponse>('/repositories')
  return data
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Format an ISO timestamp as a human-readable relative time string.
 * Covers seconds, minutes, hours, days, months and years.
 */
function formatRelativeTime(isoString: string): string {
  const diff = Date.now() - new Date(isoString).getTime()
  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  const months = Math.floor(days / 30)
  if (months < 12) return `${months}mo ago`
  return `${Math.floor(months / 12)}y ago`
}

/**
 * Derive a deterministic Tailwind background-colour class from a repo name.
 * Uses a simple character-code sum mod the palette length so the same name
 * always maps to the same colour without needing a hash library.
 */
function repoIconColor(name: string): string {
  const palette = [
    'bg-secondary',
    'bg-primary',
    'bg-tertiary-fixed',
    'bg-on-secondary-container',
    'bg-secondary-container',
    'bg-on-tertiary-container',
    'bg-outline',
  ]
  const sum = name.split('').reduce((acc, ch) => acc + ch.charCodeAt(0), 0)
  return palette[sum % palette.length]
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

function RepositoryDashboard() {
  // Single query for the full repository list — passed down to RecentBuilds
  // so that component doesn't need its own network request.
  const {
    data: reposData,
    isLoading: reposLoading,
  } = useQuery({
    queryKey: ['repositories'],
    queryFn: fetchRepositories,
  })

  return (
    <div className="space-y-xl">
      {/* ── Operations Overview header ──────────────────────────────────── */}
      <section>
        <div className="flex items-center justify-between mb-md">
          <h1 className="text-headline-lg text-on-surface">Operations Overview</h1>
          {/* Live Updates badge — pulsing dot indicates real-time data (mock for now) */}
          <span className="text-on-surface-variant text-[12px] font-medium px-2 py-1 bg-surface-container rounded-lg flex items-center gap-1">
            <span className="w-1.5 h-1.5 rounded-full bg-on-tertiary-container animate-pulse" />
            Live Updates
          </span>
        </div>
        <StatsCards />
      </section>

      {/* ── Main two-column grid ─────────────────────────────────────────── */}
      {/* Left: tables (col-span-2), Right: actions + activity feed (1 col) */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-xl">
        <div className="lg:col-span-2 space-y-xl">
          <FeaturedRepositories />
          <RecentBuilds
            repositories={reposData?.repositories ?? []}
            isLoading={reposLoading}
          />
        </div>
        <div className="space-y-xl">
          <QuickActions />
          <RegistryActivity />
        </div>
      </div>

      {/* ── System health + quick setup row ─────────────────────────────── */}
      {/* SystemHealth returns a div with lg:col-span-2 so it spans 2 of 3 cols */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-xl">
        <SystemHealth />
        <QuickSetup />
      </div>

      {/* ── Advanced Setup footer card (full width) ──────────────────────── */}
      <AdvancedSetup />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Stats Cards
// ---------------------------------------------------------------------------

/**
 * Four metric tiles: Total Repositories, Active Images, Storage Used (with progress bar),
 * Total Downloads. Each uses a bare icon (no circle) + right-aligned badge.
 */
function StatsCards() {
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-md">

      {/* Total Repositories — secondary (blue) icon, green badge */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm">
        <div className="flex items-center justify-between mb-sm">
          <span className="material-symbols-outlined text-secondary">inventory_2</span>
          <span className="text-on-tertiary-container text-[11px] font-bold">+4 this week</span>
        </div>
        <p className="text-label-caps text-on-surface-variant">Total Repositories</p>
        <p className="text-headline-md">124</p>
      </div>

      {/* Active Images — primary (dark) icon, neutral badge */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm">
        <div className="flex items-center justify-between mb-sm">
          <span className="material-symbols-outlined text-primary">layers</span>
          <span className="text-on-surface-variant text-[11px] font-bold">1.2K active</span>
        </div>
        <p className="text-label-caps text-on-surface-variant">Active Images</p>
        <p className="text-headline-md">8,432</p>
      </div>

      {/* Storage Used — inline progress bar under the value */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm">
        <div className="flex items-center justify-between mb-sm">
          <span className="material-symbols-outlined text-on-secondary-container">database</span>
          <span className="text-on-surface-variant text-[11px] font-bold">78% of 5TB</span>
        </div>
        <p className="text-label-caps text-on-surface-variant">Storage Used</p>
        <div className="mt-xs">
          <p className="text-headline-md inline-block">3.9 TB</p>
          {/* bg-secondary (blue) fill on a neutral track */}
          <div className="w-full bg-surface-container h-1.5 rounded-full mt-2 overflow-hidden">
            <div className="bg-secondary h-full" style={{ width: '78%' }} />
          </div>
        </div>
      </div>

      {/* Total Downloads — green icon + green badge */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm">
        <div className="flex items-center justify-between mb-sm">
          <span className="material-symbols-outlined text-on-tertiary-container">download_for_offline</span>
          <span className="text-on-tertiary-container text-[11px] font-bold">+12% vs yesterday</span>
        </div>
        <p className="text-label-caps text-on-surface-variant">Total Downloads (24h)</p>
        <p className="text-headline-md">842K</p>
      </div>

    </div>
  )
}

// ---------------------------------------------------------------------------
// Featured Repositories table
// ---------------------------------------------------------------------------

/**
 * Two-row featured table. Columns: REPOSITORY, STATUS, PULLS, LAST PUSH.
 * ALL / PUBLIC filter tabs in the toolbar (PRIVATE omitted per new design).
 */
function FeaturedRepositories() {
  const [activeFilter, setActiveFilter] = useState<FeaturedFilter>('ALL')

  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden shadow-sm">

      {/* Toolbar */}
      <div className="px-md py-sm border-b border-outline-variant flex items-center justify-between bg-surface-container-low">
        <h3 className="text-label-caps text-on-surface">Featured Repositories</h3>
        <div className="flex gap-md items-center">
          {(['ALL', 'PUBLIC'] as FeaturedFilter[]).map((f) => (
            <button
              key={f}
              type="button"
              onClick={() => setActiveFilter(f)}
              className={[
                'px-3 py-1 text-label-caps rounded',
                activeFilter === f
                  ? 'bg-surface-container-highest border border-outline text-on-surface'
                  : 'text-on-surface-variant hover:bg-surface-variant',
              ].join(' ')}
            >
              {f}
            </button>
          ))}
        </div>
      </div>

      {/* Table */}
      <div className="overflow-x-auto">
        <table className="w-full text-left border-collapse">
          <thead>
            <tr className="bg-surface-container-low/50">
              <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant">REPOSITORY</th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant">STATUS</th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant text-right">PULLS</th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant">LAST PUSH</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-outline-variant">

            <tr className="hover:bg-surface-container transition-colors cursor-pointer">
              <td className="px-lg py-md">
                <div className="flex items-center gap-md">
                  <div className="w-8 h-8 rounded bg-surface-variant flex items-center justify-center">
                    <span className="material-symbols-outlined text-[20px] text-primary">auto_awesome_motion</span>
                  </div>
                  <div>
                    <Link
                      to="/dashboard/$repoName"
                      params={{ repoName: 'web-app' }}
                      className="text-code-md font-bold text-on-surface hover:text-secondary transition-colors"
                    >
                      web-app
                    </Link>
                    <p className="text-[12px] text-on-surface-variant">main-production-v2</p>
                  </div>
                </div>
              </td>
              <td className="px-lg py-md">
                <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-tertiary-fixed text-on-tertiary-fixed text-[11px] font-bold">
                  HEALTHY
                </span>
              </td>
              <td className="px-lg py-md text-right text-code-sm text-on-surface-variant">1.2M</td>
              <td className="px-lg py-md text-body-md text-on-surface-variant">2h ago</td>
            </tr>

            <tr className="hover:bg-surface-container transition-colors cursor-pointer">
              <td className="px-lg py-md">
                <div className="flex items-center gap-md">
                  <div className="w-8 h-8 rounded bg-surface-variant flex items-center justify-center">
                    <span className="material-symbols-outlined text-[20px] text-primary">api</span>
                  </div>
                  <div>
                    <Link
                      to="/dashboard/$repoName"
                      params={{ repoName: 'api-service' }}
                      className="text-code-md font-bold text-on-surface hover:text-secondary transition-colors"
                    >
                      api-service
                    </Link>
                    <p className="text-[12px] text-on-surface-variant">rest-gateway-cluster</p>
                  </div>
                </div>
              </td>
              <td className="px-lg py-md">
                <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-error-container text-on-error-container text-[11px] font-bold">
                  2 CRITICAL
                </span>
              </td>
              <td className="px-lg py-md text-right text-code-sm text-on-surface-variant">892K</td>
              <td className="px-lg py-md text-body-md text-on-surface-variant">14h ago</td>
            </tr>

          </tbody>
        </table>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Recent Activity (formerly "CI/CD Builds") — driven by real repo data
// ---------------------------------------------------------------------------

interface RecentBuildsProps {
  /** All repositories fetched from the management API. */
  repositories: RepoItem[]
  /** True while the parent query is still loading. */
  isLoading: boolean
}

/**
 * RecentBuilds shows the 5 most recently created repositories as activity
 * rows. Each row has a colour-coded icon, repo name, relative-time subtitle,
 * and a green "Active" pill. Clicking a row navigates to the repo detail page.
 */
function RecentBuilds({ repositories, isLoading }: RecentBuildsProps) {
  // Sort descending by created_at, take the first 5.
  const recent = [...repositories]
    .sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
    .slice(0, 5)

  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden shadow-sm">

      {/* Toolbar */}
      <div className="px-md py-sm border-b border-outline-variant flex items-center justify-between bg-surface-container-low">
        <h3 className="text-label-caps text-on-surface">Recent Activity</h3>
        <button type="button" className="text-[11px] font-bold text-primary hover:underline">VIEW ALL</button>
      </div>

      {/* Loading skeleton — 3 grey placeholder rows */}
      {isLoading && (
        <div className="divide-y divide-outline-variant animate-pulse">
          {[0, 1, 2].map((i) => (
            <div key={i} className="flex items-center gap-md px-lg py-md">
              <div className="w-8 h-8 rounded bg-surface-container shrink-0" />
              <div className="flex-1 space-y-1">
                <div className="h-3 bg-surface-container rounded w-1/3" />
                <div className="h-2 bg-surface-container rounded w-1/4" />
              </div>
              <div className="h-5 bg-surface-container rounded w-14" />
            </div>
          ))}
        </div>
      )}

      {/* Empty state */}
      {!isLoading && recent.length === 0 && (
        <div className="px-lg py-xl text-center text-on-surface-variant text-body-md">
          No repositories yet.
        </div>
      )}

      {/* Activity rows */}
      {!isLoading && recent.length > 0 && (
        <div className="divide-y divide-outline-variant">
          {recent.map((repo) => {
            // Derive a stable icon colour from the repo name.
            const colorClass = repoIconColor(repo.name)
            // Use the first letter of the repo name as the icon glyph.
            const initial = repo.name.charAt(0).toUpperCase()

            return (
              <Link
                key={repo.name}
                to="/dashboard/$repoName"
                params={{ repoName: repo.name }}
                className="flex items-center gap-md px-lg py-md hover:bg-surface-container transition-colors"
              >
                {/* Coloured icon square with the repo initial */}
                <div
                  className={`w-8 h-8 rounded flex items-center justify-center shrink-0 ${colorClass}`}
                >
                  <span className="text-on-primary text-xs font-bold leading-none">{initial}</span>
                </div>

                {/* Repo name + "Pushed X ago" subtitle */}
                <div className="flex-1 min-w-0">
                  <p className="text-code-md font-bold text-on-surface truncate">{repo.name}</p>
                  <p className="text-[11px] text-on-surface-variant">
                    Pushed {formatRelativeTime(repo.created_at)}
                  </p>
                </div>

                {/* Active pill */}
                <span className="inline-flex items-center px-2 py-0.5 rounded-full bg-tertiary-fixed text-on-tertiary-fixed text-[10px] font-bold uppercase shrink-0">
                  Active
                </span>
              </Link>
            )
          })}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Quick Actions
// ---------------------------------------------------------------------------

function QuickActions() {
  return (
    <div className="bg-surface-container-low border border-outline-variant rounded-xl p-md shadow-sm">
      <h3 className="text-label-caps text-on-surface-variant mb-md uppercase">Quick Actions</h3>
      <div className="grid grid-cols-1 gap-sm">
        {[
          { icon: 'terminal',   label: 'Push Image Guide' },
          { icon: 'add_box',    label: 'Create New Repo'  },
          { icon: 'person_add', label: 'Invite Member'    },
        ].map(({ icon, label }) => (
          <button
            key={label}
            type="button"
            className="w-full py-2 px-md bg-surface-container-lowest border border-outline-variant rounded-lg text-left hover:bg-surface-variant transition-colors flex items-center gap-md"
          >
            <span className="material-symbols-outlined text-secondary">{icon}</span>
            <span className="text-body-md font-medium">{label}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Registry Activity feed
// ---------------------------------------------------------------------------

function RegistryActivity() {
  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl shadow-sm">
      <div className="p-md border-b border-outline-variant">
        <h3 className="text-label-caps text-on-surface-variant uppercase">Registry Activity</h3>
      </div>
      <div className="p-md space-y-lg">

        {/* Push event — secondary-fixed (light blue) circle */}
        <div className="flex gap-md">
          <div className="w-8 h-8 rounded-full bg-secondary-fixed flex items-center justify-center shrink-0">
            <span className="material-symbols-outlined text-[18px] text-on-secondary-fixed">upload</span>
          </div>
          <div>
            <p className="text-body-md text-on-surface leading-tight">
              Image pushed to <span className="font-bold">core-api</span>
            </p>
            <p className="text-[11px] text-on-surface-variant mt-1">12 minutes ago by Sarah J.</p>
          </div>
        </div>

        {/* Security scan — tertiary-fixed (bright green) circle */}
        <div className="flex gap-md">
          <div className="w-8 h-8 rounded-full bg-tertiary-fixed flex items-center justify-center shrink-0">
            <span className="material-symbols-outlined text-[18px] text-on-tertiary-fixed">security</span>
          </div>
          <div>
            <p className="text-body-md text-on-surface leading-tight">
              Security scan completed for <span className="font-bold">auth-service</span>
            </p>
            <p className="text-[11px] text-on-surface-variant mt-1">
              45 minutes ago • <span className="text-on-tertiary-container">Clean</span>
            </p>
          </div>
        </div>

        {/* New member — surface-variant circle */}
        <div className="flex gap-md">
          <div className="w-8 h-8 rounded-full bg-surface-variant flex items-center justify-center shrink-0">
            <span className="material-symbols-outlined text-[18px] text-primary">person</span>
          </div>
          <div>
            <p className="text-body-md text-on-surface leading-tight">
              New member <span className="font-bold">Julian Barker</span> joined
            </p>
            <p className="text-[11px] text-on-surface-variant mt-1">2 hours ago • Admin Role</p>
          </div>
        </div>

      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// System Health card (spans 2 of 3 grid columns)
// ---------------------------------------------------------------------------

/**
 * Dark card using bg-primary-container (#0d2137 navy). Base text is on-primary (white).
 * Section heading uses on-primary-container (#7689a4) as a muted sub-label colour.
 * The lg:col-span-2 class is on this returned div so it spans correctly in the parent grid.
 */
function SystemHealth() {
  return (
    <div className="lg:col-span-2 bg-primary-container text-on-primary rounded-xl p-md shadow-sm">
      <h3 className="text-label-caps text-on-primary-container mb-md uppercase">System Health</h3>
      <div className="space-y-sm">
        {[
          { dot: 'bg-tertiary-fixed', name: 'Storage Service',  status: 'NOMINAL' },
          { dot: 'bg-tertiary-fixed', name: 'Registry API',     status: 'NOMINAL' },
          { dot: 'bg-tertiary-fixed', name: 'Database Cluster', status: '99.98%'  },
          { dot: 'bg-secondary',      name: 'Scanning Engine',  status: 'BUSY'    },
        ].map(({ dot, name, status }) => (
          <div key={name} className="flex items-center justify-between text-[12px]">
            <span className="flex items-center gap-2">
              <span className={`w-2 h-2 rounded-full ${dot}`} />
              {name}
            </span>
            <span className="text-code-sm">{status}</span>
          </div>
        ))}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Quick Setup card (1 column)
// ---------------------------------------------------------------------------

function QuickSetup() {
  const [copied, setCopied] = useState(false)

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText('cr login registry.acme.io -u $USER')
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard API unavailable in non-secure contexts
    }
  }

  return (
    <div className="bg-surface-container-low border border-outline-variant rounded-xl p-md shadow-sm">
      <h3 className="text-label-caps text-on-surface-variant mb-md uppercase">Quick Setup</h3>
      <div className="space-y-md">
        {/* Inline code snippet with copy button */}
        <div className="bg-surface-container-lowest border border-outline-variant rounded-lg p-2 text-code-sm flex items-center justify-between">
          <code className="text-primary truncate">cr login registry.acme.io -u $USER</code>
          <button
            type="button"
            onClick={handleCopy}
            aria-label="Copy login command"
            className="text-on-surface-variant hover:text-primary transition-colors shrink-0 ml-2"
          >
            <span className="material-symbols-outlined text-[16px]">{copied ? 'check' : 'content_copy'}</span>
          </button>
        </div>
        <div className="flex flex-col gap-2">
          <button type="button" className="w-full py-1.5 bg-primary text-on-primary rounded text-[10px] uppercase font-bold tracking-widest">
            API Documentation
          </button>
          <button type="button" className="w-full py-1.5 border border-outline text-on-surface rounded text-[10px] uppercase font-bold tracking-widest hover:bg-surface-variant transition-colors">
            Security Whitepaper
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Advanced Setup footer card (full width)
// ---------------------------------------------------------------------------

function AdvancedSetup() {
  const [copied, setCopied] = useState(false)

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText('cr login registry.acme.io -u $USER')
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard API unavailable in non-secure contexts
    }
  }

  return (
    <div className="bg-surface-container-highest border border-outline-variant rounded-xl p-lg relative overflow-hidden">
      <div className="relative z-10 grid grid-cols-1 md:grid-cols-2 gap-lg">

        {/* Left: heading, body copy, code block */}
        <div>
          <h3 className="text-headline-md mb-sm text-on-surface">Advanced Setup</h3>
          <p className="text-on-surface-variant text-body-md mb-lg">
            Integrate with your CI/CD flow using our CLI tool or API endpoints.
          </p>
          <div className="bg-surface-container-lowest border border-outline-variant rounded-lg p-md text-code-md flex items-center justify-between">
            <code className="text-primary">cr login registry.acme.io -u $USER</code>
            <button
              type="button"
              onClick={handleCopy}
              aria-label="Copy login command"
              className="text-on-surface-variant hover:text-primary transition-colors"
            >
              <span className="material-symbols-outlined text-[18px]">{copied ? 'check' : 'content_copy'}</span>
            </button>
          </div>
        </div>

        {/* Right: action buttons, bottom-aligned */}
        <div className="flex flex-col justify-end">
          <div className="flex gap-md">
            <button type="button" className="px-md py-2 bg-primary text-on-primary rounded-lg text-label-caps">
              API Documentation
            </button>
            <button type="button" className="px-md py-2 border border-outline text-on-surface rounded-lg text-label-caps hover:bg-surface-variant transition-colors">
              Security Whitepaper
            </button>
          </div>
        </div>

      </div>
    </div>
  )
}
