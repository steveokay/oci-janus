import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useEffect } from 'react'
import { toast } from 'sonner'
import {
  Loader2,
} from 'lucide-react'
import { apiClient } from '@/lib/api/client'
import { useAuthStore, type AuthUser } from '@/store/authStore'

// ---------------------------------------------------------------------------
// Route
// ---------------------------------------------------------------------------

// Search schema — `reason` is appended by the 401 interceptor when the
// session has expired so the login page can display a contextual banner.
const loginSearchSchema = z.object({
  reason: z.string().optional(),
})

export const Route = createFileRoute('/login')({
  validateSearch: loginSearchSchema,
  component: LoginPage,
})

// ---------------------------------------------------------------------------
// Validation schema
// ---------------------------------------------------------------------------

const loginSchema = z.object({
  username: z.string().min(3, 'Must be at least 3 characters'),
  password: z.string().min(8, 'Must be at least 8 characters'),
})

type LoginFormValues = z.infer<typeof loginSchema>

// ---------------------------------------------------------------------------
// API shape
// ---------------------------------------------------------------------------

interface LoginResponse {
  token: string
}

/** Decode the JWT payload (middle segment) without verifying the signature. */
function decodeJwtPayload(token: string): AuthUser {
  const b64 = token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')
  return JSON.parse(atob(b64)) as AuthUser
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

function LoginPage() {
  const navigate = useNavigate()
  const setAuth = useAuthStore((s) => s.setAuth)
  // Read the reason search param injected by the 401 interceptor.
  const { reason } = Route.useSearch()
  /* Password visibility toggle removed — reference design has no eye icon */

  /*
   * Apply the login-specific body styles (light dot-grid background).
   * We patch the body class rather than wrapping the entire page in a div
   * with a fixed background, which avoids z-index conflicts with the
   * frosted-glass card and sticky header.
   */
  useEffect(() => {
    document.body.classList.add('login-page')
    return () => {
      document.body.classList.remove('login-page')
    }
  }, [])

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
    setError,
  } = useForm<LoginFormValues>({
    resolver: zodResolver(loginSchema),
  })

  async function onSubmit(values: LoginFormValues) {
    try {
      const tenantId = import.meta.env.VITE_TENANT_ID ?? ''

      const { data } = await apiClient.post<LoginResponse>('/login', {
        tenant_id: tenantId,
        username: values.username,
        password: values.password,
      })

      // Decode claims from JWT payload; no need for a separate user endpoint.
      // Store token in Zustand memory — never localStorage (FE-SEC-001).
      setAuth(data.token, decodeJwtPayload(data.token))
      navigate({ to: '/dashboard' })
    } catch (err: unknown) {
      /*
       * Show a toast for visibility and also set a form-level error so the
       * user can see it inline without having to look up at the toast.
       */
      const message =
        isAxiosLike(err) && err.response?.status === 401
          ? 'Invalid username or password.'
          : 'Login failed. Please try again.'

      toast.error(message)
      setError('root', { message })
    }
  }

  return (
    <div className="flex flex-col min-h-screen font-[Hanken_Grotesk,system-ui,sans-serif] text-[#0b1c30]">
      {/* ------------------------------------------------------------------ */}
      {/* Header                                                               */}
      {/* ------------------------------------------------------------------ */}
      <header className="p-lg flex items-center justify-between w-full max-w-[1440px] mx-auto h-16 sticky top-0 z-50">
        <div className="flex items-center gap-xs">
          {/*
           * Material Symbols inventory_2 matches the reference exactly —
           * FILL 1 renders the solid/filled variant of the box icon.
           * The font is already loaded via index.html via Google Fonts CDN.
           */}
          <span
            className="material-symbols-outlined text-[#000917] text-headline-md leading-none"
            style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
          >
            inventory_2
          </span>
          <span className="font-headline-md text-headline-md font-bold text-[#0b1c30]">ContainerRegistry</span>
        </div>
        <div className="flex items-center gap-md">
          {/* label-caps in the reference: 12px/700/0.05em — mixed case per globals.css note */}
          <a
            href="#"
            className="font-label-caps text-label-caps text-[#44474d] hover:text-[#000917] transition-colors"
          >
            Documentation
          </a>
          <a
            href="#"
            className="font-label-caps text-label-caps text-[#44474d] hover:text-[#000917] transition-colors"
          >
            Support
          </a>
        </div>
      </header>

      {/* ------------------------------------------------------------------ */}
      {/* Main                                                                 */}
      {/* ------------------------------------------------------------------ */}
      <main className="flex-grow flex items-center justify-center px-5 py-8 relative">
        {/* Floating terminal — left, decorative, hidden on mobile.
            max-w-[20rem] is used instead of max-w-xs because the project's
            custom --spacing-xs token (4px) causes Tailwind v4 to generate
            max-w-xs as 4px rather than the default 20rem. */}
        <div className="hidden lg:block absolute left-8 top-1/2 -translate-y-1/2 opacity-10 pointer-events-none">
          <div className="font-mono text-xs bg-[#0d2137] text-[#f8f9ff] p-4 rounded shadow-xl max-w-[20rem] space-y-1">
            <p>$ docker login registry.ops.io</p>
            <p>Authenticating with credentials...</p>
            <p className="text-[#43e186]">Login Succeeded</p>
            <p>$ docker pull node:latest</p>
          </div>
        </div>

        {/* Floating terminal — right, decorative, hidden on mobile.
            Same max-w-[20rem] fix as above (avoids broken custom spacing token). */}
        <div className="absolute right-8 bottom-1/2 translate-y-1/2 opacity-10 pointer-events-none hidden lg:block">
          <div className="font-mono text-xs bg-[#0d2137] text-[#f8f9ff] p-4 rounded shadow-xl max-w-[20rem] space-y-1">
            <p className="text-[#7689a4]">registry-sync -v 2.4.1</p>
            <p>Pushing manifest for sha256:7f4c...</p>
            <p>Pushed [v1.0.4-stable]</p>
          </div>
        </div>

        {/* Login card — reference: max-w-[440px] login-card-blur border border-outline-variant p-lg md:p-xl shadow-lg relative z-10 */}
        <div className="w-full max-w-[440px] login-card-blur border border-outline-variant p-lg md:p-xl shadow-lg relative z-10">
          {/* Heading */}
          <div className="mb-xl">
            <h1 className="font-headline-lg text-headline-lg text-[#0b1c30] mb-xs">
              Welcome back
            </h1>
            <p className="font-body-md text-body-md text-[#44474d]">
              Access your secure image repositories.
            </p>
          </div>

          {/* Session-expired banner — shown when the 401 interceptor redirects here */}
          {reason === 'session_expired' && (
            <div className="mb-md p-md bg-yellow-500/10 border border-yellow-500/30 rounded-xl text-yellow-300 text-sm text-center">
              Your session has expired. Please sign in again.
            </div>
          )}

          {/* Form */}
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-md" noValidate>
            {/* Root/server error banner */}
            {errors.root && (
              <p className="text-xs text-[#ba1a1a] bg-[#ffdad6] border border-[#ba1a1a]/30 rounded px-3 py-2">
                {errors.root.message}
              </p>
            )}

            {/* Username / email */}
            <div className="space-y-xs">
              <label
                htmlFor="username"
                className="font-label-caps text-label-caps text-[#44474d] block"
              >
                USERNAME OR EMAIL
              </label>
              <div className="relative group">
                <span
                  className="material-symbols-outlined absolute left-md top-1/2 -translate-y-1/2 text-outline group-focus-within:text-primary transition-colors"
                  aria-hidden="true"
                >person</span>
                <input
                  id="username"
                  type="text"
                  autoComplete="username"
                  placeholder="e.g. j.doe@company.io"
                  aria-invalid={!!errors.username}
                  aria-describedby={errors.username ? 'username-error' : undefined}
                  className={[
                    // pl-[48px] = 16px icon left-md + 24px icon width + 8px gap — matches reference
                    // rounded-lg = 0.25rem per the custom scale in globals.css
                    'w-full pl-[48px] pr-md py-sm bg-surface-container-low border rounded-lg font-body-md',
                    'focus:outline-none focus:ring-0 transition-all',
                    errors.username
                      ? 'border-[#ba1a1a] focus:border-[#ba1a1a]'
                      : 'border-outline-variant focus:border-primary',
                  ].join(' ')}
                  {...register('username')}
                />
              </div>
              {errors.username && (
                <p id="username-error" className="text-xs text-[#ba1a1a] mt-0.5">
                  {errors.username.message}
                </p>
              )}
            </div>

            {/* Password */}
            <div className="space-y-xs">
              <div className="flex items-center justify-between">
                <label
                  htmlFor="password"
                  className="font-label-caps text-label-caps text-[#44474d] block"
                >
                  PASSWORD
                </label>
                {/* label-caps: 12px/700/0.05em — NOT uppercase; reference renders "Forgot Password?" in mixed case */}
                <a
                  href="#"
                  className="font-label-caps text-label-caps text-secondary hover:underline"
                >
                  Forgot Password?
                </a>
              </div>
              {/*
               * Password field — no visibility toggle, matching the reference design
               * which has no eye icon. The lock icon sits on the left only; pr-4 is
               * used (same as username) since there is no right-side button.
               */}
              <div className="relative group">
                <span
                  className="material-symbols-outlined absolute left-md top-1/2 -translate-y-1/2 text-outline group-focus-within:text-primary transition-colors"
                  aria-hidden="true"
                >lock</span>
                <input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  placeholder="••••••••"
                  aria-invalid={!!errors.password}
                  aria-describedby={errors.password ? 'password-error' : undefined}
                  className={[
                    // pl-[48px] matches reference; pr-md since there is no eye-toggle button
                    // rounded-lg = 0.25rem per the custom scale in globals.css
                    'w-full pl-[48px] pr-md py-sm bg-surface-container-low border rounded-lg font-body-md',
                    'focus:outline-none focus:ring-0 transition-all',
                    errors.password
                      ? 'border-[#ba1a1a] focus:border-[#ba1a1a]'
                      : 'border-outline-variant focus:border-primary',
                  ].join(' ')}
                  {...register('password')}
                />
              </div>
              {errors.password && (
                <p id="password-error" className="text-xs text-[#ba1a1a] mt-0.5">
                  {errors.password.message}
                </p>
              )}
            </div>

            {/* Submit — py-4 (16px) matches reference py-md=16px; mt-6 (24px) matches mt-lg=24px;
                text-[20px] leading-7 font-semibold matches headline-md: 20px/28px/600 */}
            <button
              type="submit"
              disabled={isSubmitting}
              className="w-full py-md mt-lg bg-primary-container text-surface font-headline-md text-headline-md rounded-lg
                         hover:opacity-90 active:scale-[0.98] transition-all
                         flex items-center justify-center gap-sm
                         disabled:opacity-60 disabled:cursor-not-allowed disabled:active:scale-100"
            >
              {isSubmitting ? (
                <>
                  <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />
                  Validating…
                </>
              ) : (
                <>
                  Login
                  {/*
                   * Material Symbols arrow_forward matches the reference exactly.
                   * Lucide ArrowRight has a different stroke weight/shape.
                   */}
                  <span className="material-symbols-outlined text-[20px] leading-none" aria-hidden="true">
                    arrow_forward
                  </span>
                </>
              )}
            </button>
          </form>

          {/* Divider */}
          <div className="relative flex items-center my-xl">
            <div className="flex-grow border-t border-[#c4c6cd]" />
            <span className="px-md font-label-caps text-label-caps text-[#44474d]">OR CONTINUE WITH</span>
            <div className="flex-grow border-t border-[#c4c6cd]" />
          </div>

          {/* SSO — matches reference exactly: py-md, border-outline-variant, rounded-lg, font-headline-md */}
          <button
            type="button"
            className="w-full py-md border border-outline-variant bg-white text-on-surface font-headline-md text-headline-md
                       rounded-lg hover:bg-surface-container-low active:scale-[0.98] transition-all
                       flex items-center justify-center gap-sm"
          >
            {/*
             * Material Symbols shield_person with FILL 1 (filled style) matches the reference.
             * Lucide ShieldCheck has a different shape (checkmark vs. person silhouette).
             */}
            <span
              className="material-symbols-outlined text-[20px] leading-none"
              style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
              aria-hidden="true"
            >
              shield_person
            </span>
            Login with SSO
          </button>

          {/* Registration link */}
          <div className="mt-xl text-center">
            <p className="font-body-md text-body-md text-[#44474d]">
              New user?{' '}
              <a href="#" className="text-[#2f6096] font-bold hover:underline">
                Request Access
              </a>
            </p>
          </div>
        </div>
      </main>

      {/* ------------------------------------------------------------------ */}
      {/* Footer                                                               */}
      {/* ------------------------------------------------------------------ */}
      <footer className="p-lg border-t border-[#c4c6cd] bg-[#ffffff]">
        <div className="max-w-[1440px] mx-auto flex flex-col md:flex-row items-center justify-between gap-md">
          <div className="flex items-center gap-sm">
            {/*
             * Material Symbols verified_user matches the reference footer icon exactly.
             * on-tertiary-container (#009c54) is the green color used in the reference.
             * text-body-lg matches the reference `text-body-lg` class on this icon.
             */}
            <span
              className="material-symbols-outlined text-[#009c54] text-body-lg"
              aria-hidden="true"
            >
              verified_user
            </span>
            <span className="font-label-caps text-label-caps text-[#44474d]">
              FIPS 140-2 COMPLIANT REGISTRY
            </span>
          </div>
          {/* Reference uses gap-xl (32px) between the three right-side items */}
          <div className="flex items-center gap-xl">
            <span className="font-label-caps text-label-caps text-[#44474d]">
              © 2024 ContainerRegistry
            </span>
            <a
              href="#"
              className="font-label-caps text-label-caps text-[#44474d] hover:text-[#000917]"
            >
              Privacy Policy
            </a>
            <a
              href="#"
              className="font-label-caps text-label-caps text-[#44474d] hover:text-[#000917]"
            >
              Terms of Service
            </a>
          </div>
        </div>
      </footer>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/*
 * Type-guard for axios errors. We avoid importing AxiosError directly so
 * this module doesn't take a hard dependency on the axios type internals —
 * the shape check is enough for our purposes.
 */
function isAxiosLike(
  err: unknown
): err is { response?: { status: number } } {
  return typeof err === 'object' && err !== null && 'response' in err
}
