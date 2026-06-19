/**
 * StatCard — one tile in the dashboard stats row.
 *
 * Anatomy (top to bottom):
 *   * Icon chip (tone-tinted) + delta badge in the top-right corner
 *   * Label (text-label-sm uppercase)
 *   * Big number (+ optional unit suffix)
 *   * Sparkline of recent trend (only if `trend` data is provided)
 *
 * The component takes individual props rather than a single `stat`
 * object so the dashboard can wire some tiles to live API data (where
 * `trend` and `deltaPct` aren't available yet — `/api/v1/stats` returns
 * only current values) and others to demo data (which has both).
 *
 * `loading` renders a skeleton with the same overall shape so tiles
 * don't jump as data resolves.
 */
import { ArrowDown, ArrowRight, ArrowUp, HardDrive, Package, Tag, Search, Upload, ShieldAlert, Download } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils/cn'
import { Sparkline } from './Sparkline'

export type StatIconKey =
  | 'package'
  | 'tag'
  | 'scan'
  | 'upload'
  | 'storage'
  | 'shield'
  | 'download'

export type StatTone = 'primary' | 'info' | 'success' | 'warning' | 'danger'

export interface StatCardProps {
  label: string
  value: number
  /** Optional unit shown after the value (e.g. "GB"). */
  unit?: string
  iconKey: StatIconKey
  tone: StatTone
  /** Optional sparkline series (oldest → newest). Omit to hide it. */
  trend?: number[]
  /** Optional percent change vs prior period. Omit to hide the badge. */
  deltaPct?: number
  /** Render a skeleton placeholder instead of data. */
  loading?: boolean
}

const ICONS: Record<StatIconKey, LucideIcon> = {
  package: Package,
  tag: Tag,
  scan: Search,
  upload: Upload,
  storage: HardDrive,
  shield: ShieldAlert,
  download: Download,
}

const TONE_STYLES: Record<StatTone, { chip: string; spark: string }> = {
  primary: { chip: 'bg-primary-soft text-primary',     spark: 'var(--color-primary)' },
  info:    { chip: 'bg-info-100 text-info-500',        spark: 'var(--color-info-500)' },
  success: { chip: 'bg-success-100 text-success-500',  spark: 'var(--color-success-500)' },
  warning: { chip: 'bg-warning-100 text-warning-500',  spark: 'var(--color-warning-500)' },
  danger:  { chip: 'bg-danger-100 text-danger-500',    spark: 'var(--color-danger-500)' },
}

export function StatCard({
  label,
  value,
  unit,
  iconKey,
  tone,
  trend,
  deltaPct,
  loading,
}: StatCardProps) {
  if (loading) {
    return <StatCardSkeleton />
  }

  const Icon = ICONS[iconKey]
  const styles = TONE_STYLES[tone]
  const hasTrend = Array.isArray(trend) && trend.length > 1

  return (
    <div className="flex flex-col gap-md rounded-lg border border-border bg-surface p-lg">
      <div className="flex items-center justify-between">
        <span
          className={cn(
            'inline-flex items-center justify-center w-9 h-9 rounded-sm',
            styles.chip,
          )}
          aria-hidden="true"
        >
          <Icon className="w-[18px] h-[18px]" />
        </span>
        {typeof deltaPct === 'number' && <DeltaBadge deltaPct={deltaPct} />}
      </div>

      <div>
        <div className="text-label-sm uppercase tracking-wider text-on-surface-subtle font-semibold">
          {label}
        </div>
        <div className="mt-xs text-heading-lg font-semibold text-on-surface tabular-nums">
          {value.toLocaleString()}
          {unit && (
            <span className="ml-xs text-heading-sm font-medium text-on-surface-muted">
              {unit}
            </span>
          )}
        </div>
      </div>

      {/* Reserve sparkline space so tiles without trend data still match
          the vertical rhythm of tiles that have it. */}
      <div className="h-9">
        {hasTrend && <Sparkline data={trend!} color={styles.spark} />}
      </div>
    </div>
  )
}

/** Loading skeleton — pulsing rectangles in the same overall layout. */
function StatCardSkeleton() {
  return (
    <div
      role="status"
      aria-label="Loading stat"
      className="flex flex-col gap-md rounded-lg border border-border bg-surface p-lg"
    >
      <div className="flex items-center justify-between">
        <span className="w-9 h-9 rounded-sm bg-surface-muted animate-pulse" />
        <span className="w-10 h-4 rounded-xs bg-surface-muted animate-pulse" />
      </div>
      <div>
        <span className="block w-20 h-3 rounded-xs bg-surface-muted animate-pulse" />
        <span className="block mt-xs w-24 h-7 rounded-xs bg-surface-muted animate-pulse" />
      </div>
      <div className="h-9 w-full rounded-xs bg-surface-muted animate-pulse" />
    </div>
  )
}

/** Delta-vs-prior badge. Up = success, down = danger, zero = neutral. */
function DeltaBadge({ deltaPct }: { deltaPct: number }) {
  if (deltaPct === 0) {
    return (
      <span className="inline-flex items-center gap-xs text-label-sm text-on-surface-subtle font-medium">
        <ArrowRight className="w-3 h-3" aria-hidden="true" />
        Flat
      </span>
    )
  }
  const isUp = deltaPct > 0
  return (
    <span
      className={cn(
        'inline-flex items-center gap-xs text-label-sm font-medium',
        isUp ? 'text-success-500' : 'text-danger-500',
      )}
    >
      {isUp ? (
        <ArrowUp className="w-3 h-3" aria-hidden="true" />
      ) : (
        <ArrowDown className="w-3 h-3" aria-hidden="true" />
      )}
      {Math.abs(deltaPct)}%
    </span>
  )
}
