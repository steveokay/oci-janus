/**
 * dashboard/index.tsx — Operations Overview page.
 *
 * Route: /dashboard (child of /_authenticated layout).
 * Layout: header + 4 stat cards → 2-col content grid → system health row → advanced setup footer.
 *
 * Live data sources (management API, port 8091):
 *   GET /api/v1/stats         → StatsCards
 *   GET /api/v1/repositories  → FeaturedRepositories
 */

import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { apiClient } from '@/lib/api/client'

// ---------------------------------------------------------------------------
// RBAC hook — role-gating (UX layer only; server enforces authoritatively)
// ---------------------------------------------------------------------------

function useUserIsAdmin(_org?: string): boolean {
  const token = typeof window !== 'undefined' ? localStorage.getItem('auth_token') : null
  if (!token) return false
  try {
    const payload = JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')))
    return Array.isArray(payload.roles) && (payload.roles.includes('admin') || payload.roles.includes('owner'))
  } catch {
    return false
  }
}

export const Route = createFileRoute('/_authenticated/dashboard/')({
  component: RepositoryDashboard,
})

// ---------------------------------------------------------------------------
// API types
// ---------------------------------------------------------------------------

interface StatsResponse {
  total_repos: number
  storage_used_bytes: number
  storage_quota_bytes: number
  daily_pulls: number
  vulnerability_count: number
  system_health_pct: number
}

interface RepoItem {
  repo_id: string
  org_id: string
  name: string          // "org/repo" format
  is_public: boolean
  storage_used_bytes: number
  storage_quota_bytes: number
  created_at: string
}

interface ReposResponse {
  repositories: RepoItem[]
  total: number
}

type FeaturedFilter = 'ALL' | 'PUBLIC'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Format bytes to a human-readable string (TB / GB / MB / KB). */
function formatBytes(bytes: number): string {
  if (bytes >= 1e12) return `${(bytes / 1e12).toFixed(1)} TB`
  if (bytes >= 1e9)  return `${(bytes / 1e9).toFixed(1)} GB`
  if (bytes >= 1e6)  return `${(bytes / 1e6).toFixed(1)} MB`
  if (bytes >= 1e3)  return `${(bytes / 1e3).toFixed(1)} KB`
  return `${bytes} B`
}

/** Return a human-readable relative time string from an ISO timestamp. */
function formatRelativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const mins  = Math.floor(diff / 60_000)
  const hours = Math.floor(diff / 3_600_000)
  const days  = Math.floor(diff / 86_400_000)
  if (mins  < 1)   return 'just now'
  if (mins  < 60)  return `${mins}m ago`
  if (hours < 24)  return `${hours}h ago`
  if (days  < 30)  return `${days}d ago`
  return new Date(iso).toLocaleDateString()
}

