/**
 * /login — public route. Single centered card on a quiet off-white surface.
 *
 * Design rationale:
 *   * The split-screen-with-hero pattern is played-out SaaS aesthetic. Stripe,
 *     Linear, Vercel, GitHub, Cal.com, Resend — none of them do it anymore.
 *     A login is a transactional moment; visual noise here just costs the
 *     user time before they get to the actual app.
 *   * One card, one column, one primary action. Works at every viewport
 *     from 320px upward without any media-query gymnastics.
 *   * Type stays modest (heading-md, not display-*). The card itself
 *     carries the visual weight via subtle border + shadow.
 *
 * Form rules:
 *   * Tenant ("workspace") ID is collapsed behind a disclosure — defaults
 *     from VITE_TENANT_ID in dev. Production deployments resolve tenant
 *     from host header so most users will never see this field.
 *   * Error mapping mirrors PENTEST-005: never disclose which input was
 *     wrong. Single 401 → single "Invalid credentials" toast.
 */
import { useState } from 'react'
import { createFileRoute, useNavigate, redirect } from '@tanstack/react-router'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { toast } from 'sonner'
import { AxiosError } from 'axios'
import { ChevronDown } from 'lucide-react'
import { apiClient } from '@/lib/api/client'
import { useAuthStore } from '@/store/authStore'
import { Button } from '@/components/ui/Button'
import { Input, Label, FieldError, FieldHint } from '@/components/ui/Input'
import { cn } from '@/lib/utils/cn'

const DEFAULT_TENANT_ID =
  (import.meta.env.VITE_TENANT_ID as string | undefined) ??
  '98dbe36b-ef28-4903-b25c-bff1b2921c9e'

const loginSchema = z.object({
  tenantId: z.string().uuid('Workspace ID must be a valid UUID'),
  username: z.string().min(1, 'Username is required').max(64),
  password: z.string().min(1, 'Password is required'),
})

type LoginInput = z.infer<typeof loginSchema>

interface LoginResponse {
  token: string
}

export const Route = createFileRoute('/login')({
  beforeLoad: () => {
    if (useAuthStore.getState().token) {
      throw redirect({ to: '/dashboard' })
    }
  },
  validateSearch: (search: Record<string, unknown>) => ({
    from: typeof search.from === 'string' ? search.from : undefined,
  }),
  component: LoginPage,
})

