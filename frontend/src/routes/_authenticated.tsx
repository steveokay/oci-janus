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

import { createFileRoute, Outlet, redirect, Link, useRouterState, useNavigate } from '@tanstack/react-router'
import { useState, useEffect } from 'react'
import axios from 'axios'
import { toast } from 'sonner'
import { apiClient } from '@/lib/api/client'
import { useAuthStore, AuthUser } from '@/store/authStore'

// ---------------------------------------------------------------------------
// Session expiry threshold
// ---------------------------------------------------------------------------

/** How many milliseconds before JWT expiry to attempt silent refresh. */
const SESSION_REFRESH_MS = 60_000

// ---------------------------------------------------------------------------
// Isolated axios instance for token refresh — must NOT share the apiClient
// instance because apiClient's 401 interceptor would redirect to /login,
// creating an infinite loop when the refresh request itself receives a 401.
// ---------------------------------------------------------------------------

const authRefreshClient = axios.create({
  // The Vite dev proxy routes /api to the auth service (port 8080).
  // In production this resolves through the gateway as normal.
  baseURL: '/',
  timeout: 10_000,
  headers: { 'Content-Type': 'application/json' },
})

/**
 * Decodes the JWT payload segment (no signature verification — that is the
 * backend's job). Used only to read exp/sub/tenant_id for Zustand state.
 */
function decodeJwtPayload(token: string): AuthUser {
  const base64 = token.split('.')[1]
  return JSON.parse(atob(base64)) as AuthUser
}

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
  const token = useAuthStore((s) => s.token)
  const setAuth = useAuthStore((s) => s.setAuth)
  const clearAuth = useAuthStore((s) => s.clearAuth)

  // sessionExpiringSoon is kept only as a fallback visual indicator. Under
  // normal operation the silent refresh fires before the user ever sees it.
  const [sessionExpiringSoon, setSessionExpiringSoon] = useState(false)

  useEffect(() => {
    if (!token) return

    // Timers — tracked so we can cancel all of them on cleanup / token change.
    let toastTimerId: ReturnType<typeof setTimeout> | undefined
    let warnTimerId: ReturnType<typeof setTimeout> | undefined
    let refreshTimerId: ReturnType<typeof setTimeout> | undefined

    try {
      // Decode the JWT payload (middle segment) to read the exp claim.
      const payload = JSON.parse(atob(token.split('.')[1])) as { exp: number }
      const msLeft = payload.exp * 1000 - Date.now()

      // ── Silent auto-refresh ─────────────────────────────────────────────
      // Fire 60 seconds before expiry. If the token has already entered the
      // last 60 s window, refresh immediately (msLeft ≤ SESSION_REFRESH_MS).
      const msUntilRefresh = Math.max(msLeft - SESSION_REFRESH_MS, 0)

      refreshTimerId = setTimeout(async () => {
        try {
          const resp = await authRefreshClient.post<{ token: string }>(
            '/api/v1/token/refresh',
            null,
            { headers: { Authorization: `Bearer ${token}` } },
          )
          const newToken = resp.data.token
          const newPayload = decodeJwtPayload(newToken)
          // Update Zustand — apiClient's request interceptor picks up the new
          // token automatically on the next outgoing request.
          setAuth(newToken, newPayload)
          // Hide the expiry warning button if it was already shown.
          setSessionExpiringSoon(false)
        } catch {
          // Refresh failed (network error, 401, etc.) — treat as session end.
          // Mirrors the behaviour of apiClient's 401 interceptor so UX is
          // consistent regardless of which path triggers the session expiry.
          clearAuth()
          window.location.href = '/login?reason=session_expired'
        }
      }, msUntilRefresh)

      // ── 60-second toast warning ──────────────────────────────────────────
      // Kept as a user-visible signal so they are not surprised if a pending
      // form submission falls just before the refresh fires.
      const msUntilToast = msLeft - 60_000
      if (msUntilToast > 0) {
        toastTimerId = setTimeout(() => {
          toast.warning('Renewing your session…')
        }, msUntilToast)
      }

      // ── Fallback expiry-warning button ───────────────────────────────────
      // Shown only if the silent refresh somehow hasn't fired yet. Under
      // normal operation the refreshTimerId fires first and resets this flag.
      const msUntilWarn = msLeft - 90_000
      if (msLeft <= 90_000) {
        setSessionExpiringSoon(true)
      } else {
        warnTimerId = setTimeout(() => setSessionExpiringSoon(true), msUntilWarn)
      }
    } catch {
      // Malformed JWT payload — silently ignore; the API will 401 on the next
      // authenticated request and the 401 interceptor in apiClient will redirect.
    }

    return () => {
      if (toastTimerId !== undefined) clearTimeout(toastTimerId)
      if (warnTimerId !== undefined) clearTimeout(warnTimerId)
      if (refreshTimerId !== undefined) clearTimeout(refreshTimerId)
    }
  }, [token, setAuth, clearAuth])

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
      <TopNavBar sessionExpiringSoon={sessionExpiringSoon} />
      <div className="flex">
        <SideNavBar />
        {/*
         * ml-64 offsets the main content area by the sidebar width (256px = w-64).
         * p-gutter uses the --spacing-gutter custom token (20px) for consistent
         * page gutters. bg-surface ensures the content area matches the shell.
         */}
        <main className="ml-64 flex-1 p-gutter bg-surface min-h-screen">
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

