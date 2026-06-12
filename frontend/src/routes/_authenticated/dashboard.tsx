/**
 * dashboard.tsx — Repository Dashboard page.
 *
 * Route path: /dashboard (child of the /_authenticated pathless layout route).
 * This page is the primary landing point after a successful login and shows:
 *   1. Stats cards  — high-level registry metrics at a glance
 *   2. Repo table   — searchable/filterable list of repositories with status
 *   3. Bento cards  — Quick Setup Guide + Vulnerability Scanner shortcuts
 *
 * All data is static/mock in this initial build. In production, replace the
 * MOCK_* constants with TanStack Query hooks that call the registry API.
 */

import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'

// ---------------------------------------------------------------------------
// Route definition
// ---------------------------------------------------------------------------

export const Route = createFileRoute('/_authenticated/dashboard')({
  component: RepositoryDashboard,
})

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Visibility options for the repository filter toolbar. */
type VisibilityFilter = 'ALL' | 'PUBLIC' | 'PRIVATE'

/** Status badge variant — each maps to a distinct colour treatment. */
type RepoStatus = 'HEALTHY' | 'CRITICAL' | 'SCANNED'

/** Shape of a single repository row in the table. */
interface Repository {
  id: string
  icon: string          // Material Symbols icon name
  name: string          // Repository slug (e.g. "web-app")
  subLabel: string      // Descriptive sub-label (e.g. "main-production-v2")
  status: RepoStatus
  criticalCount?: number // Only set when status === 'CRITICAL'
  isPublic: boolean
  pulls: string         // Pre-formatted pull count (e.g. "1.2M")
  stars: number
  lastPushed: string    // Human-readable relative or absolute date
}

// ---------------------------------------------------------------------------
// Mock data — replace with API calls in production
// ---------------------------------------------------------------------------

const MOCK_REPOS: Repository[] = [
  {
    id: 'web-app',
    icon: 'auto_awesome_motion',
    name: 'web-app',
    subLabel: 'main-production-v2',
    status: 'HEALTHY',
    isPublic: true,
    pulls: '1.2M',
    stars: 428,
    lastPushed: '2 hours ago',
  },
  {
    id: 'api-service',
    icon: 'api',
    name: 'api-service',
    subLabel: 'rest-gateway-cluster',
    status: 'CRITICAL',
    criticalCount: 2,
    isPublic: false,
    pulls: '892K',
    stars: 152,
    lastPushed: '14 hours ago',
  },
  {
    id: 'db-proxy',
    icon: 'dns',
    name: 'db-proxy',
    subLabel: 'postgres-lb-v4',
    status: 'SCANNED',
    isPublic: false,
    pulls: '45K',
    stars: 12,
    lastPushed: '3 days ago',
  },
  {
    id: 'cli-tooling',
    icon: 'terminal',
    name: 'cli-tooling',
    subLabel: 'internal-dev-v1',
    status: 'HEALTHY',
    isPublic: false,
    pulls: '122',
    stars: 2,
    lastPushed: 'Jun 12, 2024',
  },
]

// ---------------------------------------------------------------------------
// Page component
// ---------------------------------------------------------------------------

