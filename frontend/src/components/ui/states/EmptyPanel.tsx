/**
 * EmptyPanel — shared empty-state shell.
 *
 * One look across every "you have nothing here yet" surface — repos
 * list, tags list, API keys list, etc. Lets each consumer pass an
 * icon + headline + body + (optional) primary action without
 * re-styling the panel.
 *
 * The `variant="warm"` option keeps the existing repos-list warm-
 * gradient look; `variant="plain"` renders on the standard surface
 * for embeds inside other cards.
 */
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils/cn'

export interface EmptyPanelProps {
  /** Icon node — usually a lucide icon, sized internally. */
  icon?: ReactNode
  /** Illustration replaces the icon chip when set — typically one of
      the SVGs from `components/ui/illustrations.tsx`. */
  illustration?: ReactNode
  title: string
  description?: ReactNode
  action?: ReactNode
  /** Right-hand-side slot — e.g. a CLI snippet card. */
  rightSlot?: ReactNode
  variant?: 'warm' | 'plain'
  className?: string
}

export function EmptyPanel({
  icon,
  illustration,
  title,
  description,
  action,
  rightSlot,
  variant = 'plain',
  className,
}: EmptyPanelProps) {
  const warmGradient =
    'linear-gradient(110deg, oklch(0.97 0.04 60) 0%, oklch(0.98 0.025 350) 55%, oklch(1 0 0) 100%)'

  return (
    <section
      className={cn(
        'relative overflow-hidden rounded-lg border border-border bg-surface',
        className,
      )}
      style={variant === 'warm' ? { backgroundImage: warmGradient } : undefined}
    >
      <div
        className={cn(
          'relative grid gap-lg p-2xl',
          rightSlot ? 'grid-cols-1 lg:grid-cols-5' : 'grid-cols-1',
        )}
      >
        <div
          className={cn(
            'flex flex-col items-start gap-md',
            rightSlot && 'lg:col-span-2 justify-center',
            !rightSlot && 'items-center text-center',
          )}
        >
          {illustration ? (
            <span aria-hidden="true" className="shrink-0">
              {illustration}
            </span>
          ) : icon ? (
            <span
              aria-hidden="true"
              className="inline-flex items-center justify-center w-12 h-12 rounded-md bg-primary-soft text-primary shrink-0"
            >
              {icon}
            </span>
          ) : null}
          <div className={cn('w-full', !rightSlot && 'max-w-prose mx-auto')}>
            <h3 className="text-heading-sm font-semibold text-on-surface">
              {title}
            </h3>
            {description && (
              <p className="mt-xs text-body-sm text-on-surface-muted">
                {description}
              </p>
            )}
          </div>
          {action && <div className="mt-xs">{action}</div>}
        </div>
        {rightSlot && <div className="lg:col-span-3">{rightSlot}</div>}
      </div>
    </section>
  )
}
