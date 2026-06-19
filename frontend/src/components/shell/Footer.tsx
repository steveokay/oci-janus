/**
 * Footer — slim status / utility strip at the bottom of every
 * authenticated page.
 *
 * Three slots:
 *   * Left   — live status indicator (small pulsing dot + service label
 *               + version) backed by the /stats system_health_pct.
 *   * Centre — docs / API reference / changelog links.
 *   * Right  — keyboard shortcuts trigger (`?`), opens the cheat-sheet
 *               dialog. Shows the literal `?` keybind hint.
 *
 * The shortcut keybind itself lives in `useKeyboardShortcutsHint` which
 * is mounted at the AppShell level so it works regardless of route.
 */
import { useState } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { ArrowUpRight, BookOpen, Code, Github, Keyboard, X } from 'lucide-react'
import { useStats } from '@/lib/api/hooks/useStats'
import { cn } from '@/lib/utils/cn'

const VERSION = '0.1.0'

export function Footer() {
  const { data, isError, isLoading } = useStats()
  const [shortcutsOpen, setShortcutsOpen] = useState(false)
  const health = data?.system_health_pct ?? 100
  const status = isLoading
    ? 'pending'
    : isError || health < 75
    ? 'down'
    : health < 95
    ? 'degraded'
    : 'healthy'

  return (
    <footer className="flex items-center justify-between gap-md px-xl py-sm border-t border-border bg-surface text-label-sm text-on-surface-muted">
      <div className="flex items-center gap-sm shrink-0">
        <span
          aria-hidden="true"
          className={cn(
            'w-2 h-2 rounded-full',
            status === 'healthy' && 'bg-success-500 animate-pulse',
            status === 'degraded' && 'bg-warning-500',
            status === 'down' && 'bg-danger-500',
            status === 'pending' && 'bg-neutral-400 animate-pulse',
          )}
        />
        <span>
          Janus{' '}
          <span className="font-mono text-on-surface-subtle">v{VERSION}</span>
        </span>
      </div>

      <nav
        aria-label="Documentation links"
        className="hidden md:flex items-center gap-lg"
      >
        <FooterLink href="https://docs.docker.com/registry/" label="Docs" icon={<BookOpen className="w-3.5 h-3.5" />} />
        <FooterLink href="https://github.com/steveokay/oci-janus" label="GitHub" icon={<Github className="w-3.5 h-3.5" />} />
        <FooterLink href="#" label="API reference" icon={<Code className="w-3.5 h-3.5" />} />
      </nav>

      <button
        type="button"
        onClick={() => setShortcutsOpen(true)}
        className="inline-flex items-center gap-xs hover:text-on-surface transition-colors shrink-0"
      >
        <Keyboard className="w-3.5 h-3.5" aria-hidden="true" />
        <span className="hidden sm:inline">Shortcuts</span>
        <kbd className="inline-flex items-center justify-center px-1.5 h-4 rounded-xs border border-border bg-surface-muted text-[10px] font-mono">
          ?
        </kbd>
      </button>

      <ShortcutsDialog
        open={shortcutsOpen}
        onOpenChange={setShortcutsOpen}
      />
    </footer>
  )
}

function FooterLink({
  href,
  label,
  icon,
}: {
  href: string
  label: string
  icon: React.ReactNode
}) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-xs hover:text-on-surface transition-colors"
    >
      {icon}
      {label}
      <ArrowUpRight
        className="w-3 h-3 text-on-surface-subtle"
        aria-hidden="true"
      />
    </a>
  )
}

const SHORTCUTS: { keys: string; description: string }[] = [
  { keys: '⌘ K / Ctrl K',    description: 'Open command palette' },
  { keys: '?',                description: 'Show this shortcut sheet' },
  { keys: 'Esc',              description: 'Close any open dialog or palette' },
  { keys: '↑ / ↓',           description: 'Move selection in palettes + tables' },
  { keys: 'Enter',            description: 'Run highlighted action' },
  { keys: 'Tab',              description: 'Move forward through controls' },
  { keys: 'Shift + Tab',      description: 'Move backward through controls' },
]

function ShortcutsDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-surface-overlay backdrop-blur-sm" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-[480px] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-surface shadow-xl focus:outline-none">
          <div className="flex items-start justify-between p-lg border-b border-border">
            <div>
              <Dialog.Title className="text-heading-sm font-semibold text-on-surface">
                Keyboard shortcuts
              </Dialog.Title>
              <Dialog.Description className="mt-xs text-body-sm text-on-surface-muted">
                A few patterns that work anywhere in the app.
              </Dialog.Description>
            </div>
            <Dialog.Close
              aria-label="Close"
              className="inline-flex items-center justify-center w-8 h-8 rounded-xs text-on-surface-muted hover:text-on-surface hover:bg-surface-muted transition-colors"
            >
              <X className="w-4 h-4" aria-hidden="true" />
            </Dialog.Close>
          </div>
          <ul className="p-lg space-y-sm">
            {SHORTCUTS.map((s) => (
              <li
                key={s.keys}
                className="flex items-center justify-between gap-md py-xs"
              >
                <span className="text-body-sm text-on-surface">
                  {s.description}
                </span>
                <kbd className="inline-flex items-center justify-center px-sm h-6 rounded-xs border border-border bg-surface-muted text-label-sm font-mono text-on-surface-muted whitespace-nowrap">
                  {s.keys}
                </kbd>
              </li>
            ))}
          </ul>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
