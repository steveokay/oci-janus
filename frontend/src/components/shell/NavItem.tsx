/**
 * NavItem — a single row in the sidebar nav.
 *
 * Two modes:
 *   * `to` provided → renders as a TanStack Router Link with an active
 *     state (highlighted when the current URL matches or is nested under
 *     the link's path).
 *   * `to` omitted  → renders as a disabled-feeling button that shows a
 *     "coming soon" toast on click. We use this for sprint-future
 *     screens so the nav structure is visible from day one without
 *     dead-end 404 links.
 */
import { Link, useLocation } from '@tanstack/react-router'
import { toast } from 'sonner'
import { cn } from '@/lib/utils/cn'

export interface NavItemProps {
  label: string
  icon: React.ReactNode
  /** Route path. Omit for not-yet-built screens. */
  to?: string
  /** Body text on the coming-soon toast (defaults to a generic sentence). */
  comingSoonNote?: string
}

export function NavItem({ label, icon, to, comingSoonNote }: NavItemProps) {
  const { pathname } = useLocation()
  const active = !!to && (pathname === to || pathname.startsWith(`${to}/`))

  const className = cn(
    'group relative flex items-center gap-md w-full',
    'pl-[1.125rem] pr-md py-sm rounded-sm',
    'text-body-sm font-medium transition-colors',
    active
      ? 'bg-primary-soft text-primary'
      : 'text-on-surface-muted hover:bg-surface-muted hover:text-on-surface',
  )

  const content = (
    <>
      {/* Active state accent bar — 3px primary line on the left edge. */}
      {active && (
        <span
          aria-hidden="true"
          className="absolute left-0 top-1.5 bottom-1.5 w-[3px] rounded-full bg-primary"
        />
      )}
      <span className="w-4 h-4 flex items-center justify-center shrink-0" aria-hidden="true">
        {icon}
      </span>
      <span className="flex-1 text-left">{label}</span>
    </>
  )

  if (!to) {
    return (
      <button
        type="button"
        onClick={() =>
          toast.message(`${label} is coming soon`, {
            description: comingSoonNote ?? 'This screen lands in a later sprint.',
          })
        }
        className={cn(className, 'opacity-60 cursor-default')}
      >
        {content}
      </button>
    )
  }

  // `to` is a runtime path; cast satisfies the typed-routes generic without
  // lying — the call sites here all match real `_authenticated/*` routes.
  return (
    <Link to={to as never} className={className}>
      {content}
    </Link>
  )
}
