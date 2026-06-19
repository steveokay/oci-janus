/**
 * CommandPalette — Cmd+K / Ctrl+K palette built on `cmdk` + Radix Dialog.
 *
 * Three result groups:
 *   * Pages       — static routes (Dashboard, Repositories, Profile, Admin)
 *   * Repositories — live from `useRepositories`, navigates to the detail
 *                    route for a clicked row
 *   * Actions     — top-of-mind actions (new repo, new API key, sign out,
 *                    cycle theme)
 *
 * Why the dialog wrap: `cmdk`'s `Command` is a focus-trapped list. We
 * still need a dimming overlay + an escape-to-close handler + an
 * accessible label — Radix Dialog already does all of that.
 *
 * The shortcut listener lives in this file so the keybind is active
 * regardless of which route is mounted (it's installed at the AppShell
 * level via `useCommandPaletteShortcut`).
 */
import { useEffect, useMemo } from 'react'
import { Command } from 'cmdk'
import * as Dialog from '@radix-ui/react-dialog'
import { useNavigate } from '@tanstack/react-router'
import {
  Home,
  KeyRound,
  LogOut,
  Monitor,
  Moon,
  Package,
  Plus,
  Search,
  Settings,
  Sun,
  User,
} from 'lucide-react'
import { usePaletteStore } from '@/store/paletteStore'
import { useThemeStore } from '@/store/themeStore'
import { useAuthStore } from '@/store/authStore'
import { useRepositories } from '@/lib/api/hooks/useRepositories'
import { usePlatformAdmin } from '@/lib/auth/usePlatformAdmin'
import { Avatar } from '@/components/ui/Avatar'

/**
 * Mount this once at the AppShell level. Listens for Cmd+K / Ctrl+K
 * globally and toggles the palette. Standard convention; ignores the
 * shortcut when the user is typing in an input/textarea so it doesn't
 * eat their text.
 */
export function useCommandPaletteShortcut() {
  const toggle = usePaletteStore((s) => s.toggle)

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'k' && (e.metaKey || e.ctrlKey)) {
        const target = e.target as HTMLElement | null
        const tag = target?.tagName
        // Don't eat the key inside form inputs unless modifier is held —
        // we want a clean toggle from anywhere else.
        if (
          tag === 'INPUT' ||
          tag === 'TEXTAREA' ||
          target?.isContentEditable
        ) {
          if (!(e.metaKey || e.ctrlKey)) return
        }
        e.preventDefault()
        toggle()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [toggle])
}

