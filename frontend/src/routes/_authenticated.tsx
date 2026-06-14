/**
 * _authenticated.tsx — pathless layout route for all authenticated pages.
 *
 * TanStack Router file-based routing treats files prefixed with `_` as
 * "pathless layout routes". This file renders the persistent chrome (TopNav +
 * Sidebar) and an <Outlet /> slot where child routes under _authenticated/
 * render their content. The path segment "_authenticated" does NOT appear in
 * the URL — /dashboard is the real path, not /_authenticated/dashboard.
 *
 * Authentication guard: beforeLoad runs before the component mounts, so any
 * unauthenticated access to a child route is caught before any component
 * renders — avoiding a flash of protected content.
 */

import { createFileRoute, Outlet, redirect, Link, useRouterState } from '@tanstack/react-router'
import { useState, useEffect } from 'react'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'

// ---------------------------------------------------------------------------
// Route definition
// ---------------------------------------------------------------------------

export const Route = createFileRoute('/_authenticated')({
  beforeLoad: () => {
    /*
     * Redirect to /login if no access token is in the Zustand memory store.
     * Using throw redirect() (not navigate()) here because beforeLoad runs
     * synchronously during route resolution — throwing is the correct escape
     * hatch that aborts the current navigation and starts a new one.
     * Token lives in memory only (FE-SEC-001/002) — page reload clears it,
     * which intentionally forces re-login for this ops tool.
     */
    if (!useAuthStore.getState().isAuthenticated()) {
      throw redirect({ to: '/login' })
    }
  },
  component: AuthenticatedLayout,
})

// ---------------------------------------------------------------------------
// Layout component — composes TopNav + SideNav + content Outlet
// ---------------------------------------------------------------------------

