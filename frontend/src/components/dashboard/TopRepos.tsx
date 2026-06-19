/**
 * TopRepos — "most active repositories" card for the dashboard.
 *
 * Each row: avatar (initial), name, tag count, delta vs prior period.
 * Clicking a row will route to the repo detail page once Sprint 1d lands;
 * for now it's a hoverable surface that shows a "coming soon" toast.
 */
import { ArrowDown, ArrowRight, ArrowUp } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from '@/lib/utils/cn'
import type { DemoRepo } from '@/lib/demo/dashboardData'

export function TopRepos({ repos }: { repos: DemoRepo[] }) {
  return (
    <section
      aria-labelledby="top-repos-heading"
      className="flex flex-col rounded-lg border border-border bg-surface"
    >
      <header className="flex items-center justify-between p-lg pb-md border-b border-border">
        <h2
          id="top-repos-heading"
          className="text-heading-sm font-semibold text-on-surface"
        >
          Top repositories
        </h2>
        <a
          href="#"
          onClick={(e) => {
            e.preventDefault()
            toast.message('Repository list is coming in Sprint 1c')
          }}
          className="text-label-md text-on-surface-muted hover:text-on-surface transition-colors"
        >
          See all
        </a>
      </header>
      <ul className="divide-y divide-border">
        {repos.map((repo) => (
          <li key={repo.name}>
            <button
              type="button"
              onClick={() =>
                toast.message(`Repo "${repo.name}" detail is coming in Sprint 1d`)
              }
              className="flex items-center gap-md w-full px-lg py-md text-left hover:bg-surface-muted transition-colors"
            >
              <span
                aria-hidden="true"
                className="inline-flex items-center justify-center w-8 h-8 rounded-sm bg-neutral-100 text-on-surface font-mono text-label-md font-semibold"
              >
                {repo.name.charAt(0).toUpperCase()}
              </span>
              <div className="flex-1 min-w-0">
                <div className="text-body-sm font-medium text-on-surface truncate">
                  {repo.name}
                </div>
                <div className="text-label-sm text-on-surface-subtle">
                  {repo.tags} {repo.tags === 1 ? 'tag' : 'tags'}
                </div>
              </div>
              <RepoDelta deltaPct={repo.deltaPct} />
            </button>
          </li>
        ))}
      </ul>
    </section>
  )
}

function RepoDelta({ deltaPct }: { deltaPct: number }) {
  if (deltaPct === 0) {
    return (
      <span className="inline-flex items-center gap-xs text-label-sm text-on-surface-subtle">
        <ArrowRight className="w-3 h-3" aria-hidden="true" />
        Flat
      </span>
    )
  }
  const isUp = deltaPct > 0
  return (
    <span
      className={cn(
        'inline-flex items-center gap-xs text-label-sm font-medium tabular-nums',
        isUp ? 'text-success-500' : 'text-danger-500',
      )}
    >
      {isUp ? (
        <ArrowUp className="w-3 h-3" aria-hidden="true" />
      ) : (
        <ArrowDown className="w-3 h-3" aria-hidden="true" />
      )}
      {Math.abs(deltaPct)}%
    </span>
  )
}
