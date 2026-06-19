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
import { useNavigate } from '@tanstack/react-router'
import { Plus, Search } from 'lucide-react'
import { Breadcrumbs } from './Breadcrumbs'
import { usePaletteStore } from '@/store/paletteStore'

export function Topbar() {
  return (
    <header className="flex items-center h-14 px-xl border-b border-border bg-surface gap-lg">
      <div className="flex-1 flex items-center min-w-0">
        <Breadcrumbs />
      </div>

      <CommandPaletteTrigger />

      <div className="flex-1 flex items-center justify-end gap-sm min-w-0">
        {/* NotificationBell hidden until the events stream lands
            (FE-API-008). The previous hardcoded "2" badge was the most
            honest-looking thing on the page that wasn't real. */}
        <NewRepositoryCTA />
      </div>
    </header>
  )
}

/** Fixed-width centre column. Hidden on narrow screens to free up space. */
function CommandPaletteTrigger() {
  const openPalette = usePaletteStore((s) => s.setOpen)
  return (
    <button
      type="button"
      onClick={() => openPalette(true)}
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


/** Primary CTA — navigates to the repositories list with ?new=true so
    the create dialog opens on arrival. */
function NewRepositoryCTA() {
  const navigate = useNavigate()
  return (
    <button
      type="button"
      onClick={() =>
        navigate({ to: '/repositories', search: { new: true } })
      }
      className="inline-flex items-center gap-xs h-9 px-md rounded-sm bg-primary text-on-primary text-body-sm font-medium shadow-xs hover:bg-primary-600 active:bg-primary-700 transition-colors shrink-0 whitespace-nowrap"
    >
      <Plus className="w-4 h-4" aria-hidden="true" />
      <span className="hidden sm:inline">New repository</span>
    </button>
  )
}
