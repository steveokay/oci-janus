/**
 * HeroCard — slim warm welcome strip at the top of the dashboard.
 *
 * Compact by design: a single horizontal row with the greeting + summary
 * on the left and the health pill + primary CTA on the right. The
 * gradient echoes the login band (amber → rose) so the visual
 * transition from login → app feels continuous, but without dominating
 * the page the way a full-bleed welcome card would.
 *
 * The previous iteration of this component used `lg:flex-row` with a
 * `max-w-xl` text column. That capped the text width even on wide
 * screens, and on intermediate widths the flex layout collapsed the
 * text column to roughly the width of the longest word, wrapping the
 * paragraph one-word-per-line. The fix: drop the max-width, use
 * `flex-1 min-w-0` so the text takes available space cleanly, and break
 * to a column layout earlier (md, not lg) so cramped mid-widths get
 * the stacked layout instead of a broken row.
 */
import { ArrowUpRight, Plus } from 'lucide-react'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'
import { cn } from '@/lib/utils/cn'

export interface HeroCardProps {
  repoCount: number
  tagCount: number
  criticalCount: number
}

export function HeroCard({ repoCount, tagCount, criticalCount }: HeroCardProps) {
  const username = useAuthStore((s) => s.user?.username ?? '')
  const greeting = timeOfDayGreeting()
  const isHealthy = criticalCount === 0

  return (
    <section
      aria-labelledby="hero-heading"
      className="relative overflow-hidden rounded-lg border border-border bg-surface"
      style={{
        // Same amber → rose family as the login band, dialled down so it
        // tints the card rather than billboarding it. The card's
        // `bg-surface` sits underneath; the gradient fades to transparent
        // on the right so the text always meets a neutral backdrop.
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
            <Stat n={repoCount} label={repoCount === 1 ? 'repository' : 'repositories'} />
            <Dot />
            <Stat n={tagCount} label="tags" />
            <Dot />
            <Stat
              n={criticalCount}
              label={`critical ${criticalCount === 1 ? 'issue' : 'issues'}`}
            />
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-sm shrink-0">
          <HealthPill healthy={isHealthy} criticalCount={criticalCount} />
          <button
            type="button"
            onClick={() =>
              toast.message('Repository create flow coming soon', {
                description: 'Sprint 1c wires the new-repo dialog.',
              })
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
function Stat({ n, label }: { n: number; label: string }) {
  return (
    <>
      <strong className="text-on-surface font-semibold tabular-nums">
        {n.toLocaleString()}
      </strong>{' '}
      {label}
    </>
  )
}

/** Thin separator dot between subtitle stats. */
function Dot() {
  return <span aria-hidden="true" className="mx-sm text-on-surface-subtle">·</span>
}

/** Compact status pill — green when healthy, danger-tinted otherwise. */
function HealthPill({
  healthy,
  criticalCount,
}: {
  healthy: boolean
  criticalCount: number
}) {
  return (
    <div
      className={cn(
        'inline-flex items-center gap-sm rounded-full px-md h-9 border',
        healthy
          ? 'bg-success-100 border-success-500/30 text-success-500'
          : 'bg-danger-100 border-danger-500/30 text-danger-500',
      )}
    >
      <span
        aria-hidden="true"
        className={cn(
          'w-2 h-2 rounded-full',
          healthy ? 'bg-success-500 animate-pulse' : 'bg-danger-500',
        )}
      />
      <span className="text-label-md font-medium whitespace-nowrap">
        {healthy
          ? 'All systems healthy'
          : `${criticalCount} critical ${criticalCount === 1 ? 'issue' : 'issues'}`}
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
