/**
 * $repoName.tsx — Layout route for /dashboard/:repoName and its children.
 *
 * Wraps all three per-repo screens with a shared sub-navigation tab bar:
 *   Tags   → /_authenticated/dashboard/$repoName/       (index)
 *   Security → /_authenticated/dashboard/$repoName/scan
 *   Builds → /_authenticated/dashboard/$repoName/builds
 *
 * The active tab is determined by TanStack Router's `<Link activeProps>` which
 * applies the active class when the link's href matches the current pathname.
 * An `<Outlet />` renders the matched child route below the tab bar.
 */

import { createFileRoute, Link, Outlet, useParams } from '@tanstack/react-router'

export const Route = createFileRoute('/_authenticated/dashboard/$repoName')({
  component: RepoLayout,
})

// ---------------------------------------------------------------------------
// Sub-navigation tab configuration
// ---------------------------------------------------------------------------

/**
 * Describes one tab in the per-repo sub-navigation bar.
 * `to` must match the TanStack Router route path for the child screen.
 */
interface RepoTab {
  /** Display label rendered inside the tab. */
  label: string
  /** Material Symbols Outlined icon name. */
  icon: string
  /** TanStack Router `to` path. Use '.' for the index child. */
  to: '/dashboard/$repoName' | '/dashboard/$repoName/scan' | '/dashboard/$repoName/builds'
  /** Whether this tab links to the index (exact-match) child route. */
  isIndex?: boolean
}

const REPO_TABS: RepoTab[] = [
  {
    label: 'Tags',
    icon: 'sell',
    to: '/dashboard/$repoName',
    isIndex: true,
  },
  {
    label: 'Security',
    icon: 'security',
    to: '/dashboard/$repoName/scan',
  },
  {
    label: 'Builds',
    icon: 'bolt',
    to: '/dashboard/$repoName/builds',
  },
]

// ---------------------------------------------------------------------------
// Layout component
// ---------------------------------------------------------------------------

function RepoLayout() {
  const { repoName } = useParams({ from: '/_authenticated/dashboard/$repoName' })

  return (
    <div className="space-y-xl">
      {/* ── Sub-navigation tab bar ──────────────────────────────────────── */}
      {/*
       * Tab bar sits at the top of every repo sub-page above the Outlet.
       * Active tab uses `bg-secondary-container text-on-secondary-container`
       * to match the sidebar's selected-item pattern from the Stitch design.
       * Inactive tabs use muted `text-on-surface-variant` with a hover tint.
       */}
      <nav
        aria-label="Repository sections"
        className="flex items-center gap-xs border-b border-outline-variant pb-0 overflow-x-auto"
      >
        {REPO_TABS.map(({ label, icon, to, isIndex }) => (
          <Link
            key={label}
            to={to}
            params={{ repoName }}
            /*
             * For the index child (Tags), use `exact: true` so the Security
             * and Builds tabs do not also appear active when their routes are
             * matched (TanStack Router Link is active when the href is a prefix
             * of the current path unless `exact` is used).
             */
            activeOptions={isIndex ? { exact: true } : undefined}
            activeProps={{
              className:
                'flex items-center gap-xs px-md py-sm rounded-t-lg border-b-2 border-primary text-primary font-bold bg-surface-container-low',
            }}
            inactiveProps={{
              className:
                'flex items-center gap-xs px-md py-sm rounded-t-lg border-b-2 border-transparent text-on-surface-variant hover:text-primary hover:bg-surface-container-low transition-colors font-label-caps text-label-caps',
            }}
          >
            <span className="material-symbols-outlined text-[20px]">{icon}</span>
            {label}
          </Link>
        ))}
      </nav>

      {/* ── Child route content ─────────────────────────────────────────── */}
      <Outlet />
    </div>
  )
}