function LoginPage() {
  const navigate = useNavigate()
  const setSession = useAuthStore((s) => s.setSession)
  const { from } = Route.useSearch()
  const [showAdvanced, setShowAdvanced] = useState(false)

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<LoginInput>({
    resolver: zodResolver(loginSchema),
    defaultValues: {
      tenantId: DEFAULT_TENANT_ID,
      username: '',
      password: '',
    },
  })

  const loginMutation = useMutation({
    mutationFn: async (input: LoginInput) => {
      const resp = await apiClient.post<LoginResponse>('/login', {
        tenant_id: input.tenantId,
        username: input.username,
        password: input.password,
      })
      return resp.data
    },
    onSuccess: (data) => {
      setSession(data.token)
      toast.success('Signed in')
      navigate({ to: (from as '/dashboard') ?? '/dashboard' })
    },
    onError: (err: unknown) => {
      let message = 'Invalid credentials. Please try again.'
      if (err instanceof AxiosError) {
        if (err.response?.status === 429) {
          message = 'Too many attempts. Please wait a moment.'
        } else if (!err.response) {
          message = "Couldn't reach the server. Check your connection."
        }
      }
      toast.error(message)
    },
  })

  // SSO is UI-complete here but the backend doesn't have any providers wired
  // yet (services/auth only does username/password + API keys today). When the
  // operator clicks an SSO button we surface a "coming soon" toast so the
  // affordance is honest. Sprint 1 backend work adds the OIDC/OAuth flow.
  const ssoComingSoon = (provider: string) => () =>
    toast.message(`${provider} sign-in is coming soon`, {
      description: 'Use your username and password for now.',
    })

  return (
    <div className="min-h-screen w-full flex flex-col items-center justify-center bg-auth-canvas px-md py-2xl">
      {/* Brand mark — sits above the card so it doesn't compete for space inside */}
      <div className="flex items-center gap-md mb-xl">
        <Mark />
        <span className="text-heading-sm font-semibold text-on-surface tracking-tight">
          Janus
        </span>
      </div>

      {/* The card. max-w-[400px] keeps the form readable on any viewport. */}
      <div className="w-full max-w-[400px] bg-surface rounded-md border border-border shadow-sm">
        <div className="p-xl space-y-lg">
          <header className="space-y-xs">
            <h1 className="text-heading-md font-semibold text-on-surface">
              Sign in to your workspace
            </h1>
            <p className="text-body-sm text-on-surface-muted">
              Continue with a single sign-on provider or your username and password.
            </p>
          </header>

          {/* SSO providers — leads the screen because for most enterprise
              deployments this is the path most users will take. */}
          <div className="space-y-sm">
            <SSOButton provider="Google" onClick={ssoComingSoon('Google')}>
              <GoogleGlyph />
              Continue with Google
            </SSOButton>
            <SSOButton provider="GitHub" onClick={ssoComingSoon('GitHub')}>
              <GitHubGlyph />
              Continue with GitHub
            </SSOButton>
            <SSOButton provider="Microsoft" onClick={ssoComingSoon('Microsoft')}>
              <MicrosoftGlyph />
              Continue with Microsoft
            </SSOButton>
            <button
              type="button"
              onClick={ssoComingSoon('SAML / OIDC')}
              className="w-full text-center text-label-md text-on-surface-muted hover:text-on-surface transition-colors pt-xs"
            >
              Use a different SSO provider
            </button>
          </div>

          {/* Divider with "or" — separates SSO from credential login. */}
          <div className="relative flex items-center" aria-hidden="true">
            <div className="flex-1 h-px bg-border" />
            <span className="mx-md text-label-sm text-on-surface-subtle uppercase tracking-wider">
              or
            </span>
            <div className="flex-1 h-px bg-border" />
          </div>

          <form
            noValidate
            onSubmit={handleSubmit((v) => loginMutation.mutate(v))}
            className="space-y-lg"
          >
            <div>
              <Label htmlFor="username" required>Username</Label>
              <Input
                id="username"
                type="text"
                autoComplete="username"
                autoFocus
                placeholder="admin"
                aria-invalid={!!errors.username}
                error={!!errors.username}
                {...register('username')}
              />
              {errors.username && <FieldError>{errors.username.message}</FieldError>}
            </div>

            <div>
              <Label htmlFor="password" required>Password</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                placeholder="••••••••"
                aria-invalid={!!errors.password}
                error={!!errors.password}
                {...register('password')}
              />
              {errors.password && <FieldError>{errors.password.message}</FieldError>}
            </div>

            {/* Workspace ID — collapsed by default. */}
            <div>
              <button
                type="button"
                onClick={() => setShowAdvanced((s) => !s)}
                className="inline-flex items-center gap-xs text-label-md text-on-surface-muted hover:text-on-surface transition-colors"
                aria-expanded={showAdvanced}
                aria-controls="workspace-field"
              >
                <ChevronDown
                  className={cn(
                    'w-4 h-4 transition-transform duration-base ease-out',
                    showAdvanced && 'rotate-180',
                  )}
                />
                Use a different workspace
              </button>
              {showAdvanced && (
                <div id="workspace-field" className="mt-md">
                  <Label htmlFor="tenantId">Workspace ID</Label>
                  <Input
                    id="tenantId"
                    type="text"
                    spellCheck={false}
                    autoComplete="off"
                    className="font-mono text-code-sm"
                    error={!!errors.tenantId}
                    {...register('tenantId')}
                  />
                  {errors.tenantId ? (
                    <FieldError>{errors.tenantId.message}</FieldError>
                  ) : (
                    <FieldHint>
                      With a custom domain you can skip this — the platform routes by hostname.
                    </FieldHint>
                  )}
                </div>
              )}
            </div>

            <Button
              type="submit"
              size="lg"
              fullWidth
              loading={loginMutation.isPending}
            >
              Sign in
            </Button>
          </form>
        </div>
      </div>

      {/* Footer line below the card */}
      <p className="mt-xl text-label-sm text-on-surface-subtle">
        Need access?{' '}
        <button
          type="button"
          className="text-on-surface-muted hover:text-on-surface underline-offset-4 hover:underline transition-colors"
          onClick={() =>
            toast.message('Account requests go through your workspace admin.')
          }
        >
          Talk to your admin
        </button>
      </p>
    </div>
  )
}

