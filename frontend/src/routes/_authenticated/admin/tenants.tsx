/**
 * /admin/tenants — Super-admin tenant CRUD.
 *
 * Backed by management's /api/v1/admin/tenants endpoints. Both the route
 * itself and the rendered actions are gated by the platform-admin marker
 * scope (admin / org / *) — server enforcement is the source of truth
 * (PENTEST-024), this hook is a UX-only convenience that hides controls
 * the server would deny anyway.
 *
 * Bootstrap chicken-and-egg: the first super-admin still comes from the
 * dev seed migration (services/auth/migrations/20260618000001_seed_dev_admin_role.sql)
 * because the page itself requires a logged-in platform admin to use.
 */

import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { toast } from 'sonner'
import { apiClient } from '@/lib/api/client'
import { useAuthStore } from '@/store/authStore'
import { useUserIsPlatformAdmin } from '@/lib/auth/usePlatformAdmin'

// ---------------------------------------------------------------------------
// API types — mirror services/management/internal/handler/admin_tenants.go
// ---------------------------------------------------------------------------

interface TenantRow {
  tenant_id: string
  name: string
  plan: string
  created_at: string
}

interface ListTenantsResponse {
  tenants: TenantRow[]
  next_page_token?: string
}

// ---------------------------------------------------------------------------
// Route definition
// ---------------------------------------------------------------------------

export const Route = createFileRoute('/_authenticated/admin/tenants')({
  /*
   * beforeLoad runs synchronously during route resolution; throwing redirect()
   * is the spec-correct way to abort the current navigation and start a new
   * one before any component mounts. We bounce non-platform-admins back to
   * the dashboard so they don't see the screen render and immediately error.
   */
  beforeLoad: () => {
    const user = useAuthStore.getState().user
    const roles = user?.roles ?? []
    // Frontend can't read scope-aware roles from the JWT (only flat names),
    // so it shows the page to any admin/owner; the server (PENTEST-024)
    // will reject the actual API calls if the caller lacks the org=* marker.
    // The check here is purely a "don't even render" UX guard.
    if (!roles.includes('admin') && !roles.includes('owner')) {
      throw redirect({ to: '/dashboard' })
    }
  },
  component: AdminTenants,
})

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

