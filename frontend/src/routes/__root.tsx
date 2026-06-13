import { createRootRouteWithContext, Outlet, Link, useRouter } from '@tanstack/react-router'
import { QueryClient } from '@tanstack/react-query'
import { Toaster } from 'sonner'

interface RouterContext {
  queryClient: QueryClient
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: RootComponent,
  notFoundComponent: NotFoundPage,
})

function RootComponent() {
  return (
    <>
      <Outlet />
      <Toaster position="top-right" richColors />
    </>
  )
}

// ---------------------------------------------------------------------------
// 404 — Not Found
// ---------------------------------------------------------------------------

/**
 * Full-page 404 screen rendered by TanStack Router whenever no route matches.
 * Uses the same design tokens as the rest of the app (primary-container dark
 * navy, secondary blue accents, Hanken Grotesk type).
 */
function NotFoundPage() {
  const router = useRouter()

  return (
    <div className="min-h-screen bg-primary-container flex items-center justify-center p-lg relative overflow-hidden">

      {/* Decorative background grid */}
      <div
        aria-hidden="true"
        className="absolute inset-0 pointer-events-none"
        style={{
          backgroundImage: `linear-gradient(rgba(255,255,255,0.03) 1px, transparent 1px),
                            linear-gradient(90deg, rgba(255,255,255,0.03) 1px, transparent 1px)`,
          backgroundSize: '40px 40px',
        }}
      />

      {/* Soft glow blob */}
      <div
        aria-hidden="true"
        className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[600px] h-[600px] rounded-full pointer-events-none"
        style={{
          background: 'radial-gradient(circle, rgba(47,96,150,0.15) 0%, transparent 70%)',
        }}
      />

      {/* Card */}
      <div className="relative z-10 max-w-lg w-full text-center">

        {/* Registry wordmark */}
        <div className="flex items-center justify-center gap-sm mb-2xl">
          <span
            className="material-symbols-outlined text-secondary text-2xl"
            style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
          >
            cloud
          </span>
          <span className="text-on-primary font-bold tracking-wide text-lg">
            ContainerRegistry
          </span>
        </div>

        {/* 404 number */}
        <div className="relative mb-lg">
          <span
            aria-hidden="true"
            className="absolute inset-0 flex items-center justify-center text-[200px] font-black leading-none select-none pointer-events-none"
            style={{ color: 'rgba(47,96,150,0.12)' }}
          >
            404
          </span>
          <span className="relative text-[96px] font-black leading-none text-secondary">
            404
          </span>
        </div>

        {/* Heading + description */}
        <h1 className="text-headline-lg text-on-primary font-bold mb-md">
          Page not found
        </h1>
        <p className="text-on-primary-container text-body-lg mb-2xl max-w-sm mx-auto">
          The page you're looking for doesn't exist or has been moved. Check the
          URL or head back to the dashboard.
        </p>

        {/* Action buttons */}
        <div className="flex flex-col sm:flex-row items-center justify-center gap-md">
          <Link
            to="/dashboard"
            className="flex items-center gap-sm bg-secondary text-on-primary px-xl py-md rounded-xl font-bold hover:opacity-90 transition-opacity w-full sm:w-auto justify-center"
          >
            <span className="material-symbols-outlined text-lg">home</span>
            Go to Dashboard
          </Link>
          <button
            type="button"
            onClick={() => router.history.back()}
            className="flex items-center gap-sm border border-on-primary-container text-on-primary-container px-xl py-md rounded-xl font-bold hover:bg-white/5 transition-colors w-full sm:w-auto justify-center"
          >
            <span className="material-symbols-outlined text-lg">arrow_back</span>
            Go Back
          </button>
        </div>

        {/* Decorative icon watermark */}
        <div
          aria-hidden="true"
          className="absolute -bottom-16 -right-16 opacity-5 pointer-events-none"
        >
          <span
            className="material-symbols-outlined text-[220px] text-secondary-container"
            style={{ fontVariationSettings: "'FILL' 1, 'wght' 400, 'GRAD' 0, 'opsz' 24" }}
          >
            search_off
          </span>
        </div>
      </div>
    </div>
  )
}
