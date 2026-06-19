/**
 * StatCard — one tile in the dashboard stats row.
 *
 * Anatomy (top to bottom):
 *   * Icon chip in a tone-tinted background
 *   * Label (text-label-sm uppercase)
 *   * Big number + delta-vs-prior badge
 *   * Sparkline of the last 12 data points
 *
 * `tone` picks one of the design-token palettes (primary / info /
 * success / warning) so the page gets visual variety without us
 * hand-picking colours per card.
 */
import { ArrowDown, ArrowRight, ArrowUp, HardDrive, Package, Tag, Search, Upload } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils/cn'
import { Sparkline } from './Sparkline'
import type { DemoStat } from '@/lib/demo/dashboardData'

const ICONS: Record<DemoStat['iconKey'], LucideIcon> = {
  package: Package,
  tag: Tag,
  scan: Search,
  upload: Upload,
  storage: HardDrive,
}

const TONE_STYLES: Record<DemoStat['tone'], { chip: string; spark: string }> = {
  primary: {
    chip: 'bg-primary-soft text-primary',
    spark: 'var(--color-primary)',
  },
  info: {
    chip: 'bg-info-100 text-info-500',
    spark: 'var(--color-info-500)',
  },
  success: {
    chip: 'bg-success-100 text-success-500',
    spark: 'var(--color-success-500)',
  },
  warning: {
    chip: 'bg-warning-100 text-warning-500',
    spark: 'var(--color-warning-500)',
  },
}

export function StatCard({ stat }: { stat: DemoStat }) {
  const Icon = ICONS[stat.iconKey]
  const styles = TONE_STYLES[stat.tone]

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
        <DeltaBadge deltaPct={stat.deltaPct} />
      </div>

      <div>
        <div className="text-label-sm uppercase tracking-wider text-on-surface-subtle font-semibold">
          {stat.label}
        </div>
        <div className="mt-xs text-heading-lg font-semibold text-on-surface tabular-nums">
          {stat.value.toLocaleString()}
          {stat.unit && (
            <span className="ml-xs text-heading-sm font-medium text-on-surface-muted">
              {stat.unit}
            </span>
          )}
        </div>
      </div>

      <Sparkline data={stat.trend} color={styles.spark} />
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
