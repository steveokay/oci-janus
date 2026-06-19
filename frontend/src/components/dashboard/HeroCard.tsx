/**
 * HeroCard — slim warm welcome strip at the top of the dashboard.
 *
 * Compact by design: a single horizontal row with the greeting + summary
 * on the left and the health pill + primary CTA on the right.
 *
 * Time-of-day system:
 *   The current hour buckets into one of four periods — morning,
 *   afternoon, evening, night — each with its own gradient palette and
 *   matching Higgsfield-generated photograph at `/hero/{period}.png`.
 *   The gradient always renders as a fallback so the layout never
 *   shows a blank card if the image fails to load (404, slow network,
 *   etc.); the image fades on top via mix-blend-overlay so even a
 *   moody photograph keeps the left-side text legible.
 *
 * Sprint 1b makes the data props optional so the card renders during
 * the `useStats()` round-trip: missing numbers fall back to em-dashes
 * and the health pill defaults to a neutral "Checking…" state.
 */
import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import {
  ArrowRight,
  ArrowUpRight,
  CloudSun,
  Moon,
  Plus,
  Sun,
  Sunset,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { useLastVisitedRepoStore } from '@/store/lastVisitedRepoStore'
import { cn } from '@/lib/utils/cn'

export interface HeroCardProps {
  /** undefined while stats are loading. */
  repoCount?: number
  /** undefined when no tag-count endpoint exists yet (today). */
  tagCount?: number
  /** Open vulnerabilities, not just criticals — API doesn't split by severity. */
  vulnerabilityCount?: number
  /** Backend health 0–100; undefined while loading. */
  systemHealthPct?: number
  /** Suppresses fallback dashes while the first fetch is in-flight. */
  loading?: boolean
}

type TimeOfDay = 'morning' | 'afternoon' | 'evening' | 'night'

/** Per-period configuration: greeting copy, fallback gradient, image path, weather glyph. */
const PERIODS: Record<
  TimeOfDay,
  { greeting: string; gradient: string; imageUrl: string; Icon: LucideIcon }
> = {
  morning: {
    greeting: 'Good morning',
    gradient:
      'linear-gradient(105deg, oklch(0.92 0.08 55) 0%, oklch(0.95 0.05 35) 45%, oklch(0.99 0.02 60) 85%)',
    imageUrl: '/hero/morning.png',
    Icon: Sun,
  },
  afternoon: {
    greeting: 'Good afternoon',
    gradient:
      'linear-gradient(105deg, oklch(0.93 0.05 230) 0%, oklch(0.96 0.03 220) 45%, oklch(0.99 0.01 60) 85%)',
    imageUrl: '/hero/afternoon.png',
    Icon: CloudSun,
  },
  evening: {
    greeting: 'Good evening',
    gradient:
      'linear-gradient(105deg, oklch(0.86 0.10 35) 0%, oklch(0.89 0.08 350) 45%, oklch(0.96 0.03 280) 85%)',
    imageUrl: '/hero/evening.png',
    Icon: Sunset,
  },
  night: {
    greeting: 'Good evening',
    gradient:
      'linear-gradient(105deg, oklch(0.83 0.05 260) 0%, oklch(0.88 0.04 280) 45%, oklch(0.95 0.02 240) 85%)',
    imageUrl: '/hero/night.png',
    Icon: Moon,
  },
}

export function HeroCard({
  repoCount,
  tagCount,
  vulnerabilityCount,
  systemHealthPct,
  loading,
}: HeroCardProps) {
  const navigate = useNavigate()
  const username = useAuthStore((s) => s.user?.username ?? '')
  const lastVisited = useLastVisitedRepoStore((s) => s.last)
  const period = currentPeriod()
  const config = PERIODS[period]
  const PeriodIcon = config.Icon
  // If the per-period image is missing or 404s, we drop the <img> and
  // fall back to the gradient alone — no broken-icon glyph in the card.
  const [imageBroken, setImageBroken] = useState(false)

  return (
    <section
      aria-labelledby="hero-heading"
      className="relative overflow-hidden rounded-lg border border-border bg-surface"
      data-period={period}
    >
      {/* Light-mode layers: warm gradient + photograph + left-fading
          white veil. All three are hidden in dark mode where the same
          composition would put a white slab on a dark page. */}
      <div
        aria-hidden="true"
        className="absolute inset-0 dark:hidden"
        style={{ backgroundImage: config.gradient }}
      />
      {!imageBroken && (
        <img
          src={config.imageUrl}
          alt=""
          aria-hidden="true"
          onError={() => setImageBroken(true)}
          className="absolute inset-0 w-full h-full object-cover opacity-60 mix-blend-overlay pointer-events-none dark:hidden"
        />
      )}
      <div
        aria-hidden="true"
        className="absolute inset-0 dark:hidden"
        style={{
          background:
            'linear-gradient(105deg, oklch(1 0 0 / 0.65), oklch(1 0 0 / 0.25) 55%, transparent 90%)',
        }}
      />
      {/* Dark-mode layer: a single subtle indigo gradient. We don't
          swap the time-of-day photo here — those images all sit in the
          warm/light family. Could be revisited with a dark photo set. */}
      <div
        aria-hidden="true"
        className="hidden dark:block absolute inset-0"
        style={{
          backgroundImage:
            'linear-gradient(105deg, oklch(0.22 0.06 280) 0%, oklch(0.19 0.04 260) 60%, oklch(0.16 0.03 250) 100%)',
        }}
      />

      <div className="relative flex flex-col gap-md md:flex-row md:items-center md:justify-between px-xl py-2xl">
        <div className="flex-1 min-w-0">
          <h2
            id="hero-heading"
            className="flex items-center gap-sm text-heading-md font-semibold text-on-surface tracking-tight"
          >
            <PeriodIcon
              className="w-5 h-5 text-on-surface-muted dark:text-primary"
              aria-hidden="true"
            />
            <span>
              {config.greeting}
              {username && (
                <>
                  , <span className="text-primary">{username}</span>
                </>
              )}
            </span>
          </h2>
          <p className="mt-xs text-body-sm text-on-surface-muted">
            <Stat
              n={repoCount}
              label={(repoCount ?? 0) === 1 ? 'repository' : 'repositories'}
            />
            <Dot />
            <Stat n={tagCount} label="tags" />
            <Dot />
            <Stat
              n={vulnerabilityCount}
              label={`open ${(vulnerabilityCount ?? 0) === 1 ? 'vulnerability' : 'vulnerabilities'}`}
            />
          </p>
          <ContextualTip
            lastVisited={lastVisited}
            repoCount={repoCount}
            vulnCount={vulnerabilityCount}
            loading={loading}
          />
        </div>

        <div className="flex flex-wrap items-center gap-sm shrink-0">
          <HealthPill
            loading={loading}
            vulnerabilityCount={vulnerabilityCount}
            systemHealthPct={systemHealthPct}
          />
          <button
            type="button"
            onClick={() =>
              navigate({ to: '/repositories', search: { new: true } })
            }
            className="inline-flex items-center gap-xs h-9 px-md rounded-sm bg-primary text-on-primary text-body-sm font-medium shadow-xs hover:bg-primary-600 active:bg-primary-700 transition-colors"
          >
            <Plus className="w-4 h-4" aria-hidden="true" />
            Push image
          </button>
          <a
            href="https://docs.docker.com/registry/"
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-xs h-9 px-md rounded-sm text-body-sm font-medium text-on-surface-muted hover:text-on-surface hover:bg-surface-muted transition-colors"
          >
            Docs
            <ArrowUpRight className="w-3.5 h-3.5" aria-hidden="true" />
          </a>
        </div>
      </div>
    </section>
  )
}

/**
 * ContextualTip — one-line contextual nudge below the stats summary.
 *
 * Waterfall: last-visited link wins → critical-vulns nudge → all-clear
 * → empty-workspace nudge. Loading state skips the line entirely so
 * the hero doesn't flash a misleading "All quiet" message before data
 * lands.
 */
function ContextualTip({
  lastVisited,
  repoCount,
  vulnCount,
  loading,
}: {
  lastVisited: { org: string; repo: string } | null
  repoCount: number | undefined
  vulnCount: number | undefined
  loading: boolean | undefined
}) {
  if (loading) return null

  if (lastVisited) {
    return (
      <p className="mt-md text-body-sm text-on-surface-muted">
        Pick up where you left off →{' '}
        <Link
          to="/repositories/$org/$repo"
          params={{ org: lastVisited.org, repo: lastVisited.repo }}
          className="inline-flex items-center gap-xs font-mono text-code-sm text-primary hover:underline"
        >
          {lastVisited.org}/{lastVisited.repo}
          <ArrowRight className="w-3.5 h-3.5" aria-hidden="true" />
        </Link>
      </p>
    )
  }

  if ((vulnCount ?? 0) > 0) {
    return (
      <p className="mt-md text-body-sm text-warning-500">
        {vulnCount} open{' '}
        {(vulnCount ?? 0) === 1 ? 'vulnerability' : 'vulnerabilities'} —
        worth a look.
      </p>
    )
  }

  if ((repoCount ?? 0) === 0) {
    return (
      <p className="mt-md text-body-sm text-on-surface-muted">
        No repositories yet. Push your first image to get started.
      </p>
    )
  }

  return (
    <p className="mt-md text-body-sm text-on-surface-muted">
      All quiet on the registry front.
    </p>
  )
}

/** Number + label pair, used inline in the hero subtitle. */
function Stat({ n, label }: { n: number | undefined; label: string }) {
  return (
    <>
      <strong className="text-on-surface font-semibold tabular-nums">
        {typeof n === 'number' ? n.toLocaleString() : '—'}
      </strong>{' '}
      {label}
    </>
  )
}

/** Thin separator dot between subtitle stats. */
function Dot() {
  return <span aria-hidden="true" className="mx-sm text-on-surface-subtle">·</span>
}

/**
 * Health pill — green when healthy, amber when vulnerabilities exist,
 * red when the backend reports a degraded health score, neutral while
 * the first fetch resolves.
 */
function HealthPill({
  loading,
  vulnerabilityCount,
  systemHealthPct,
}: {
  loading?: boolean
  vulnerabilityCount?: number
  systemHealthPct?: number
}) {
  if (loading) {
    return (
      <div className="inline-flex items-center gap-sm rounded-full px-md h-9 border bg-neutral-100 border-border text-on-surface-muted">
        <span aria-hidden="true" className="w-2 h-2 rounded-full bg-neutral-400 animate-pulse" />
        <span className="text-label-md font-medium whitespace-nowrap">Checking…</span>
      </div>
    )
  }

  const vulns = vulnerabilityCount ?? 0
  const health = systemHealthPct ?? 100
  const backendOk = health >= 95
  const healthy = vulns === 0 && backendOk

  if (healthy) {
    return (
      <div className="inline-flex items-center gap-sm rounded-full px-md h-9 border bg-success-100 border-success-500/30 text-success-500">
        <span aria-hidden="true" className="w-2 h-2 rounded-full bg-success-500 animate-pulse" />
        <span className="text-label-md font-medium whitespace-nowrap">
          All systems healthy
        </span>
      </div>
    )
  }

  if (!backendOk) {
    return (
      <div className="inline-flex items-center gap-sm rounded-full px-md h-9 border bg-danger-100 border-danger-500/30 text-danger-500">
        <span aria-hidden="true" className="w-2 h-2 rounded-full bg-danger-500" />
        <span className={cn('text-label-md font-medium whitespace-nowrap')}>
          Service issues
        </span>
      </div>
    )
  }

  return (
    <div className="inline-flex items-center gap-sm rounded-full px-md h-9 border bg-warning-100 border-warning-500/30 text-warning-500">
      <span aria-hidden="true" className="w-2 h-2 rounded-full bg-warning-500" />
      <span className="text-label-md font-medium whitespace-nowrap">
        {vulns} open {vulns === 1 ? 'vulnerability' : 'vulnerabilities'}
      </span>
    </div>
  )
}

/**
 * Bucket the current hour into morning / afternoon / evening / night.
 *
 *   5  → 11  morning
 *   12 → 16  afternoon
 *   17 → 20  evening
 *   21 → 4   night (across midnight)
 *
 * Bound on system clock — same caveats as any client-time logic. The
 * worst case is a sunrise gradient at 4:55 a.m. instead of 5:05; not a
 * correctness concern.
 */
function currentPeriod(): TimeOfDay {
  const h = new Date().getHours()
  if (h >= 5 && h < 12)  return 'morning'
  if (h >= 12 && h < 17) return 'afternoon'
  if (h >= 17 && h < 21) return 'evening'
  return 'night'
}