function RepositoryDashboard() {
  return (
    <div className="space-y-xl">
      {/*
       * Page header — just the h1, no subtitle, matching the reference design.
       * The mb-md spacing is applied via the parent space-y-xl.
       */}
      <div className="mb-xl">
        <h1 className="text-headline-lg text-on-surface mb-md">Repositories</h1>

        {/* Stats row — placed directly under the h1 per the reference layout */}
        <StatsCards />
      </div>

      {/* Repository table */}
      <RepositoryTable />

      {/* Bento cards — quick setup + security shortcut */}
      <BentoCards />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Stats Cards
// ---------------------------------------------------------------------------

/**
 * StatsCards renders 4 metric tiles in a responsive grid.
 * Each card pairs a coloured icon circle with a label and value — the icon
 * colour encodes the semantic category (primary=repos, tertiary=volume, etc.)
 */
function StatsCards() {
  return (
    /*
     * 4-column grid matches the reference's md:grid-cols-4 layout.
     * gap-md (16px) between cards as in the reference.
     */
    <div className="grid grid-cols-1 md:grid-cols-4 gap-md">
      {/* Total Repos — secondary-fixed (light blue) circle + on-secondary-fixed text */}
      <StatCard
        icon="storage"
        iconBg="bg-secondary-fixed"
        iconColor="text-on-secondary-fixed"
        label="Total Repos"
        value="124"
      />

      {/* Daily Pulls — tertiary-fixed (bright green) circle + on-tertiary-fixed text */}
      <StatCard
        icon="download"
        iconBg="bg-tertiary-fixed"
        iconColor="text-on-tertiary-fixed"
        label="Daily Pulls"
        value="842K"
      />

      {/*
       * Vulnerabilities — surface-variant circle + on-secondary-container text.
       * Neutral palette gives a "caution" feel without full red alarm.
       */}
      <StatCard
        icon="warning"
        iconBg="bg-surface-variant"
        iconColor="text-on-secondary-container"
        label="Vulnerabilities"
        value="12"
      />

      {/*
       * System Health — primary-fixed circle + on-primary-fixed icon;
       * value text uses on-tertiary-container (green) to visually signal "all good".
       */}
      <StatCard
        icon="speed"
        iconBg="bg-primary-fixed"
        iconColor="text-on-primary-fixed"
        label="System Health"
        value="99.9%"
        valueColor="text-on-tertiary-container"
      />
    </div>
  )
}

/** Props for a single StatCard tile. */
interface StatCardProps {
  icon: string
  iconBg: string
  iconColor: string
  label: string
  value: string
  valueColor?: string // Optional override for the value colour (e.g. green for health)
}

function StatCard({ icon, iconBg, iconColor, label, value, valueColor }: StatCardProps) {
  return (
    /*
     * bg-surface-container-lowest gives the white card face against the
     * bg-surface page background. rounded-xl matches the reference's rounded corners.
     */
    <div className="bg-surface-container-lowest border border-outline-variant p-md rounded-xl flex items-center gap-md">
      {/*
       * w-10 h-10 (40px) circle matches the reference design exactly.
       * rounded-full gives the circular shape used in all four stat cards.
       */}
      <div className={`w-10 h-10 rounded-full ${iconBg} ${iconColor} flex items-center justify-center flex-shrink-0`}>
        <span className="material-symbols-outlined">{icon}</span>
      </div>

      {/* Text stack — label above (label-caps), value below (headline-md) */}
      <div>
        <p className="text-label-caps text-on-surface-variant">{label}</p>
        <p className={`text-headline-md ${valueColor ?? 'text-on-surface'}`}>{value}</p>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Repository Table
// ---------------------------------------------------------------------------

/**
 * RepositoryTable renders the filterable list of repos with a toolbar,
 * column headers, data rows, and a pagination footer.
 *
 * The visibility filter is the only interactive state for this initial build;
 * sorting and pagination are visual-only stubs until the API is wired up.
 */
function RepositoryTable() {
  const [activeFilter, setActiveFilter] = useState<VisibilityFilter>('ALL')

  // Filter repos based on the active visibility tab
  const visibleRepos = MOCK_REPOS.filter((r) => {
    if (activeFilter === 'PUBLIC')  return r.isPublic
    if (activeFilter === 'PRIVATE') return !r.isPublic
    return true // 'ALL' — show everything
  })

  return (
    <div className="bg-surface-container-lowest border border-outline-variant rounded-xl overflow-hidden shadow-sm">

      {/* ── Table toolbar ──────────────────────────────────────────────── */}
      <div className="px-md py-sm border-b border-outline-variant bg-surface-container-low flex items-center justify-between">

        {/*
         * Visibility filter tabs — gap-md (16px) between items matches the
         * reference design. Active tab gets a filled bg + border treatment.
         */}
        <div className="flex gap-md items-center">
          {(['ALL', 'PUBLIC', 'PRIVATE'] as VisibilityFilter[]).map((filter) => (
            <button
              key={filter}
              type="button"
              onClick={() => setActiveFilter(filter)}
              className={[
                'px-3 py-1 rounded text-label-caps transition-colors',
                activeFilter === filter
                  ? 'bg-surface-container-highest border border-outline text-on-surface'
                  : 'text-on-surface-variant hover:bg-surface-variant',
              ].join(' ')}
            >
              {filter}
            </button>
          ))}
        </div>

        {/* Sort indicator — visual-only stub; clicking would open a sort menu */}
        <div className="flex items-center gap-xs text-on-surface-variant">
          <span className="material-symbols-outlined text-[16px]">filter_list</span>
          <span className="text-label-caps">Sort by: Last Pushed</span>
        </div>
      </div>

      {/* ── Column headers ─────────────────────────────────────────────── */}
      <div className="overflow-x-auto">
        <table className="w-full">
          <thead>
            <tr className="border-b border-outline-variant bg-surface-container-low/50">
              <th className="px-lg py-md text-label-caps text-on-surface-variant text-left">
                REPOSITORY
              </th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant text-left">
                STATUS
              </th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant text-left">
                VISIBILITY
              </th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant text-right">
                PULLS
              </th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant text-right">
                STARS
              </th>
              <th className="px-lg py-md text-label-caps text-on-surface-variant text-left">
                LAST PUSHED
              </th>
              {/* Empty header for the row action button column */}
              <th className="px-lg py-md" aria-hidden="true" />
            </tr>
          </thead>

          {/* ── Repository rows ──────────────────────────────────────────── */}
          <tbody className="divide-y divide-outline-variant">
            {visibleRepos.map((repo) => (
              <RepoRow key={repo.id} repo={repo} />
            ))}
          </tbody>
        </table>
      </div>

      {/* ── Pagination footer ──────────────────────────────────────────── */}
      <PaginationFooter total={124} showing={visibleRepos.length} />
    </div>
  )
}

// ---------------------------------------------------------------------------
// RepoRow
// ---------------------------------------------------------------------------

/**
 * Renders a single repository row inside the table.
 * The `group` class on <tr> lets child elements react to row-level hover via
 * `group-hover:*` utilities — used for the opacity-toggled action button.
 */
function RepoRow({ repo }: { repo: Repository }) {
  return (
    <tr className="hover:bg-surface-container transition-colors group">

      {/* ── Repository name + sub-label ──────────────────────────────── */}
      <td className="px-lg py-md">
        <div className="flex items-center gap-md">
          {/*
           * The reference wraps each repo icon in a 32×32 rounded square with
           * bg-surface-variant fill — this container is essential for the
           * card-like icon treatment visible in the design.
           */}
          <div className="w-8 h-8 rounded bg-surface-variant flex items-center justify-center flex-shrink-0">
            <span className="material-symbols-outlined text-[20px] text-primary">
              {repo.icon}
            </span>
          </div>
          <div>
            {/*
             * text-code-md for the name because repo slugs are identifiers —
             * monospace makes them scannable in a list alongside other IDs.
             */}
            <p className="text-code-md font-bold text-on-surface">{repo.name}</p>
            <p className="text-[12px] text-on-surface-variant">{repo.subLabel}</p>
          </div>
        </div>
      </td>

      {/* ── Status badge ─────────────────────────────────────────────── */}
      <td className="px-lg py-md">
        <StatusBadge status={repo.status} criticalCount={repo.criticalCount} />
      </td>

      {/* ── Visibility ───────────────────────────────────────────────── */}
      <td className="px-lg py-md">
        {/*
         * gap-sm (8px) and text-[12px] matches the reference's visibility cell.
         * The text is mixed-case "Public"/"Private" — NOT all-caps, so we avoid
         * using text-label-caps which would force uppercase via text-transform.
         */}
        <div className="flex items-center gap-sm text-on-surface-variant">
          <span className="material-symbols-outlined text-[16px]">
            {repo.isPublic ? 'public' : 'lock'}
          </span>
          <span className="text-[12px] font-medium uppercase">{repo.isPublic ? 'Public' : 'Private'}</span>
        </div>
      </td>

      {/* ── Pull count ───────────────────────────────────────────────── */}
      {/*
       * font-code-sm (JetBrains Mono) for numeric counts — monospace keeps
       * columns aligned and distinguishes metrics from prose labels.
       */}
      <td className="px-lg py-md text-right text-code-sm text-on-surface-variant">
        {repo.pulls}
      </td>

      {/* ── Star count — same monospace treatment as pulls ───────────── */}
      <td className="px-lg py-md text-right text-code-sm text-on-surface-variant">
        {repo.stars}
      </td>

      {/* ── Last pushed ──────────────────────────────────────────────── */}
      <td className="px-lg py-md text-body-md text-on-surface-variant">
        {repo.lastPushed}
      </td>

      {/* ── Row action button ────────────────────────────────────────── */}
      {/*
       * opacity-0 hides the button by default; group-hover:opacity-100 reveals
       * it only when the user hovers the row — avoids visual clutter while still
       * being immediately accessible. transition-opacity for smooth fade-in.
       */}
      <td className="px-md py-md">
        <button
          type="button"
          aria-label={`More options for ${repo.name}`}
          className="opacity-0 group-hover:opacity-100 transition-opacity w-8 h-8 flex items-center justify-center rounded-lg text-on-surface-variant hover:bg-surface-container-high"
        >
          <span className="material-symbols-outlined text-[20px]">more_vert</span>
        </button>
      </td>
    </tr>
  )
}

// ---------------------------------------------------------------------------
// StatusBadge
// ---------------------------------------------------------------------------

/**
 * StatusBadge renders the coloured status pill inside each repository row.
 * The three variants map to distinct MD3 colour roles so the severity can be
 * understood at a glance without reading the label text.
 */
function StatusBadge({ status, criticalCount }: { status: RepoStatus; criticalCount?: number }) {
  if (status === 'HEALTHY') {
    return (
      /*
       * HEALTHY: tertiary-fixed (bright green) background.
       * `rounded` uses our 2px border-radius token — matches the reference's
       * `rounded` class (reference does NOT use pill/rounded-full for badges).
       * px-2 py-0.5 matches the reference's padding exactly.
       */
      <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-tertiary-fixed text-on-tertiary-fixed text-[11px] font-bold">
        {/* Pulsing green dot — animate-pulse gives a subtle heartbeat effect */}
        <span className="w-1.5 h-1.5 rounded-full bg-on-tertiary-fixed animate-pulse" />
        HEALTHY
      </span>
    )
  }

  if (status === 'CRITICAL') {
    return (
      /*
       * CRITICAL: error-container (red-tinted) background with a report icon.
       * The icon reinforces severity without relying solely on colour (accessibility).
       */
      <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-error-container text-on-error-container text-[11px] font-bold">
        <span className="material-symbols-outlined text-[14px]">report</span>
        {criticalCount} CRITICAL
      </span>
    )
  }

  // SCANNED — neutral surface-container background; no icon for a clean "OK" state
  return (
    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-surface-container text-on-surface-variant text-[11px] font-bold">
      SCANNED
    </span>
  )
}

// ---------------------------------------------------------------------------
// PaginationFooter
// ---------------------------------------------------------------------------

/**
 * PaginationFooter renders the count summary and page number controls
 * at the bottom of the repository table.
 * Page navigation is visual-only in this build — wiring it to real data
 * requires a cursor/offset query param in the TanStack Query layer.
 */
function PaginationFooter({ total, showing }: { total: number; showing: number }) {
  return (
    <div className="px-lg py-md bg-surface-container-low border-t border-outline-variant flex items-center justify-between">

      {/* Count summary */}
      <p className="text-body-md text-on-surface-variant">
        Showing {showing} of {total} repositories
      </p>

      {/* Page controls */}
      <div className="flex items-center gap-xs">
        {/* Previous — disabled on page 1 */}
        <button
          type="button"
          aria-label="Previous page"
          disabled
          className="w-8 h-8 flex items-center justify-center rounded-lg text-on-surface-variant opacity-40 cursor-not-allowed"
        >
          <span className="material-symbols-outlined text-[18px]">chevron_left</span>
        </button>

        {/* Page 1 — active */}
        <button
          type="button"
          aria-current="page"
          className="w-8 h-8 flex items-center justify-center rounded-lg bg-primary text-on-primary text-label-caps"
        >
          1
        </button>

        {/* Pages 2, 3 — inactive */}
        {[2, 3].map((page) => (
          <button
            key={page}
            type="button"
            className="w-8 h-8 flex items-center justify-center rounded-lg text-on-surface-variant text-label-caps hover:bg-surface-container transition-colors"
          >
            {page}
          </button>
        ))}

        {/* Next */}
        <button
          type="button"
          aria-label="Next page"
          className="w-8 h-8 flex items-center justify-center rounded-lg text-on-surface-variant hover:bg-surface-container transition-colors"
        >
          <span className="material-symbols-outlined text-[18px]">chevron_right</span>
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Bento Cards
// ---------------------------------------------------------------------------

/**
 * BentoCards is the lower "feature spotlight" row of the dashboard.
 * Uses a 3-column grid where the Quick Setup card spans 2 columns and the
 * Vulnerability Scanner card takes the remaining 1 column.
 */
function BentoCards() {
  const [copied, setCopied] = useState(false)

  /** Copy the pull command to the clipboard and briefly show confirmation. */
  async function handleCopy() {
    try {
      await navigator.clipboard.writeText('docker pull registry.acme.io/web-app:latest')
      setCopied(true)
      // Reset the copied state after 2s so the icon reverts to "copy"
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard API unavailable in non-secure contexts — silently ignore
    }
  }

  return (
    <div className="grid grid-cols-1 md:grid-cols-3 gap-lg">

      {/* ── Quick Setup Guide (spans 2 of 3 columns) ──────────────────── */}
      {/*
       * bg-primary-container gives the deep navy background from the design.
       * overflow-hidden clips the decorative blur blob that extends beyond the
       * card boundary so it doesn't affect the page layout.
       */}
      <div className="md:col-span-2 bg-primary-container text-on-primary rounded-xl p-lg relative overflow-hidden">

        {/* Card header — text matches the reference design exactly */}
        <h3 className="text-headline-md text-on-primary mb-sm">Quick Setup Guide</h3>
        {/*
         * max-w-md in Tailwind v4 can be ~28rem — used here to prevent the paragraph
         * from stretching full-width across the 2-col card on large viewports.
         * We use an inline max-width so it doesn't conflict with any @theme overrides.
         */}
        <p className="text-body-md text-on-primary-container mb-lg" style={{ maxWidth: '28rem' }}>
          Pull any image from this registry directly to your terminal or CI/CD pipeline using the standardized Docker CLI.
        </p>

        {/* Pull command code block */}
        <div className="bg-black/20 backdrop-blur-md border border-white/10 rounded-lg p-md flex items-center justify-between gap-md">
          {/* text-tertiary-fixed (bright green) makes the command pop on the dark navy bg */}
          <code className="text-tertiary-fixed text-code-md">
            docker pull registry.acme.io/web-app:latest
          </code>

          {/*
           * Copy button sits inside the code block for spatial proximity to the
           * command. text-white/60 is subtle by default; hover:text-white makes
           * it clearly interactive on focus.
           */}
          <button
            type="button"
            onClick={handleCopy}
            aria-label="Copy pull command"
            className="text-white/60 hover:text-white transition-colors flex-shrink-0"
          >
            <span className="material-symbols-outlined text-[20px]">
              {copied ? 'check' : 'content_copy'}
            </span>
          </button>
        </div>

        {/*
         * Decorative blur blob — positioned in the bottom-right corner and
         * clipped by the parent's overflow-hidden. The low opacity ensures it
         * doesn't compete with the legible card content.
         */}
        <div
          aria-hidden="true"
          className="absolute right-[-10%] bottom-[-20%] w-64 h-64 bg-secondary opacity-20 blur-[80px] rounded-full pointer-events-none"
        />
      </div>

      {/* ── Vulnerability Scanner card (1 column) ─────────────────────── */}
      <div className="bg-surface-container-lowest border border-outline-variant rounded-xl p-lg flex flex-col justify-between">

        {/* Card header — label and body text match the reference design */}
        <div>
          {/*
           * uppercase label-caps matches the "VULNERABILITY SCANNER" all-caps
           * heading style in the reference HTML.
           */}
          <h4 className="text-label-caps text-on-surface-variant mb-md uppercase">
            Vulnerability Scanner
          </h4>
          <p className="text-body-md mb-lg">
            Security scans are performed automatically on every image push. 12 images currently require attention.
          </p>
        </div>

        {/*
         * CTA button — full-width, outlined style, to keep visual weight secondary.
         * py-2 and the flex centering matches the reference's button treatment.
         */}
        <button
          type="button"
          className="w-full py-2 border border-outline text-on-surface text-label-caps rounded hover:bg-surface-variant transition-colors flex items-center justify-center gap-sm"
        >
          <span className="material-symbols-outlined text-[18px]">security</span>
          VIEW SECURITY REPORT
        </button>
      </div>

    </div>
  )
}
