/**
 * ErrorPanel — shared error-state shell.
 *
 * Used wherever a query fails and we want to keep the page usable
 * (e.g. one panel errors but the rest of the page renders normally).
 * For full-screen "the whole app is wedged" errors, the route-level
 * error boundary handles it instead.
 */
import type { ReactNode } from 'react'
import { TriangleAlert } from 'lucide-react'
import { cn } from '@/lib/utils/cn'

export interface ErrorPanelProps {
  title: string
  description?: ReactNode
  action?: ReactNode
  className?: string
}

export function ErrorPanel({
  title,
  description,
  action,
  className,
}: ErrorPanelProps) {
  return (
    <div
      className={cn(
        'rounded-lg border border-danger-500/30 bg-danger-100 p-lg',
        className,
      )}
    >
      <div className="flex items-start gap-md">
        <span
          aria-hidden="true"
          className="inline-flex items-center justify-center w-10 h-10 rounded-sm bg-danger-500/10 text-danger-500 shrink-0"
        >
          <TriangleAlert className="w-5 h-5" />
        </span>
        <div className="flex-1 min-w-0">
          <h3 className="text-body-md font-semibold text-danger-500">{title}</h3>
          {description && (
            <p className="mt-xs text-body-sm text-danger-500/80">
              {description}
            </p>
          )}
          {action && <div className="mt-md">{action}</div>}
        </div>
      </div>
    </div>
  )
}
