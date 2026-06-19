/**
 * UserMenu — footer of the sidebar. Avatar + name + primary role, plus a
 * Radix dropdown for Profile / Sign out.
 *
 * The avatar is an initial-on-indigo tile. Real avatars land when the
 * profile API supports an upload (Sprint 1e). Profile itself is also
 * Sprint 1e — until then the menu item shows a "coming soon" toast.
 */
import * as DropdownMenu from '@radix-ui/react-dropdown-menu'
import { LogOut, User } from 'lucide-react'
import { useNavigate } from '@tanstack/react-router'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'

export function UserMenu() {
  const user = useAuthStore((s) => s.user)
  const clearSession = useAuthStore((s) => s.clearSession)
  const navigate = useNavigate()

  if (!user) return null

  const initial = user.username.charAt(0).toUpperCase() || '?'
  // The JWT carries multiple roles; the highest-privilege one happens to
  // sort first in our seed data so we just use index 0. If the ordering
  // ever stops being meaningful, we'll need a "primary role" claim or a
  // small client-side ranker — fine as a TODO until then.
  const primaryRole = user.roles[0] ?? 'member'

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          className="flex items-center gap-md w-full p-md rounded-sm hover:bg-surface-muted transition-colors"
        >
          <div className="w-8 h-8 rounded-full bg-primary text-on-primary flex items-center justify-center font-semibold text-label-md shrink-0">
            {initial}
          </div>
          <div className="flex-1 text-left min-w-0">
            <div className="text-body-sm font-medium text-on-surface truncate">
              {user.username}
            </div>
            <div className="text-label-sm text-on-surface-subtle capitalize truncate">
              {primaryRole}
            </div>
          </div>
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          side="top"
          align="start"
          sideOffset={4}
          className="z-50 min-w-[200px] bg-surface-raised border border-border rounded-sm shadow-md p-xs"
        >
          <DropdownMenu.Item
            onSelect={() =>
              toast.message('Profile is coming soon', {
                description:
                  'Manage your name, email, password, and API keys (Sprint 1e).',
              })
            }
            className="flex items-center gap-md px-md py-sm rounded-xs text-body-sm text-on-surface hover:bg-surface-muted focus:bg-surface-muted outline-none cursor-default"
          >
            <User className="w-4 h-4 text-on-surface-muted" aria-hidden="true" />
            Profile
          </DropdownMenu.Item>
          <DropdownMenu.Separator className="h-px bg-border my-xs" />
          <DropdownMenu.Item
            onSelect={() => {
              clearSession()
              // `from` is a typed search param on /login; explicitly clear
              // it so a sign-out lands on a clean login URL.
              navigate({ to: '/login', search: { from: undefined } })
            }}
            className="flex items-center gap-md px-md py-sm rounded-xs text-body-sm text-on-surface hover:bg-surface-muted focus:bg-surface-muted outline-none cursor-default"
          >
            <LogOut className="w-4 h-4 text-on-surface-muted" aria-hidden="true" />
            Sign out
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  )
}
