/**
 * WorkspaceSwitcher — top-of-sidebar workspace header.
 *
 * Sprint 1a ships this read-only: the dropdown chevron is a placeholder
 * for the future tenant switcher (Sprint 3). The workspace name comes
 * from a small dev-tenant lookup table; production wiring waits on a
 * `GET /api/v1/workspace/me` endpoint that we haven't built yet.
 *
 * Why surface the workspace at all in Sprint 1: the deferred decision
 * around how super-admins log in (see REBUILD-PLAN.md) means every
 * authenticated screen should always answer "which workspace am I in?"
 * at a glance. Even a static label is better than nothing.
 */
import { ChevronsUpDown } from 'lucide-react'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'

// Hard-coded for now. Each tenant id maps to its display name. Replace
// with a `/api/v1/workspace/me` call when the endpoint exists.
const WORKSPACE_NAME: Record<string, string> = {
  '98dbe36b-ef28-4903-b25c-bff1b2921c9e': 'Default',
}

export function WorkspaceSwitcher() {
  const tenantId = useAuthStore((s) => s.user?.tenantId ?? '')
  const name = WORKSPACE_NAME[tenantId] ?? 'Workspace'
  // Show the first segment of the tenant UUID as a stand-in slug. Once
  // tenants have human-readable slugs we'll surface those instead.
  const slug = tenantId.slice(0, 8) || '—'

  return (
    <button
      type="button"
      onClick={() =>
        toast.message('Workspace switching is coming soon', {
          description: 'Sprint 3 wires this to the tenant API.',
        })
      }
      className="flex items-center gap-md w-full p-md rounded-sm hover:bg-surface-muted transition-colors"
    >
      <div className="w-8 h-8 rounded-sm bg-primary text-on-primary flex items-center justify-center shadow-xs font-semibold text-label-md shrink-0">
        {name.charAt(0).toUpperCase()}
      </div>
      <div className="flex-1 text-left min-w-0">
        <div className="text-body-sm font-semibold text-on-surface truncate">
          {name}
        </div>
        <div className="text-label-sm text-on-surface-subtle font-mono truncate">
          {slug}
        </div>
      </div>
      <ChevronsUpDown
        className="w-4 h-4 text-on-surface-subtle shrink-0"
        aria-hidden="true"
      />
    </button>
  )
}
