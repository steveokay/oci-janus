/**
 * Dashboard — Sprint 0 placeholder. Just proves the login → dashboard
 * round-trip works. Sprint 1 builds the actual overview.
 */
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '@/store/authStore'
import { Button } from '@/components/ui/Button'

export const Route = createFileRoute('/_authenticated/dashboard')({
  component: DashboardPlaceholder,
})

function DashboardPlaceholder() {
  const user = useAuthStore((s) => s.user)
  const clearSession = useAuthStore((s) => s.clearSession)
  const navigate = useNavigate()

  return (
    <div className="min-h-screen flex items-center justify-center p-2xl">
      <div className="max-w-md w-full bg-surface rounded-md shadow-md p-2xl text-center space-y-lg border border-border">
        <div className="flex items-center justify-center w-12 h-12 mx-auto rounded-full bg-primary-soft">
          <svg
            className="w-6 h-6 text-primary"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M20 6 9 17l-5-5" />
          </svg>
        </div>
        <div>
          <h1 className="text-heading-md font-semibold text-on-surface">You're in</h1>
          <p className="mt-sm text-body-sm text-on-surface-muted">
            Signed in as <span className="font-medium text-on-surface">{user?.username || '—'}</span>{' '}
            {user?.roles && user.roles.length > 0 && (
              <>
                with role{' '}
                <span className="font-mono text-code-sm text-primary">
                  {user.roles.join(', ')}
                </span>
              </>
            )}
            .
          </p>
        </div>
        <div className="text-label-sm text-on-surface-subtle">
          Sprint 1 will replace this placeholder with the real overview.
        </div>
        <Button
          variant="ghost"
          fullWidth
          onClick={() => {
            clearSession()
            navigate({ to: '/login', search: { from: undefined } })
          }}
        >
          Sign out
        </Button>
      </div>
    </div>
  )
}
