/**
 * HeroCard — slim warm welcome strip at the top of the dashboard.
 *
 * Compact by design: a single horizontal row with the greeting + summary
 * on the left and the health pill + primary CTA on the right. The
 * gradient echoes the login band (amber → rose) so the visual transition
 * from login → app feels continuous, but without dominating the page.
 *
 * Sprint 1b makes the data props optional so the card renders during the
 * `useStats()` round-trip: missing numbers fall back to em-dashes and
 * the health pill defaults to a neutral "Checking…" state. The tag
 * count is still a placeholder — `/api/v1/stats` doesn't expose tag
 * totals today (would need a new metadata RPC), so we display "—" rather
 * than show a stale demo number alongside real ones.
 */
import { useNavigate } from '@tanstack/react-router'
import { ArrowUpRight, Plus } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
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

export function HeroCard({
  repoCount,
  tagCount,
  vulnerabilityCount,
  systemHealthPct,
  loading,
}: HeroCardProps) {
  const navigate = useNavigate()
  const username = useAuthStore((s) => s.user?.username ?? '')
  const greeting = timeOfDayGreeting()

  return (
    <section
      aria-labelledby="hero-heading"
      className="relative overflow-hidden rounded-lg border border-border bg-surface"
      style={{
        backgroundImage:
          'linear-gradient(105deg, oklch(0.96 0.05 60) 0%, oklch(0.97 0.03 350) 45%, oklch(1 0 0) 85%)',
      }}
    >
      <div className="flex flex-col gap-md md:flex-row md:items-center md:justify-between p-lg">
        <div className="flex-1 min-w-0">
          <h2
            id="hero-heading"
            className="text-heading-md font-semibold text-on-surface tracking-tight"
          >
            {greeting}
            {username && (
              <>
                , <span className="text-primary">{username}</span>
              </>
            )}
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
 *
 * Healthy means: 0 open vulns AND backend self-reports ≥95%.
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

/** Client time is fine here — no SSR. */
function timeOfDayGreeting(): string {
  const h = new Date().getHours()
  if (h < 5) return 'Good evening'
  if (h < 12) return 'Good morning'
  if (h < 17) return 'Good afternoon'
  return 'Good evening'
}