function AdminTenants() {
  const isPlatformAdmin = useUserIsPlatformAdmin()
  const qc = useQueryClient()

  const { data, isLoading, error } = useQuery<ListTenantsResponse>({
    queryKey: ['admin', 'tenants'],
    queryFn: async () => {
      const resp = await apiClient.get<ListTenantsResponse>('/admin/tenants')
      return resp.data
    },
  })

  const createMutation = useMutation({
    mutationFn: async (payload: { name: string; plan: string }) => {
      const resp = await apiClient.post<TenantRow>('/admin/tenants', payload)
      return resp.data
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'tenants'] })
      toast.success('Tenant created')
      setShowCreate(false)
    },
    onError: (err: unknown) => {
      const msg =
        err && typeof err === 'object' && 'response' in err
          ? (err as { response?: { data?: { error?: string } } }).response?.data?.error
          : null
      toast.error(msg ?? 'Failed to create tenant')
    },
  })

  const deleteMutation = useMutation({
    mutationFn: async (tenantID: string) => {
      await apiClient.delete(`/admin/tenants/${tenantID}`)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'tenants'] })
      toast.success('Tenant deleted')
    },
    onError: () => toast.error('Failed to delete tenant'),
  })

  const [showCreate, setShowCreate] = useState(false)

  if (!isPlatformAdmin) {
    return (
      <div className="text-on-surface-variant">
        Platform-admin role required. Your role is set by the operator.
      </div>
    )
  }

  return (
    <div className="space-y-lg">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-headline-lg font-bold text-on-surface">Tenants</h1>
        <button
          type="button"
          onClick={() => setShowCreate(true)}
          className="px-md py-sm bg-primary text-on-primary rounded-lg font-label-caps text-label-caps font-bold hover:opacity-90 transition-opacity"
        >
          New tenant
        </button>
      </div>

      {/* State: loading / error / empty / list */}
      {isLoading && <div className="text-on-surface-variant">Loading…</div>}
      {error && <div className="text-error">Failed to load tenants.</div>}
      {data && data.tenants.length === 0 && (
        <div className="text-on-surface-variant">
          No tenants yet. Click <span className="font-bold">New tenant</span> to create one.
        </div>
      )}

      {data && data.tenants.length > 0 && (
        <div className="bg-surface-container-low border border-outline-variant rounded-lg overflow-hidden">
          <table className="w-full">
            <thead className="bg-surface-container">
              <tr className="text-left text-label-caps font-label-caps text-on-surface-variant">
                <th className="px-md py-sm">Name</th>
                <th className="px-md py-sm">Plan</th>
                <th className="px-md py-sm">Tenant ID</th>
                <th className="px-md py-sm">Created</th>
                <th className="px-md py-sm w-1" />
              </tr>
            </thead>
            <tbody>
              {data.tenants.map((t) => (
                <tr key={t.tenant_id} className="border-t border-outline-variant text-body-md">
                  <td className="px-md py-sm font-bold text-on-surface">{t.name}</td>
                  <td className="px-md py-sm text-on-surface-variant">{t.plan}</td>
                  <td className="px-md py-sm text-on-surface-variant font-code-sm">
                    {t.tenant_id.slice(0, 8)}…
                  </td>
                  <td className="px-md py-sm text-on-surface-variant">
                    {new Date(t.created_at).toLocaleString()}
                  </td>
                  <td className="px-md py-sm">
                    <button
                      type="button"
                      onClick={() => {
                        // Confirm before destructive op — cascades to all repos in the tenant.
                        if (
                          confirm(
                            `Delete tenant "${t.name}" and all its data? This cannot be undone.`,
                          )
                        ) {
                          deleteMutation.mutate(t.tenant_id)
                        }
                      }}
                      aria-label={`Delete tenant ${t.name}`}
                      className="text-error hover:opacity-80 transition-opacity"
                    >
                      <span className="material-symbols-outlined text-[20px]">delete</span>
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Create modal */}
      {showCreate && (
        <CreateTenantModal
          onClose={() => setShowCreate(false)}
          onSubmit={(name, plan) => createMutation.mutate({ name, plan })}
          submitting={createMutation.isPending}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// CreateTenantModal — lightweight inline modal (no portal needed)
// ---------------------------------------------------------------------------

function CreateTenantModal({
  onClose,
  onSubmit,
  submitting,
}: {
  onClose: () => void
  onSubmit: (name: string, plan: string) => void
  submitting: boolean
}) {
  const [name, setName] = useState('')
  const [plan, setPlan] = useState('standard')

  return (
    <div
      className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-labelledby="new-tenant-title"
    >
      <div
        className="bg-surface w-full max-w-md p-lg rounded-lg space-y-md"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 id="new-tenant-title" className="text-headline-md font-bold text-on-surface">
          New tenant
        </h2>

        <div className="space-y-sm">
          <label htmlFor="tenant-name" className="block text-label-caps font-label-caps text-on-surface-variant">
            Name
          </label>
          <input
            id="tenant-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="acme-corp"
            className="w-full px-md py-sm border border-outline-variant rounded-lg bg-surface-container-low text-body-md focus:outline-none focus:ring-1 focus:ring-primary"
          />
          <p className="text-xs text-on-surface-variant">
            Lowercase alphanumeric + hyphens, 2–64 chars. Used as a subdomain.
          </p>
        </div>

        <div className="space-y-sm">
          <label htmlFor="tenant-plan" className="block text-label-caps font-label-caps text-on-surface-variant">
            Plan
          </label>
          <select
            id="tenant-plan"
            value={plan}
            onChange={(e) => setPlan(e.target.value)}
            className="w-full px-md py-sm border border-outline-variant rounded-lg bg-surface-container-low text-body-md focus:outline-none focus:ring-1 focus:ring-primary"
          >
            <option value="standard">Standard</option>
            <option value="enterprise">Enterprise</option>
            <option value="free">Free</option>
          </select>
        </div>

        <div className="flex items-center justify-end gap-sm pt-md">
          <button
            type="button"
            onClick={onClose}
            className="px-md py-sm text-on-surface-variant hover:bg-surface-variant rounded-lg transition-colors"
          >
            Cancel
          </button>
          <button
            type="button"
            disabled={submitting || !name.trim()}
            onClick={() => onSubmit(name.trim(), plan)}
            className="px-md py-sm bg-primary text-on-primary rounded-lg font-bold disabled:opacity-50 hover:opacity-90 transition-opacity"
          >
            {submitting ? 'Creating…' : 'Create tenant'}
          </button>
        </div>
      </div>
    </div>
  )
}
