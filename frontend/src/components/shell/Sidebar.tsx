/**
 * Sidebar — left rail of the authenticated app shell.
 *
 * Sections (top to bottom):
 *   1. Workspace switcher
 *   2. MAIN nav      (Dashboard, Repositories)
 *   3. MANAGE nav    (Webhooks, API Keys, Audit Log)
 *   4. ADMIN nav     (gated by usePlatformAdmin — Tenants, Site Settings)
 *   5. Footer link to docs
 *   6. User menu
 *
 * Section labels make the rail scannable instead of one undifferentiated
 * list. Nav items whose destination route doesn't exist yet render as
 * "coming soon" buttons — see NavItem.
 *
 * Width: 240px on md+, hidden below md. Mobile drawer is a Sprint 4 task.
 */
import {
  Book,
  Building2,
  KeyRound,
  LayoutGrid,
  Package,
  ScrollText,
  Settings,
  Webhook,
} from 'lucide-react'
import { NavItem } from './NavItem'
import { UserMenu } from './UserMenu'
import { WorkspaceSwitcher } from './WorkspaceSwitcher'
import { usePlatformAdmin } from '@/lib/auth/usePlatformAdmin'

export function Sidebar() {
  const isPlatformAdmin = usePlatformAdmin()

  return (
    <aside className="w-60 shrink-0 hidden md:flex flex-col border-r border-border bg-surface">
      <div className="p-sm border-b border-border">
        <WorkspaceSwitcher />
      </div>

      <nav
        aria-label="Primary"
        className="flex-1 overflow-y-auto px-sm py-md space-y-xs"
      >
        <SectionHeading>Main</SectionHeading>
        <NavItem
          to="/dashboard"
          icon={<LayoutGrid className="w-4 h-4" />}
          label="Dashboard"
        />
        <NavItem
          to="/repositories"
          icon={<Package className="w-4 h-4" />}
          label="Repositories"
        />

        <SectionHeading className="pt-lg">Manage</SectionHeading>
        <NavItem
          icon={<Webhook className="w-4 h-4" />}
          label="Webhooks"
          comingSoonNote="Sprint 2: webhook CRUD + delivery log."
        />
        <NavItem
          icon={<KeyRound className="w-4 h-4" />}
          label="API Keys"
          comingSoonNote="Sprint 2: API key CRUD + one-time secret display."
        />
        <NavItem
          icon={<ScrollText className="w-4 h-4" />}
          label="Audit Log"
          comingSoonNote="Sprint 3: paged audit log viewer."
        />

        {isPlatformAdmin && (
          <>
            <SectionHeading className="pt-lg">Admin</SectionHeading>
            <NavItem
              icon={<Building2 className="w-4 h-4" />}
              label="Tenants"
              comingSoonNote="Sprint 3: tenant CRUD + domain verification."
            />
            <NavItem
              icon={<Settings className="w-4 h-4" />}
              label="Site Settings"
              comingSoonNote="Sprint 4: runtime site settings UI."
            />
          </>
        )}
      </nav>

      <div className="px-sm pt-sm pb-xs">
        <a
          href="https://docs.docker.com/registry/"
          target="_blank"
          rel="noreferrer"
          className="flex items-center gap-md w-full px-md py-sm rounded-sm text-body-sm font-medium text-on-surface-muted hover:bg-surface-muted hover:text-on-surface transition-colors"
        >
          <Book className="w-4 h-4 shrink-0" aria-hidden="true" />
          <span className="flex-1">Documentation</span>
        </a>
      </div>

      <div className="p-sm border-t border-border">
        <UserMenu />
      </div>
    </aside>
  )
}

/** Section subhead for grouped sidebar nav. */
function SectionHeading({
  children,
  className = '',
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={`pb-xs px-md ${className}`}>
      <span className="text-label-sm uppercase tracking-wider text-on-surface-subtle font-semibold">
        {children}
      </span>
    </div>
  )
}
