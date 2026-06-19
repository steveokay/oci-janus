/**
 * ActivityFeed — vertical list of recent registry events.
 *
 * Each row is icon + headline + timestamp. The icon swatches make the
 * feed scannable at a glance — push events get a green up-arrow, scans
 * get an indigo search glyph, etc.
 *
 * Sprint 1b wires real events from registry-audit; until then the
 * component takes a static list from `dashboardData.ts`.
 */
import { FilePlus, Search, Trash2, Upload, UserPlus } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils/cn'
import type { DemoActivity, ActivityKind } from '@/lib/demo/dashboardData'

const KIND_VISUAL: Record<
  ActivityKind,
  { Icon: LucideIcon; chip: string }
> = {
  push:   { Icon: Upload,   chip: 'bg-success-100 text-success-500' },
  scan:   { Icon: Search,   chip: 'bg-primary-soft text-primary' },
  invite: { Icon: UserPlus, chip: 'bg-info-100 text-info-500' },
  create: { Icon: FilePlus, chip: 'bg-warning-100 text-warning-500' },
  delete: { Icon: Trash2,   chip: 'bg-danger-100 text-danger-500' },
}

const SEVERITY_BADGE: Record<NonNullable<DemoActivity['severity']>, string> = {
  low:      'bg-neutral-100 text-on-surface-muted border-border',
  medium:   'bg-info-100 text-info-500 border-info-500/30',
  high:     'bg-warning-100 text-warning-500 border-warning-500/30',
  critical: 'bg-danger-100 text-danger-500 border-danger-500/30',
}

export function ActivityFeed({ items }: { items: DemoActivity[] }) {
  return (
    <section
      aria-labelledby="activity-heading"
      className="flex flex-col rounded-lg border border-border bg-surface"
    >
      <header className="flex items-center justify-between p-lg pb-md border-b border-border">
        <h2
          id="activity-heading"
          className="text-heading-sm font-semibold text-on-surface"
        >
          Recent activity
        </h2>
        <a
          href="#"
          onClick={(e) => e.preventDefault()}
          className="text-label-md text-on-surface-muted hover:text-on-surface transition-colors"
        >
          View all
        </a>
      </header>

      <ul className="divide-y divide-border">
        {items.map((item) => {
          const { Icon, chip } = KIND_VISUAL[item.kind]
          return (
            <li key={item.id} className="flex items-start gap-md p-lg">
              <span
                className={cn(
                  'inline-flex items-center justify-center w-9 h-9 rounded-sm shrink-0',
                  chip,
                )}
                aria-hidden="true"
              >
                <Icon className="w-4 h-4" />
              </span>
              <div className="flex-1 min-w-0">
                <p className="text-body-sm text-on-surface">
                  <span className="font-medium">{item.actor}</span>{' '}
                  <span className="text-on-surface-muted">{item.verb}</span>{' '}
                  <span className="font-medium">{item.object}</span>
                </p>
                <div className="mt-xs flex items-center gap-sm">
                  <span className="text-label-sm text-on-surface-subtle">
                    {item.when}
                  </span>
                  {item.severity && (
                    <span
                      className={cn(
                        'inline-flex items-center px-sm py-0.5 rounded-full border text-label-sm font-medium capitalize',
                        SEVERITY_BADGE[item.severity],
                      )}
                    >
                      {item.severity}
                    </span>
                  )}
                </div>
              </div>
            </li>
          )
        })}
      </ul>
    </section>
  )
}