/** Tiny brand mark — square indigo tile with three stacked layers. */
function Mark() {
  return (
    <div className="w-8 h-8 rounded-sm bg-primary flex items-center justify-center shadow-xs">
      <svg width="18" height="18" viewBox="0 0 32 32" aria-hidden="true">
        <path d="M9 14l7-4 7 4-7 4-7-4z" fill="#fff" fillOpacity="0.95" />
        <path d="M9 14v6l7 4v-6L9 14z" fill="#fff" fillOpacity="0.7" />
        <path d="M23 14v6l-7 4v-6l7-4z" fill="#fff" fillOpacity="0.55" />
      </svg>
    </div>
  )
}

/**
 * SSOButton — full-width neutral button with a brand glyph on the left.
 *
 * We use ONE button style for every provider (no Google blue, GitHub black,
 * etc.) so the wall of buttons reads as a coherent list rather than a
 * stripe of colors fighting each other. The brand identity comes from the
 * glyph alone — that's the convention Stripe / Linear / Vercel use.
 */
function SSOButton({
  provider,
  onClick,
  children,
}: {
  provider: string
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={`Continue with ${provider}`}
      className={cn(
        'w-full h-11 inline-flex items-center justify-center gap-md',
        'rounded-sm border border-border bg-surface',
        'text-body-sm font-medium text-on-surface',
        'transition-[background-color,border-color,box-shadow] duration-[120ms] ease-out',
        'hover:bg-surface-muted hover:border-border-strong',
        'active:scale-[0.99]',
        'shadow-xs',
      )}
    >
      {children}
    </button>
  )
}

// ---- Provider glyphs ------------------------------------------------------
// Inline SVG so we don't ship an icon dep just for these three. Each glyph
// is sized to ~18px and uses each brand's official mark + colors.

function GoogleGlyph() {
  return (
    <svg width="18" height="18" viewBox="0 0 18 18" aria-hidden="true">
      <path
        fill="#4285F4"
        d="M17.64 9.2c0-.637-.057-1.251-.164-1.84H9v3.481h4.844a4.14 4.14 0 0 1-1.796 2.716v2.258h2.908c1.702-1.567 2.684-3.874 2.684-6.615z"
      />
      <path
        fill="#34A853"
        d="M9 18c2.43 0 4.467-.806 5.956-2.18l-2.908-2.259c-.806.54-1.836.86-3.048.86-2.344 0-4.328-1.584-5.036-3.711H.957v2.332A8.997 8.997 0 0 0 9 18z"
      />
      <path
        fill="#FBBC05"
        d="M3.964 10.71A5.41 5.41 0 0 1 3.682 9c0-.593.102-1.17.282-1.71V4.958H.957A8.996 8.996 0 0 0 0 9c0 1.452.348 2.827.957 4.042l3.007-2.332z"
      />
      <path
        fill="#EA4335"
        d="M9 3.58c1.321 0 2.508.454 3.44 1.345l2.582-2.58C13.463.891 11.426 0 9 0A8.997 8.997 0 0 0 .957 4.958L3.964 7.29C4.672 5.163 6.656 3.58 9 3.58z"
      />
    </svg>
  )
}

function GitHubGlyph() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true">
      <path
        fill="currentColor"
        d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23A11.51 11.51 0 0 1 12 5.803c1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.91 1.235 3.221 0 4.61-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222 0 1.606-.014 2.898-.014 3.293 0 .322.216.694.825.576C20.565 22.092 24 17.598 24 12.297c0-6.627-5.373-12-12-12"
      />
    </svg>
  )
}

function MicrosoftGlyph() {
  return (
    <svg width="18" height="18" viewBox="0 0 23 23" aria-hidden="true">
      <path fill="#F25022" d="M0 0h11v11H0z" />
      <path fill="#7FBA00" d="M12 0h11v11H12z" />
      <path fill="#00A4EF" d="M0 12h11v11H0z" />
      <path fill="#FFB900" d="M12 12h11v11H12z" />
    </svg>
  )
}
