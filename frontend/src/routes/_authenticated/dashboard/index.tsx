/**
 * dashboard/index.tsx — Operations Overview page.
 *
 * Route: /dashboard (child of /_authenticated layout).
 * Layout: header + 4 stat cards → 2-col content grid → system health row → advanced setup footer.
 * All data is static/mock. Replace with TanStack Query hooks when the management API is ready.
 */

import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'

// ---------------------------------------------------------------------------
// RBAC hook — role-gating (UX layer only; server enforces authoritatively)
// ---------------------------------------------------------------------------

/**
 * useUserIsAdmin decodes the JWT stored in localStorage and checks whether the
 * caller has an admin or owner role claim. This is UX-layer gating only —
 * the management API re-enforces roles on every request.
 *
 * @param _org - reserved for future per-org role lookups; currently unused
 * @returns true when the decoded JWT payload contains an admin or owner role
 */
// eslint-disable-next-line @typescript-eslint/no-unused-vars
function useUserIsAdmin(_org?: string): boolean {
  // Retrieve the raw JWT from localStorage (set by the auth layer on login).
  const token = typeof window !== 'undefined' ? localStorage.getItem('auth_token') : null
  if (!token) return false
  try {
    // Decode the base64url-encoded middle segment (payload) without verifying the
    // signature — verification is performed server-side on every API call.
    const payload = JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')))
    return (
      Array.isArray(payload.roles) &&
      (payload.roles.includes('admin') || payload.roles.includes('owner'))
    )
  } catch {
    // Malformed token — deny access at the UX layer; the server will reject anyway.
    return false
  }
}

export const Route = createFileRoute('/_authenticated/dashboard/')({
  component: RepositoryDashboard,
})

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type FeaturedFilter = 'ALL' | 'PUBLIC'

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

function RepositoryDashboard() {
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
          <RecentBuilds />
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
 * Two-row featured table. Columns: REPOSITORY, STATUS, PULLS, LAST PUSH, (delete for admins).
 * ALL / PUBLIC filter tabs in the toolbar (PRIVATE omitted per new design).
 * Delete buttons are gated by useUserIsAdmin — server enforces the real check.
 */
function FeaturedRepositories() {
  const [activeFilter, setActiveFilter] = useState<FeaturedFilter>('ALL')
  const isAdmin = useUserIsAdmin()

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
              {/* Delete column is only rendered for admin/owner users */}
              {isAdmin && (
                <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant" />
              )}
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
              {/* Delete button — visible to admins/owners only (UX gating; server enforces) */}
              {isAdmin && (
                <td className="px-lg py-md text-right">
                  <button
                    type="button"
                    aria-label="Delete web-app repository"
                    className="text-error hover:text-on-error-container transition-colors"
                    onClick={(e) => { e.stopPropagation(); /* TODO: wire DELETE /api/v1/repositories/org/web-app */ }}
                  >
                    <span className="material-symbols-outlined text-[18px]">delete</span>
                  </button>
                </td>
              )}
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
              {/* Delete button — visible to admins/owners only (UX gating; server enforces) */}
              {isAdmin && (
                <td className="px-lg py-md text-right">
                  <button
                    type="button"
                    aria-label="Delete api-service repository"
                    className="text-error hover:text-on-error-container transition-colors"
                    onClick={(e) => { e.stopPropagation(); /* TODO: wire DELETE /api/v1/repositories/org/api-service */ }}
                  >
                    <span className="material-symbols-outlined text-[18px]">delete</span>
                  </button>
                </td>
              )}
            </tr>

          </tbody>
        </table>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Recent CI/CD Builds table
// ---------------------------------------------------------------------------

function RecentBuilds() {
  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden shadow-sm">

      <div className="px-md py-sm border-b border-outline-variant flex items-center justify-between bg-surface-container-low">
        <h3 className="text-label-caps text-on-surface">Recent CI/CD Builds</h3>
        <button type="button" className="text-[11px] font-bold text-primary hover:underline">VIEW ALL</button>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-left border-collapse">
          <thead>
            <tr className="bg-surface-container-low/50">
              <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant">BUILD ID</th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant">TAG</th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant">STATUS</th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant border-b border-outline-variant text-right">DURATION</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-outline-variant">

            <tr className="transition-all duration-200">
              <td className="px-lg py-md">
                <p className="text-code-md text-on-surface">#9842-web-app</p>
              </td>
              <td className="px-lg py-md text-code-sm text-on-surface-variant">v2.4.1-rc</td>
              <td className="px-lg py-md">
                {/* on-tertiary-container = green */}
                <div className="flex items-center gap-sm text-on-tertiary-container font-bold text-[11px]">
                  <span className="material-symbols-outlined text-[16px]">check_circle</span>
                  SUCCESS
                </div>
              </td>
              <td className="px-lg py-md text-right text-code-sm text-on-surface-variant">4m 32s</td>
            </tr>

            <tr className="transition-all duration-200">
              <td className="px-lg py-md">
                <p className="text-code-md text-on-surface">#9841-auth-svc</p>
              </td>
              <td className="px-lg py-md text-code-sm text-on-surface-variant">hotfix-login</td>
              <td className="px-lg py-md">
                <div className="flex items-center gap-sm text-error font-bold text-[11px]">
                  <span className="material-symbols-outlined text-[16px]">cancel</span>
                  FAILED
                </div>
              </td>
              <td className="px-lg py-md text-right text-code-sm text-on-surface-variant">1m 12s</td>
            </tr>

          </tbody>
        </table>
      </div>
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
