/**
 * Topbar — header strip across the content area.
 *
 * Layout is a deliberate 3-column flex:
 *   * Left column   (flex-1): breadcrumbs, left-aligned
 *   * Centre column (fixed):  Cmd+K palette trigger, a real width
 *   * Right column  (flex-1): notification bell + "+ New repository" CTA,
 *                              right-aligned
 *
 * Why fixed width on the centre, not `flex-1 + w-full max-w-md`: the
 * earlier nested-flex approach was fragile at intermediate widths — the
 * trigger could collapse so narrow that the placeholder + kbd wrapped.
 * A real width (`w-96` = 384px) plus `whitespace-nowrap` on the
 * placeholder makes the trigger keep its shape regardless of viewport.
 *
 * The trigger is rendered as a button styled like an input because that's
 * how Stripe/Linear/Vercel signal "this is a search you can also click
 * on". The `⌘ K` chip teaches the shortcut.
 */
import { Bell, Plus, Search } from 'lucide-react'
import { toast } from 'sonner'
import { Breadcrumbs } from './Breadcrumbs'

export function Topbar() {
  return (
    <header className="flex items-center h-14 px-xl border-b border-border bg-surface gap-lg">
      <div className="flex-1 flex items-center min-w-0">
        <Breadcrumbs />
      </div>

      <CommandPaletteTrigger />

      <div className="flex-1 flex items-center justify-end gap-sm min-w-0">
        <NotificationBell />
        <NewRepositoryCTA />
      </div>
    </header>
  )
}

/** Fixed-width centre column. Hidden on narrow screens to free up space. */
function CommandPaletteTrigger() {
  return (
    <button
      type="button"
      onClick={() =>
        toast.message('Command palette coming soon', {
          description: 'Sprint 1f wires ⌘K search across repos, tags, and users.',
        })
      }
      aria-label="Open command palette"
      className="hidden md:flex items-center gap-sm w-96 h-9 px-md rounded-sm border border-border bg-surface-muted/60 hover:bg-surface-muted hover:border-border-strong transition-colors shrink-0"
    >
      <Search
        className="w-4 h-4 text-on-surface-subtle shrink-0"
        aria-hidden="true"
      />
      <span className="flex-1 text-left text-body-sm text-on-surface-subtle whitespace-nowrap overflow-hidden text-ellipsis">
        Search repositories, tags, users…
      </span>
      <kbd className="inline-flex items-center gap-0.5 px-xs h-5 rounded-xs border border-border bg-surface text-label-sm font-mono text-on-surface-muted whitespace-nowrap shrink-0">
        ⌘ K
      </kbd>
    </button>
  )
}

/** Bell with a stub unread badge. Sprint 2 wires it to the events stream. */
function NotificationBell() {
  const unread = 2 // TODO Sprint 2: subscribe to webhook/audit notifications
  return (
    <button
      type="button"
      onClick={() =>
        toast.message('Notifications inbox coming soon', {
          description: 'Sprint 2 surfaces webhook deliveries + scan results here.',
        })
      }
      aria-label={`Notifications (${unread} unread)`}
      className="relative inline-flex items-center justify-center w-9 h-9 rounded-sm text-on-surface-muted hover:text-on-surface hover:bg-surface-muted transition-colors shrink-0"
    >
      <Bell className="w-[18px] h-[18px]" aria-hidden="true" />
      {unread > 0 && (
        <span
          aria-hidden="true"
          className="absolute top-1.5 right-1.5 inline-flex items-center justify-center min-w-[16px] h-4 px-1 rounded-full bg-danger-500 text-white text-[10px] font-semibold leading-none tabular-nums"
        >
          {unread > 9 ? '9+' : unread}
        </span>
      )}
    </button>
  )
}

/** Primary CTA. Toast for now; Sprint 1c wires the new-repo dialog. */
function NewRepositoryCTA() {
  return (
    <button
      type="button"
      onClick={() =>
        toast.message('New repository flow coming soon', {
          description: 'Sprint 1c wires the create-repo dialog.',
        })
      }
      className="inline-flex items-center gap-xs h-9 px-md rounded-sm bg-primary text-on-primary text-body-sm font-medium shadow-xs hover:bg-primary-600 active:bg-primary-700 transition-colors shrink-0 whitespace-nowrap"
    >
      <Plus className="w-4 h-4" aria-hidden="true" />
      <span className="hidden sm:inline">New repository</span>
    </button>
  )
}
