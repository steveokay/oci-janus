import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useEffect } from 'react'
import { toast } from 'sonner'
import {
  User,
  Lock,
  Loader2,
} from 'lucide-react'
import { apiClient } from '@/lib/api/client'
import { useAuthStore, type AuthUser } from '@/store/authStore'

// ---------------------------------------------------------------------------
// Route
// ---------------------------------------------------------------------------

export const Route = createFileRoute('/login')({
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
      <header className="px-6 flex items-center justify-between w-full max-w-[1440px] mx-auto h-16 sticky top-0 z-50">
        <div className="flex items-center gap-1">
          {/*
           * Material Symbols inventory_2 matches the reference exactly —
           * FILL 1 renders the solid/filled variant of the box icon.
           * The font is already loaded via index.html via Google Fonts CDN.
           */}
          <span
            className="material-symbols-outlined text-[#000917] text-[20px] leading-none"
            style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
          >
            inventory_2
          </span>
          <span className="text-xl font-bold text-[#0b1c30]">ContainerRegistry</span>
        </div>
        <div className="flex items-center gap-4">
          {/* label-caps in the reference is 12px/700/0.05em tracking but NOT text-transform:uppercase
              — the reference renders "Documentation" and "Support" in mixed case */}
          <a
            href="#"
            className="text-xs font-bold tracking-[0.05em] text-[#44474d] hover:text-[#000917] transition-colors"
          >
            Documentation
          </a>
          <a
            href="#"
            className="text-xs font-bold tracking-[0.05em] text-[#44474d] hover:text-[#000917] transition-colors"
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

        {/* Login card — rounded-[4px] matches reference's custom "lg" = 0.25rem border-radius */}
        <div className="w-full max-w-[440px] login-card-blur border border-[#c4c6cd] p-6 md:p-8 shadow-lg relative z-10 rounded-[4px]">
          {/* Heading */}
          <div className="mb-8">
            <h1 className="text-[28px] leading-9 font-semibold text-[#0b1c30] mb-1">
              Welcome back
            </h1>
            <p className="text-sm text-[#44474d]">
              Access your secure image repositories.
            </p>
          </div>

          {/* Form */}
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
            {/* Root/server error banner */}
            {errors.root && (
              <p className="text-xs text-[#ba1a1a] bg-[#ffdad6] border border-[#ba1a1a]/30 rounded px-3 py-2">
                {errors.root.message}
              </p>
            )}

            {/* Username / email */}
            <div className="space-y-1">
              <label
                htmlFor="username"
                className="block text-xs font-bold uppercase tracking-widest text-[#44474d]"
              >
                Username or Email
              </label>
              <div className="relative group">
                <User
                  className="absolute left-4 top-1/2 -translate-y-1/2 w-4 h-4 text-[#74777d] group-focus-within:text-[#000917] transition-colors"
                  aria-hidden="true"
                />
                <input
                  id="username"
                  type="text"
                  autoComplete="username"
                  placeholder="e.g. j.doe@company.io"
                  aria-invalid={!!errors.username}
                  aria-describedby={errors.username ? 'username-error' : undefined}
                  className={[
                    // rounded-[4px] matches reference rounded-lg = 0.25rem (custom scale overrides Tailwind default 8px)
                    'w-full pl-12 pr-4 py-2 bg-[#eff4ff] border rounded-[4px] text-sm',
                    'focus:outline-none focus:ring-0 transition-all',
                    errors.username
                      ? 'border-[#ba1a1a] focus:border-[#ba1a1a]'
                      : 'border-[#c4c6cd] focus:border-[#000917]',
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
            <div className="space-y-1">
              <div className="flex items-center justify-between">
                <label
                  htmlFor="password"
                  className="block text-xs font-bold uppercase tracking-widest text-[#44474d]"
                >
                  Password
                </label>
                {/* label-caps: 12px/700/0.05em — NOT uppercase; reference renders "Forgot Password?" in mixed case */}
                <a
                  href="#"
                  className="text-xs font-bold tracking-[0.05em] text-[#2f6096] hover:underline"
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
                <Lock
                  className="absolute left-4 top-1/2 -translate-y-1/2 w-4 h-4 text-[#74777d] group-focus-within:text-[#000917] transition-colors"
                  aria-hidden="true"
                />
                <input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  placeholder="••••••••"
                  aria-invalid={!!errors.password}
                  aria-describedby={errors.password ? 'password-error' : undefined}
                  className={[
                    // rounded-[4px] matches reference rounded-lg = 0.25rem (custom scale overrides Tailwind default 8px)
                    // pr-4 instead of pr-12: no eye-toggle button on the right
                    'w-full pl-12 pr-4 py-2 bg-[#eff4ff] border rounded-[4px] text-sm',
                    'focus:outline-none focus:ring-0 transition-all',
                    errors.password
                      ? 'border-[#ba1a1a] focus:border-[#ba1a1a]'
                      : 'border-[#c4c6cd] focus:border-[#000917]',
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
              className="w-full py-4 mt-6 bg-[#0d2137] text-white text-[20px] leading-7 font-semibold rounded-[4px]
                         hover:opacity-90 active:scale-[0.98] transition-all
                         flex items-center justify-center gap-2
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
          <div className="relative flex items-center my-8">
            <div className="flex-grow border-t border-[#c4c6cd]" />
            <span className="px-4 text-xs font-bold uppercase tracking-widest text-[#44474d]">
              Or continue with
            </span>
            <div className="flex-grow border-t border-[#c4c6cd]" />
          </div>

          {/* SSO — py-4 (16px) matches reference py-md=16px; text-[20px] matches headline-md;
              rounded-[4px] matches reference rounded-lg=0.25rem; hover bg matches surface-container-low */}
          <button
            type="button"
            className="w-full py-4 border border-[#c4c6cd] bg-white text-[#0b1c30] text-[20px] leading-7 font-semibold
                       rounded-[4px] hover:bg-[#eff4ff] active:scale-[0.98] transition-all
                       flex items-center justify-center gap-2"
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
          <div className="mt-8 text-center">
            <p className="text-sm text-[#44474d]">
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
      <footer className="px-6 py-6 border-t border-[#c4c6cd] bg-white">
        <div className="max-w-[1440px] mx-auto flex flex-col md:flex-row items-center justify-between gap-4">
          <div className="flex items-center gap-2">
            {/*
             * Material Symbols verified_user matches the reference footer icon exactly.
             * on-tertiary-container (#009c54) is the green color used in the reference.
             */}
            <span
              className="material-symbols-outlined text-[#009c54] text-[16px] leading-none"
              aria-hidden="true"
            >
              verified_user
            </span>
            <span className="text-xs font-bold uppercase tracking-widest text-[#44474d]">
              FIPS 140-2 Compliant Registry
            </span>
          </div>
          {/* label-caps is 12px/700/0.05em tracking — reference renders these in mixed case, NOT all-caps */}
          <div className="flex items-center gap-8">
            <span className="text-xs font-bold tracking-[0.05em] text-[#44474d]">
              © 2024 ContainerRegistry
            </span>
            <a
              href="#"
              className="text-xs font-bold tracking-[0.05em] text-[#44474d] hover:text-[#000917]"
            >
              Privacy Policy
            </a>
            <a
              href="#"
              className="text-xs font-bold tracking-[0.05em] text-[#44474d] hover:text-[#000917]"
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