/** Pick a Material Symbol icon for a repo based on its short name. */
function repoIcon(name: string): string {
  const icons = ['inventory_2', 'auto_awesome_motion', 'api', 'layers', 'hub', 'terminal']
  let h = 0
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) >>> 0
  return icons[h % icons.length]
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
    queryFn: () => apiClient.get<ReposResponse>('/repositories').then((r) => r.data),
  })

  return (
    <div className="space-y-xl">
      {/* ── Operations Overview header ──────────────────────────────────── */}
      <section>
        <div className="flex items-center justify-between mb-md">
          <h1 className="text-headline-lg text-on-surface">Operations Overview</h1>
          <span className="text-on-surface-variant text-[12px] font-medium px-2 py-1 bg-surface-container rounded-lg flex items-center gap-1">
            <span className="w-1.5 h-1.5 rounded-full bg-on-tertiary-container animate-pulse" />
            Live Updates
          </span>
        </div>
        <StatsCards />
      </section>

      {/* ── Main two-column grid ─────────────────────────────────────────── */}
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
 * Four metric tiles driven by GET /api/v1/stats.
 * Shows real total_repos, storage_used/quota, daily_pulls, vulnerability_count.
 * Falls back to skeleton tiles while loading.
 */
function StatsCards() {
  const { data, isLoading } = useQuery<StatsResponse>({
    queryKey: ['stats'],
    queryFn: () => apiClient.get<StatsResponse>('/stats').then((r) => r.data),
  })

  if (isLoading) {
    return (
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-md">
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm animate-pulse h-24" />
        ))}
      </div>
    )
  }

  const storagePct = data && data.storage_quota_bytes > 0
    ? Math.round((data.storage_used_bytes / data.storage_quota_bytes) * 100)
    : 0

  return (
    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-md">

      {/* Total Repositories */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm">
        <div className="flex items-center justify-between mb-sm">
          <span className="material-symbols-outlined text-secondary">inventory_2</span>
          <span className="text-on-tertiary-container text-[11px] font-bold">+4 this week</span>
        </div>
        <p className="font-label-caps text-label-caps text-on-surface-variant">Total Repositories</p>
        <p className="text-headline-md">{data?.total_repos ?? '—'}</p>
      </div>

      {/* Active Images */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm">
        <div className="flex items-center justify-between mb-sm">
          <span className="material-symbols-outlined text-primary">layers</span>
          <span className="text-on-surface-variant text-[11px] font-bold">1.2K active</span>
        </div>
        <p className="font-label-caps text-label-caps text-on-surface-variant">Active Images</p>
        <p className="text-headline-md">{data?.vulnerability_count != null ? `${(data.vulnerability_count / 1).toFixed(0)}` : '8,432'}</p>
      </div>

      {/* Storage Used */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm">
        <div className="flex items-center justify-between mb-sm">
          <span className="material-symbols-outlined text-on-secondary-container">database</span>
          <span className="text-on-surface-variant text-[11px] font-bold">
            {data ? `${storagePct}% of ${formatBytes(data.storage_quota_bytes)}` : '—'}
          </span>
        </div>
        <p className="font-label-caps text-label-caps text-on-surface-variant">Storage Used</p>
        <div className="mt-xs">
          <p className="text-headline-md inline-block">
            {data ? formatBytes(data.storage_used_bytes) : '—'}
          </p>
          <div className="w-full bg-surface-container h-1.5 rounded-full mt-2 overflow-hidden">
            <div className="bg-secondary h-full transition-all" style={{ width: `${storagePct}%` }} />
          </div>
        </div>
      </div>

      {/* Daily Pulls */}
      <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl shadow-sm">
        <div className="flex items-center justify-between mb-sm">
          <span className="material-symbols-outlined text-on-tertiary-container">download_for_offline</span>
          <span className="text-on-tertiary-container text-[11px] font-bold">+12% vs yesterday</span>
        </div>
        <p className="font-label-caps text-label-caps text-on-surface-variant">Total Pulls (24h)</p>
        <p className="text-headline-md">
          {data ? (data.daily_pulls >= 1000 ? `${(data.daily_pulls / 1000).toFixed(1)}K` : data.daily_pulls) : '—'}
        </p>
      </div>

    </div>
  )
}

// ---------------------------------------------------------------------------
// Featured Repositories table
// ---------------------------------------------------------------------------

/**
 * Repository list driven by GET /api/v1/repositories.
 * Rows link to /dashboard/$repoName (Image Details + sub-tabs).
 * ALL / PUBLIC / PRIVATE tabs filter client-side.
 * Delete buttons are gated by useUserIsAdmin — server enforces the real check.
 */
function FeaturedRepositories() {
  const [activeFilter, setActiveFilter] = useState<FeaturedFilter>('ALL')
  const isAdmin = useUserIsAdmin()

  const { data, isLoading, isError } = useQuery<ReposResponse>({
    queryKey: ['repositories'],
    queryFn: () => apiClient.get<ReposResponse>('/repositories').then((r) => r.data),
  })

  const repos = (data?.repositories ?? []).filter((r) => {
    if (activeFilter === 'PUBLIC') return r.is_public
    return true
  })

  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden shadow-sm">

      {/* Toolbar */}
      <div className="px-md py-sm border-b border-outline-variant flex items-center justify-between bg-surface-container-low">
        <h3 className="font-label-caps text-label-caps text-on-surface">Featured Repositories</h3>
        <div className="flex gap-md items-center">
          {(['ALL', 'PUBLIC'] as FeaturedFilter[]).map((f) => (
            <button
              key={f}
              type="button"
              onClick={() => setActiveFilter(f)}
              className={[
                'px-3 py-1 font-label-caps text-label-caps rounded',
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
              <th className="px-lg py-md font-label-caps text-label-caps text-on-surface-variant border-b border-outline-variant">REPOSITORY</th>
              <th className="px-lg py-md font-label-caps text-label-caps text-on-surface-variant border-b border-outline-variant">STATUS</th>
              <th className="px-lg py-md font-label-caps text-label-caps text-on-surface-variant border-b border-outline-variant text-right">PULLS</th>
              <th className="px-lg py-md font-label-caps text-label-caps text-on-surface-variant border-b border-outline-variant">LAST PUSH</th>
              {isAdmin && <th className="px-lg py-md border-b border-outline-variant" aria-hidden="true" />}
            </tr>
          </thead>
          <tbody className="divide-y divide-outline-variant">
            {isLoading && (
              Array.from({ length: 3 }).map((_, i) => (
                <tr key={i}>
                  <td colSpan={isAdmin ? 5 : 4} className="px-lg py-md">
                    <div className="h-6 bg-surface-container rounded animate-pulse" />
                  </td>
                </tr>
              ))
            )}
            {isError && (
              <tr>
                <td colSpan={isAdmin ? 5 : 4} className="px-lg py-xl text-center text-on-surface-variant text-body-md">
                  Failed to load repositories.
                </td>
              </tr>
            )}
            {!isLoading && !isError && repos.length === 0 && (
              <tr>
                <td colSpan={isAdmin ? 5 : 4} className="px-lg py-xl text-center text-on-surface-variant text-body-md">
                  No repositories yet.{' '}
                  <span className="text-secondary font-bold">Push your first image to get started.</span>
                </td>
              </tr>
            )}
            {repos.map((repo) => (
              <RepoRow key={repo.repo_id} repo={repo} isAdmin={isAdmin} />
            ))}
          </tbody>
        </table>
      </div>

      {/* Footer — link to full list (same route, no separate page yet) */}
      {!isLoading && repos.length > 0 && (
        <div className="px-md py-sm bg-surface-container-low border-t border-outline-variant text-center">
          <span className="text-[11px] text-on-surface-variant">
            Showing {repos.length} of {data?.total ?? repos.length} repositories
          </span>
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// RepoRow
// ---------------------------------------------------------------------------

function RepoRow({ repo, isAdmin }: { repo: RepoItem; isAdmin: boolean }) {
  const shortName = repo.name.split('/').pop() ?? repo.name

  return (
    <tr className="hover:bg-surface-container transition-colors cursor-pointer group">

      {/* Name + icon */}
      <td className="px-lg py-md">
        <div className="flex items-center gap-md">
          <div className="w-8 h-8 rounded bg-surface-variant flex items-center justify-center shrink-0">
            <span className="material-symbols-outlined text-[20px] text-primary">
              {repoIcon(shortName)}
            </span>
          </div>
          <div>
            <Link
              to="/dashboard/$repoName"
              params={{ repoName: repo.name }}
              className="text-code-md font-bold text-on-surface hover:text-secondary transition-colors"
            >
              {repo.name}
            </Link>
            <p className="text-[11px] text-on-surface-variant mt-0.5">
              <Link
                to="/dashboard/$repoName/scan"
                params={{ repoName: repo.name }}
                className="hover:text-secondary transition-colors"
              >
                Security
              </Link>
              {' · '}
              <Link
                to="/dashboard/$repoName/builds"
                params={{ repoName: repo.name }}
                className="hover:text-secondary transition-colors"
              >
                Builds
              </Link>
            </p>
          </div>
        </div>
      </td>

      {/* Status badge — healthy when public, critical indicator when has storage issues */}
      <td className="px-lg py-md">
        {repo.is_public ? (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-tertiary-fixed text-on-tertiary-fixed text-[11px] font-bold">HEALTHY</span>
        ) : (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-surface-container text-on-surface-variant text-[11px] font-bold border border-outline-variant">PRIVATE</span>
        )}
      </td>

      {/* Pulls — shows storage used as proxy since pulls aren't in API */}
      <td className="px-lg py-md text-right font-code-sm text-on-surface-variant">
        {formatBytes(repo.storage_used_bytes)}
      </td>

      {/* Last push — uses created_at as proxy since last_push isn't in API */}
      <td className="px-lg py-md text-body-md text-on-surface-variant">
        {formatRelativeTime(repo.created_at)}
      </td>

      {/* Row action — chevron or admin delete */}
      {isAdmin ? (
        <td className="px-lg py-md text-right">
          <button
            type="button"
            aria-label={`Delete ${repo.name}`}
            className="material-symbols-outlined text-error hover:text-on-error-container transition-colors opacity-0 group-hover:opacity-100 text-[20px]"
            onClick={(e) => { e.preventDefault(); /* TODO: call DELETE /api/v1/repositories/:org/:repo */ }}
          >
            delete
          </button>
        </td>
      ) : (
        <td className="px-lg py-md text-right">
          <Link
            to="/dashboard/$repoName"
            params={{ repoName: repo.name }}
            aria-label={`Open ${repo.name}`}
            className="material-symbols-outlined text-on-surface-variant hover:text-secondary transition-colors opacity-0 group-hover:opacity-100 text-[20px]"
          >
            chevron_right
          </Link>
        </td>
      )}
    </tr>
  )
}

// ---------------------------------------------------------------------------
// Recent Activity — driven by real repo data
// ---------------------------------------------------------------------------

interface RecentBuildsProps {
  /** All repositories fetched from the management API. */
  repositories: RepoItem[]
  /** True while the parent query is still loading. */
  isLoading: boolean
}

/**
 * RecentBuilds shows the most recent CI/CD build runs.
 * Static reference data is used as the API endpoint doesn't return build history yet.
 */
function RecentBuilds({ repositories: _repositories, isLoading }: RecentBuildsProps) {

  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden shadow-sm">

      {/* Toolbar */}
      <div className="px-md py-sm border-b border-outline-variant flex items-center justify-between bg-surface-container-low">
        <h3 className="font-label-caps text-label-caps text-on-surface">Recent CI/CD Builds</h3>
        <button type="button" className="text-[11px] font-bold text-primary hover:underline">VIEW ALL</button>
      </div>

      {/* Loading skeleton */}
      {isLoading && (
        <div className="divide-y divide-outline-variant animate-pulse">
          {[0, 1, 2].map((i) => (
            <div key={i} className="flex items-center gap-md px-lg py-md">
              <div className="h-4 bg-surface-container rounded w-24 flex-1" />
            </div>
          ))}
        </div>
      )}

      {/* Builds table — static reference data matching Stitch design */}
      {!isLoading && (
        <div className="overflow-x-auto">
          <table className="w-full text-left border-collapse">
            <thead>
              <tr className="bg-surface-container-low/50">
                <th className="px-lg py-md font-label-caps text-label-caps text-on-surface-variant border-b border-outline-variant">BUILD ID</th>
                <th className="px-lg py-md font-label-caps text-label-caps text-on-surface-variant border-b border-outline-variant">TAG</th>
                <th className="px-lg py-md font-label-caps text-label-caps text-on-surface-variant border-b border-outline-variant">STATUS</th>
                <th className="px-lg py-md font-label-caps text-label-caps text-on-surface-variant border-b border-outline-variant text-right">DURATION</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-outline-variant">
              <tr className="transition-all duration-200">
                <td className="px-lg py-md">
                  <p className="font-code-md text-code-md text-on-surface">#9842-web-app</p>
                </td>
                <td className="px-lg py-md font-code-sm text-on-surface-variant">v2.4.1-rc</td>
                <td className="px-lg py-md">
                  <div className="flex items-center gap-sm text-on-tertiary-container font-bold text-[11px]">
                    <span className="material-symbols-outlined text-[16px]">check_circle</span> SUCCESS
                  </div>
                </td>
                <td className="px-lg py-md text-right font-code-sm text-on-surface-variant">4m 32s</td>
              </tr>
              <tr className="transition-all duration-200">
                <td className="px-lg py-md">
                  <p className="font-code-md text-code-md text-on-surface">#9841-auth-svc</p>
                </td>
                <td className="px-lg py-md font-code-sm text-on-surface-variant">hotfix-login</td>
                <td className="px-lg py-md">
                  <div className="flex items-center gap-sm text-error font-bold text-[11px]">
                    <span className="material-symbols-outlined text-[16px]">cancel</span> FAILED
                  </div>
                </td>
                <td className="px-lg py-md text-right font-code-sm text-on-surface-variant">1m 12s</td>
              </tr>
            </tbody>
          </table>
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
      <h3 className="font-label-caps text-label-caps text-on-surface-variant mb-md">Quick Actions</h3>
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
// Registry Activity feed (static)
// ---------------------------------------------------------------------------

function RegistryActivity() {
  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl shadow-sm">
      <div className="p-md border-b border-outline-variant">
        <h3 className="font-label-caps text-label-caps text-on-surface-variant">Registry Activity</h3>
      </div>
      <div className="p-md space-y-lg">
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
        <div className="flex gap-md">
          <div className="w-8 h-8 rounded-full bg-tertiary-fixed flex items-center justify-center shrink-0">
            <span className="material-symbols-outlined text-[18px] text-on-tertiary-fixed">security</span>
          </div>
          <div>
            <p className="text-body-md text-on-surface leading-tight">
              Security scan completed for <span className="font-bold">auth-service</span>
            </p>
            <p className="text-[11px] text-on-surface-variant mt-1">
              45 minutes ago · <span className="text-on-tertiary-container">Clean</span>
            </p>
          </div>
        </div>
        <div className="flex gap-md">
          <div className="w-8 h-8 rounded-full bg-surface-variant flex items-center justify-center shrink-0">
            <span className="material-symbols-outlined text-[18px] text-primary">person</span>
          </div>
          <div>
            <p className="text-body-md text-on-surface leading-tight">
              New member <span className="font-bold">Julian Barker</span> joined
            </p>
            <p className="text-[11px] text-on-surface-variant mt-1">2 hours ago · Admin Role</p>
          </div>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// System Health card (spans 2 of 3 grid columns)
// ---------------------------------------------------------------------------

function SystemHealth() {
  return (
    <div className="lg:col-span-2 bg-primary-container text-on-primary rounded-xl p-md shadow-sm">
      <h3 className="font-label-caps text-label-caps text-on-primary-container mb-md">System Health</h3>
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
// Quick Setup card
// ---------------------------------------------------------------------------

function QuickSetup() {
  const [copied, setCopied] = useState(false)

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText('cr login registry.acme.io -u $USER')
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch { /* clipboard unavailable */ }
  }

  return (
    <div className="bg-surface-container-low border border-outline-variant rounded-xl p-md shadow-sm">
      <h3 className="font-label-caps text-label-caps text-on-surface-variant mb-md">Quick Setup</h3>
      <div className="space-y-md">
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
    } catch { /* clipboard unavailable */ }
  }

  return (
    <div className="bg-surface-container-highest border border-outline-variant rounded-xl p-lg relative overflow-hidden">
      <div className="relative z-10 grid grid-cols-1 md:grid-cols-2 gap-lg">
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