function AuthenticatedLayout() {
  // Warn the user 60 seconds before their JWT expires so they can save work.
  const token = useAuthStore((s) => s.token)
  useEffect(() => {
    if (!token) return

    let timerId: ReturnType<typeof setTimeout> | undefined

    try {
      // Decode the JWT payload (middle segment) to read the exp claim.
      const payload = JSON.parse(atob(token.split('.')[1])) as { exp: number }
      // exp is in seconds; warn 60 s before expiry.
      const msUntilWarning = payload.exp * 1000 - Date.now() - 60_000

      if (msUntilWarning > 0) {
        timerId = setTimeout(() => {
          toast.warning('Your session expires in 60 seconds. Please save your work.')
        }, msUntilWarning)
      }
    } catch {
      // Malformed JWT — silently ignore; the API will 401 when it actually expires.
    }

    return () => {
      if (timerId !== undefined) clearTimeout(timerId)
    }
  }, [token])

  return (
    /*
     * bg-surface / text-on-surface are the MD3 semantic surface tokens defined
     * in globals.css. They give the base light background (#f8f9ff) and dark
     * foreground (#0b1c30) across the entire authenticated shell.
     */
    <div
      className="bg-surface text-on-surface min-h-screen"
      style={{ fontFamily: '"Hanken Grotesk", sans-serif' }}
    >
      <TopNavBar />
      <div className="flex">
        <SideNavBar />
        {/*
         * ml-64 offsets the main content area by the sidebar width (256px = w-64).
         * p-gutter uses the --spacing-gutter custom token (20px) for consistent
         * page gutters. bg-surface ensures the content area matches the shell.
         */}
        <main className="ml-64 flex-1 py-gutter px-[56px] bg-surface min-h-screen">
          <div className="max-w-[1440px] mx-auto">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// TopNavBar
// ---------------------------------------------------------------------------

function TopNavBar() {
  const [searchValue, setSearchValue] = useState('')

  return (
    /*
     * sticky top-0 z-50 keeps the nav pinned while the page scrolls.
     * h-16 (64px) is the canonical top bar height used by both the nav and
     * the sidebar top offset (top-16 in SideNavBar).
     */
    <header className="sticky top-0 z-50 h-16 bg-surface border-b border-outline-variant w-full">
      {/* w-full so justify-between spreads items edge-to-edge across the full nav width */}
      <div className="flex items-center justify-between h-full px-gutter w-full">

        {/* ── Left: wordmark + primary nav links ─────────────────────────── */}
        <div className="flex items-center gap-[48px]">
          {/*
           * Wordmark — text only, no icon prefix, matching the reference design.
           * gap-xl (32px) between wordmark and nav links matches the reference's
           * spacing so the top bar doesn't feel cramped on a 1400px viewport.
           */}
          <span className="text-headline-md font-bold text-on-surface">
            ContainerRegistry
          </span>

          {/* Top-level marketing/docs nav — lower visual weight than the wordmark */}
          <nav className="hidden md:flex items-center gap-[32px]">
            <a
              href="#"
              className="text-body-md text-on-surface-variant hover:text-primary transition-colors"
            >
              Explore
            </a>
            <a
              href="#"
              className="text-body-md text-on-surface-variant hover:text-primary transition-colors"
            >
              Pricing
            </a>
            <a
              href="#"
              className="text-body-md text-on-surface-variant hover:text-primary transition-colors"
            >
              Documentation
            </a>
          </nav>
        </div>

        {/* ── Right: search + utility icons + avatar ──────────────────────── */}
        <div className="flex items-center gap-sm">

          {/* Search input — icon embedded inside the field for compact UX */}
          {/*
           * w-64 (256px) matches the reference design's search field width.
           * Placeholder reads "Search registry..." to match reference exactly.
           */}
          <div className="relative hidden lg:block w-64">
            <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant text-[18px]">
              search
            </span>
            <input
              type="text"
              placeholder="Search registry..."
              value={searchValue}
              onChange={(e) => setSearchValue(e.target.value)}
              className="
                w-full pl-10 pr-4 py-1.5
                bg-surface-container-low border border-outline-variant
                rounded-lg text-body-md text-on-surface
                placeholder:text-on-surface-variant
                focus:outline-none focus:ring-1 focus:ring-primary
                transition-all
              "
            />
          </div>

          {/* Notifications icon button */}
          <button
            type="button"
            aria-label="Notifications"
            className="w-9 h-9 flex items-center justify-center rounded-lg text-on-surface-variant hover:bg-surface-container transition-colors"
          >
            <span className="material-symbols-outlined text-[22px]">notifications</span>
          </button>

          {/* Settings icon button */}
          <button
            type="button"
            aria-label="Settings"
            className="w-9 h-9 flex items-center justify-center rounded-lg text-on-surface-variant hover:bg-surface-container transition-colors"
          >
            <span className="material-symbols-outlined text-[22px]">settings</span>
          </button>

          {/*
           * User avatar — circular <img> with a Google Fonts AIDA public image,
           * matching the reference design which uses the same URL.
           * border-outline-variant gives the subtle ring seen in the reference.
           */}
          <img
            src="https://lh3.googleusercontent.com/aida-public/AB6AXuCFvzdMC15-1AyD7IV4by23e5uZ4KB-4nx4PtpjyGRtMksCxDqOh-4794tEXM27xNG-POXkExxMbGv0JJUhxYeD7-fYkAGYNge6y0D2QevSrSynv-uR2ujeeNvF-djOpp04u0QgadUwUoReFPYLXY7tIKuarSaCZWfy9HbtXW2m7OTbclyKnIuwYDmb47L_UGyPRmUOz98Lirc2OBc9C3vnR9nCiOidYzKqMzYV4FECIl_pn_GZ0xtXTtWkn_mfak4R9r2iKf9TN2M"
            alt="User profile"
            className="w-8 h-8 rounded-full border border-outline-variant cursor-pointer"
          />
        </div>
      </div>
    </header>
  )
}

// ---------------------------------------------------------------------------
// SideNavBar
// ---------------------------------------------------------------------------

/**
 * Navigation item descriptor — keeps the nav link list declarative so adding
 * a new section only requires inserting an object, not touching JSX directly.
 */
interface NavItem {
  icon: string        // Material Symbols icon name
  label: string       // Display text
  to: string          // Route path
  active?: boolean    // Highlight as the current page
}

/**
 * SideNavBar renders the fixed left sidebar with:
 *   • Organisation header (icon + cluster name)
 *   • "New Repository" primary action button
 *   • Main nav links (Repositories, Organizations, Images, Security)
 *   • Bottom utility links (Support, API Docs)
 */
function SideNavBar() {
  /*
   * In a real app we'd derive the active route from useRouterState or a
   * useMatch hook. For the initial build we hard-code the active item so the
   * visual state renders correctly without wiring up router subscriptions.
   */
  const routerState = useRouterState()
  const currentPath = routerState.location.pathname

  const mainNavItems: NavItem[] = [
    { icon: 'inventory_2',  label: 'Repositories',  to: '/dashboard',      active: currentPath === '/dashboard' },
    { icon: 'corporate_fare', label: 'Organizations', to: '/organizations', active: currentPath === '/organizations' },
    { icon: 'layers',       label: 'Images',         to: '/images',         active: currentPath === '/images' },
    { icon: 'shield_lock',  label: 'Security',       to: '/security',       active: currentPath === '/security' },
  ]

  const bottomNavItems: NavItem[] = [
    { icon: 'help', label: 'Support',  to: '/support' },
    { icon: 'code', label: 'API Docs', to: '/api-docs' },
  ]

  return (
    /*
     * fixed top-16 accounts for the 64px TopNav height so the sidebar starts
     * flush below it. h-[calc(100vh-64px)] fills the remaining viewport.
     * z-40 sits below the sticky TopNav (z-50) so the nav bar casts a shadow
     * over the sidebar on scroll.
     */
    <aside className="fixed left-0 top-16 h-[calc(100vh-64px)] w-64 bg-surface-container-low border-r border-outline-variant z-40 flex flex-col py-md px-sm">

      {/* ── Organisation header ──────────────────────────────────────────── */}
      <div className="flex items-center gap-sm px-sm mb-md">
        {/*
         * Icon container uses bg-primary-container with a monospace terminal
         * icon to signal this is a developer-facing registry product.
         */}
        <div className="w-8 h-8 bg-primary-container rounded flex items-center justify-center flex-shrink-0">
          <span className="material-symbols-outlined text-on-primary-container text-[16px]">
            terminal
          </span>
        </div>
        <div className="min-w-0">
          <p className="text-label-caps text-on-surface truncate">Registry Admin</p>
          <p className="text-[10px] uppercase tracking-wider text-on-surface-variant truncate">
            Production Cluster
          </p>
        </div>
      </div>

      {/* ── Primary action — New Repository ─────────────────────────────── */}
      {/*
       * The button is mx-md (margin left/right 16px each) so it aligns with
       * the inner content region while still having visual separation from the
       * sidebar edge. mb-lg adds breathing room before the nav links.
       */}
      <button
        type="button"
        className="
          mx-md mb-lg
          flex items-center justify-center gap-xs
          bg-primary text-on-primary
          rounded-lg py-sm
          text-label-caps
          hover:opacity-90 active:scale-[0.98] transition-all
        "
      >
        <span className="material-symbols-outlined text-[18px]">add</span>
        New Repository
      </button>

      {/* ── Main navigation links ────────────────────────────────────────── */}
      <nav className="flex flex-col gap-1 flex-1" aria-label="Main navigation">
        {mainNavItems.map((item) => (
          <SideNavLink key={item.to} item={item} />
        ))}
      </nav>

      {/* ── Bottom utility links ─────────────────────────────────────────── */}
      {/*
       * mt-auto pushes this section to the bottom of the flex column,
       * creating a stable footer region regardless of how many main nav items
       * are above it. border-t visually separates it from the main nav.
       */}
      <div className="mt-auto border-t border-outline-variant pt-sm flex flex-col gap-xs">
        {bottomNavItems.map((item) => (
          <SideNavLink key={item.to} item={item} />
        ))}
      </div>
    </aside>
  )
}

// ---------------------------------------------------------------------------
// SideNavLink — shared nav item renderer
// ---------------------------------------------------------------------------

/**
 * Renders a single sidebar navigation link.
 * Active state gets the MD3 secondary-container highlight treatment.
 * Inactive state uses a subtle hover so the sidebar doesn't compete with
 * the main content area for visual weight.
 */
function SideNavLink({ item }: { item: NavItem }) {
  return (
    <Link
      to={item.to}
      className={[
        'flex items-center gap-md px-md py-2.5 rounded-lg transition-colors',
        item.active
          ? 'bg-secondary-container text-on-secondary-container font-bold'
          : 'text-on-surface-variant hover:bg-surface-variant hover:text-on-surface',
      ].join(' ')}
      aria-current={item.active ? 'page' : undefined}
    >
      <span className="material-symbols-outlined text-[20px]">{item.icon}</span>
      <span className="text-label-caps">{item.label}</span>
    </Link>
  )
}
