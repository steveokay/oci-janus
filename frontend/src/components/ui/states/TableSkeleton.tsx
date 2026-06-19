/**
 * TableSkeleton — shared pulsing-row placeholder for any table.
 *
 * `widths` controls how many "cells" each row pretends to have and
 * how wide each pulsing rectangle is. Passing a small icon-cell
 * width (e.g. 32) for the first slot mirrors a leading avatar.
 */
import { cn } from '@/lib/utils/cn'

export interface TableSkeletonProps {
  rows?: number
  /** Pixel widths of each "cell" in a row. */
  widths?: number[]
  className?: string
}

export function TableSkeleton({
  rows = 5,
  widths = [160, 240, 100, 120],
  className,
}: TableSkeletonProps) {
  return (
    <div
      role="status"
      aria-label="Loading"
      className={cn(
        'rounded-lg border border-border bg-surface divide-y divide-border',
        className,
      )}
    >
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex items-center gap-md p-lg">
          {widths.map((w, j) => (
            <span
              key={j}
              className="h-4 rounded-xs bg-surface-muted animate-pulse"
              style={{ width: `${w}px`, flex: j === 0 ? '0 0 auto' : '1 1 auto', maxWidth: `${w}px` }}
            />
          ))}
        </div>
      ))}
    </div>
  )
}