function TopNavBar({ sessionExpiringSoon }: { sessionExpiringSoon: boolean }) {
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

          {/* Session-expiry renewal button — shown when JWT is within 90 seconds of expiry */}
          {sessionExpiringSoon && (
            <Link
              to="/login"
              className="flex items-center gap-1 px-3 py-1 rounded-lg bg-error-container text-on-error-container text-[11px] font-bold animate-pulse hover:opacity-90 transition-opacity"
            >
              <span className="material-symbols-outlined text-[14px]">warning</span>
              Session expiring — renew
            </Link>
          )}

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
  compact?: boolean   // Bottom utility links use smaller vertical padding
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
  const navigate = useNavigate()
  const clearAuth = useAuthStore((s) => s.clearAuth)

  // PENTEST-001-frontend-pair / FE-SEC-007: handleLogout calls the server's
  // /api/v1/logout to revoke the current JTI in Redis, then clears the
  // in-memory Zustand store, then navigates to /login. Steps run in this
  // order so that even if the server call fails (network drop, server down)
  // we still clear the local session — leaving the token in memory after a
  // user-initiated logout would be a worse UX than a token that lingers
  // server-side for the remainder of its 5-minute TTL.
  async function handleLogout() {
    try {
      await apiClient.post('/logout', null)
    } catch {
      // Non-fatal: the server-side revoke is best-effort. The browser tab is
      // about to forget the token anyway (clearAuth); the worst case is a
      // 5-minute window where the JTI remains valid if someone exfiltrated
      // the token earlier. The 401 interceptor in apiClient would normally
      // redirect to /login on a 401, but we don't want that race here — we
      // catch + swallow and continue the logout flow ourselves.
    }
    clearAuth()
    navigate({ to: '/login', replace: true })
  }

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
      <div className="px-sm mb-lg">
        <div className="flex items-center gap-sm mb-base">
          {/*
           * Icon container uses bg-primary-container with a terminal icon
           * to signal this is a developer-facing registry product.
           */}
          <div className="w-8 h-8 bg-primary rounded flex items-center justify-center flex-shrink-0">
            <span
              className="material-symbols-outlined text-on-primary text-[20px]"
              style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
            >
              inventory_2
            </span>
          </div>
          <div className="min-w-0">
            <p className="font-label-caps text-label-caps text-on-surface-variant truncate">Registry Admin</p>
            <p className="text-xs text-on-surface-variant/70 truncate">Production Cluster</p>
          </div>
        </div>
      </div>

      {/* ── Main navigation links ────────────────────────────────────────── */}
      <nav className="flex-1 flex flex-col gap-1 px-sm" aria-label="Main navigation">
        {mainNavItems.map((item) => (
          <SideNavLink key={item.to} item={item} />
        ))}
      </nav>

      {/* ── Primary action — New Repository ─────────────────────────────── */}
      {/*
       * Placed after the main nav items, before the bottom utility links.
       * Uses mt-lg to separate from the nav, mb-md before the divider.
       */}
      <button
        type="button"
        className="mx-sm mt-lg mb-md py-sm bg-primary-container text-on-primary font-label-caps text-label-caps font-bold rounded-lg flex items-center justify-center gap-xs hover:opacity-90 transition-opacity active:scale-95"
      >
        <span className="material-symbols-outlined text-sm">add</span>
        New Repository
      </button>

      {/* ── Bottom utility links ─────────────────────────────────────────── */}
      {/*
       * mt-auto would push too far if the button is already handling spacing.
       * border-t visually separates bottom utility links from the main nav.
       * Logout sits below the utility links with its own divider so it can't
       * be mistaken for a navigation item.
       */}
      <div className="border-t border-outline-variant pt-md flex flex-col gap-1 px-sm">
        {bottomNavItems.map((item) => (
          <SideNavLink key={item.to} item={item} compact />
        ))}
      </div>

      <div className="border-t border-outline-variant mt-md pt-md px-sm">
        <button
          type="button"
          onClick={handleLogout}
          aria-label="Log out"
          className="w-full flex items-center gap-md px-md py-sm rounded-lg text-on-surface-variant hover:bg-surface-variant transition-all"
        >
          <span className="material-symbols-outlined text-[18px]">logout</span>
          <span className="font-label-caps text-label-caps">Log out</span>
        </button>
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
function SideNavLink({ item, compact }: { item: NavItem; compact?: boolean }) {
  return (
    <Link
      to={item.to}
      className={[
        'flex items-center gap-md px-md rounded-lg transition-all',
        compact ? 'py-sm' : 'py-sm',
        item.active
          ? 'bg-secondary-container text-on-secondary-container font-bold'
          : 'text-on-surface-variant hover:bg-surface-variant',
      ].join(' ')}
      aria-current={item.active ? 'page' : undefined}
    >
      <span
        className={`material-symbols-outlined ${compact ? 'text-[18px]' : 'text-[20px]'}`}
        style={item.active ? { fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" } : undefined}
      >
        {item.icon}
      </span>
      <span className="font-label-caps text-label-caps">{item.label}</span>
    </Link>
  )
}