export function CommandPalette() {
  const open = usePaletteStore((s) => s.open)
  const setOpen = usePaletteStore((s) => s.setOpen)
  const navigate = useNavigate()
  const clearSession = useAuthStore((s) => s.clearSession)
  const setMode = useThemeStore((s) => s.setMode)
  const isPlatformAdmin = usePlatformAdmin()

  // Pre-fetch the repo list so the palette has data even on first open.
  // TanStack Query handles caching — the dashboard's own list call will
  // share this cache key when present.
  const { data: repoData } = useRepositories('all')
  const repos = useMemo(
    () => repoData?.repositories ?? [],
    [repoData],
  )

  /** Run an action and close the palette. */
  const runAndClose = (fn: () => void) => () => {
    setOpen(false)
    // Defer so the dialog unmount finishes before navigation pushes a new
    // route — otherwise focus rebounds into the dialog before it dies.
    setTimeout(fn, 0)
  }

  return (
    <Dialog.Root open={open} onOpenChange={setOpen}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-surface-overlay backdrop-blur-sm" />
        <Dialog.Content
          aria-label="Command palette"
          className="fixed left-1/2 top-[15%] z-50 w-full max-w-[640px] -translate-x-1/2 rounded-lg border border-border bg-surface shadow-xl overflow-hidden focus:outline-none"
        >
          <Command label="Command palette" className="flex flex-col">
            <div className="flex items-center gap-md px-lg h-14 border-b border-border">
              <Search
                className="w-4 h-4 text-on-surface-subtle"
                aria-hidden="true"
              />
              <Command.Input
                placeholder="Search pages, repositories, actions…"
                className="flex-1 bg-transparent border-0 outline-none text-body-md text-on-surface placeholder:text-on-surface-subtle"
              />
              <kbd className="hidden md:inline-flex items-center gap-0.5 px-xs h-5 rounded-xs border border-border bg-surface-muted text-label-sm font-mono text-on-surface-muted">
                Esc
              </kbd>
            </div>

            <Command.List className="max-h-[420px] overflow-y-auto p-sm">
              <Command.Empty className="px-md py-lg text-center text-body-sm text-on-surface-muted">
                No matches.
              </Command.Empty>

              <Command.Group
                heading="Pages"
                className="[&_[cmdk-group-heading]]:px-md [&_[cmdk-group-heading]]:py-xs [&_[cmdk-group-heading]]:text-label-sm [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-on-surface-subtle"
              >
                <PaletteItem
                  icon={<Home className="w-4 h-4" />}
                  label="Dashboard"
                  onSelect={runAndClose(() =>
                    navigate({ to: '/dashboard' }),
                  )}
                />
                <PaletteItem
                  icon={<Package className="w-4 h-4" />}
                  label="Repositories"
                  onSelect={runAndClose(() =>
                    navigate({ to: '/repositories', search: { new: false } }),
                  )}
                />
                <PaletteItem
                  icon={<User className="w-4 h-4" />}
                  label="Profile"
                  onSelect={runAndClose(() => navigate({ to: '/profile' }))}
                />
                {isPlatformAdmin && (
                  <PaletteItem
                    icon={<Settings className="w-4 h-4" />}
                    label="Tenants (admin)"
                    onSelect={runAndClose(() =>
                      // No route yet — toast back via the typed path's
                      // fallthrough; safe because the palette closes.
                      navigate({ to: '/dashboard' }),
                    )}
                  />
                )}
              </Command.Group>

              {repos.length > 0 && (
                <Command.Group
                  heading="Repositories"
                  className="[&_[cmdk-group-heading]]:px-md [&_[cmdk-group-heading]]:py-xs [&_[cmdk-group-heading]]:text-label-sm [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-on-surface-subtle"
                >
                  {repos.slice(0, 8).map((repo) => {
                    const slash = repo.name.indexOf('/')
                    const org = slash >= 0 ? repo.name.slice(0, slash) : 'dev'
                    const leaf =
                      slash >= 0 ? repo.name.slice(slash + 1) : repo.name
                    const fullName = `${org}/${leaf}`
                    return (
                      <PaletteItem
                        key={repo.repo_id}
                        // Pass both the full and short form to cmdk's
                        // internal filter so a query of "alpine" or
                        // "dev/alp" both match.
                        value={`${fullName} ${repo.name}`}
                        icon={<Avatar seed={fullName} size="xs" />}
                        label={fullName}
                        onSelect={runAndClose(() =>
                          navigate({
                            to: '/repositories/$org/$repo',
                            params: { org, repo: leaf },
                          }),
                        )}
                      />
                    )
                  })}
                </Command.Group>
              )}

              <Command.Group
                heading="Actions"
                className="[&_[cmdk-group-heading]]:px-md [&_[cmdk-group-heading]]:py-xs [&_[cmdk-group-heading]]:text-label-sm [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-on-surface-subtle"
              >
                <PaletteItem
                  icon={<Plus className="w-4 h-4" />}
                  label="New repository"
                  onSelect={runAndClose(() =>
                    navigate({ to: '/repositories', search: { new: true } }),
                  )}
                />
                <PaletteItem
                  icon={<KeyRound className="w-4 h-4" />}
                  label="New API key"
                  onSelect={runAndClose(() => navigate({ to: '/profile' }))}
                />
                <PaletteItem
                  icon={<Sun className="w-4 h-4" />}
                  label="Theme: light"
                  onSelect={runAndClose(() => setMode('light'))}
                />
                <PaletteItem
                  icon={<Monitor className="w-4 h-4" />}
                  label="Theme: system"
                  onSelect={runAndClose(() => setMode('system'))}
                />
                <PaletteItem
                  icon={<Moon className="w-4 h-4" />}
                  label="Theme: dark"
                  onSelect={runAndClose(() => setMode('dark'))}
                />
                <PaletteItem
                  icon={<LogOut className="w-4 h-4" />}
                  label="Sign out"
                  onSelect={runAndClose(() => {
                    clearSession()
                    navigate({ to: '/login', search: { from: undefined } })
                  })}
                />
              </Command.Group>
            </Command.List>
          </Command>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

/**
 * Reusable cmdk row — icon chip + label, hover/keyboard-focus tint via
 * the `data-[selected="true"]` attribute that cmdk drives on the row.
 */
function PaletteItem({
  icon,
  label,
  value,
  onSelect,
}: {
  icon: React.ReactNode
  label: string
  /** Custom filter value if you want extra search terms beyond `label`. */
  value?: string
  onSelect: () => void
}) {
  return (
    <Command.Item
      value={value ?? label}
      onSelect={onSelect}
      className="flex items-center gap-md px-md py-sm rounded-xs text-body-sm text-on-surface cursor-pointer data-[selected=true]:bg-primary-soft data-[selected=true]:text-primary"
    >
      <span
        aria-hidden="true"
        className="inline-flex items-center justify-center w-5 h-5 text-on-surface-muted"
      >
        {icon}
      </span>
      <span className="flex-1">{label}</span>
    </Command.Item>
  )
}
