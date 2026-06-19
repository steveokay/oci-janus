/**
 * Breadcrumbs — derived from TanStack Router matches.
 *
 * Each route opts in by declaring `staticData: { crumb: 'Label' }` in its
 * route config. We walk the matched routes, keep the ones that opted in,
 * and render them as a `›`-separated trail. The last entry is rendered as
 * plain text with `aria-current="page"` per WAI-ARIA breadcrumb pattern;
 * everything before it is a router Link back up the hierarchy.
 */
import { Link, useMatches } from '@tanstack/react-router'
import { ChevronRight } from 'lucide-react'

interface Crumb {
  pathname: string
  label: string
}

export function Breadcrumbs() {
  const matches = useMatches()
  const crumbs: Crumb[] = matches
    .map((m) => {
      const data = m.staticData as { crumb?: string } | undefined
      return data?.crumb
        ? { pathname: m.pathname, label: data.crumb }
        : null
    })
    .filter((c): c is Crumb => c !== null)

  if (crumbs.length === 0) return null

  return (
    <nav aria-label="Breadcrumb">
      <ol className="flex items-center gap-xs">
        {crumbs.map((c, i) => {
          const isLast = i === crumbs.length - 1
          return (
            <li key={c.pathname} className="flex items-center gap-xs">
              {i > 0 && (
                <ChevronRight
                  className="w-3.5 h-3.5 text-on-surface-subtle"
                  aria-hidden="true"
                />
              )}
              {isLast ? (
                <span
                  className="text-body-sm font-medium text-on-surface"
                  aria-current="page"
                >
                  {c.label}
                </span>
              ) : (
                // `pathname` came from the route match itself, so the
                // runtime path is always valid even though TS can't prove
                // it against the typed-routes generic.
                <Link
                  to={c.pathname as never}
                  className="text-body-sm text-on-surface-muted hover:text-on-surface transition-colors"
                >
                  {c.label}
                </Link>
              )}
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
