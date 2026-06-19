/**
 * PinnedRepos — replaces the previous demo-data `TopRepos` panel.
 *
 * Reads pin state from `pinnedReposStore` (localStorage-backed). Each
 * pinned row fetches its own repo via `useRepository(org, repo)` so
 * the cache is shared with the detail page.
 *
 * Visual treatment:
 *   * Each row gets a deterministic gradient avatar via `<Avatar>`
 *     instead of a uniform letter-on-grey chip. Twenty pins read as
 *     twenty distinct things.
 *   * Row entries stagger in via framer-motion when the component
 *     mounts so the panel feels alive on first paint.
 *   * Empty state uses the pin illustration.
 */
import { Link } from '@tanstack/react-router'
import { motion } from 'framer-motion'
import { ArrowRight, Pin } from 'lucide-react'
import { usePinnedReposStore, type PinnedRepo } from '@/store/pinnedReposStore'
import { useRepository } from '@/lib/api/hooks/useRepositories'
import { formatBytes } from '@/lib/format/bytes'
import { Avatar } from '@/components/ui/Avatar'
import { PinIllustration } from '@/components/ui/illustrations'
import { listContainer, listItem } from '@/lib/motion'
import { cn } from '@/lib/utils/cn'

export function PinnedRepos() {
  const pinned = usePinnedReposStore((s) => s.pinned)

  return (
    <section
      aria-labelledby="pinned-heading"
      className="flex flex-col rounded-lg border border-border bg-surface"
    >
      <header className="flex items-center justify-between p-lg pb-md border-b border-border">
        <div className="flex items-center gap-sm">
          <span
            aria-hidden="true"
            className="inline-flex items-center justify-center w-8 h-8 rounded-sm bg-primary-soft text-primary"
          >
            <Pin className="w-4 h-4" />
          </span>
          <h2
            id="pinned-heading"
            className="text-heading-sm font-semibold text-on-surface"
          >
            Pinned repositories
          </h2>
        </div>
        <Link
          to="/repositories"
          search={{ new: false }}
          className="inline-flex items-center gap-xs text-label-md text-on-surface-muted hover:text-on-surface transition-colors"
        >
          See all
          <ArrowRight className="w-3.5 h-3.5" aria-hidden="true" />
        </Link>
      </header>

      {pinned.length === 0 ? (
        <PinnedEmpty />
      ) : (
        <motion.ul
          className="divide-y divide-border"
          variants={listContainer}
          initial="initial"
          animate="animate"
        >
          {pinned.map((p) => (
            <motion.li key={`${p.org}/${p.repo}`} variants={listItem}>
              <PinnedRow pin={p} />
            </motion.li>
          ))}
        </motion.ul>
      )}
    </section>
  )
}

function PinnedRow({ pin }: { pin: PinnedRepo }) {
  const { data, isError } = useRepository(pin.org, pin.repo)
  const fullName = `${pin.org}/${pin.repo}`
  const storage = data ? formatBytes(data.storage_used_bytes) : undefined

  return (
    <Link
      to="/repositories/$org/$repo"
      params={{ org: pin.org, repo: pin.repo }}
      className={cn(
        'flex items-center gap-md px-lg py-md hover:bg-surface-muted transition-colors',
        isError && 'opacity-60',
      )}
    >
      <Avatar seed={fullName} size="md" />
      <div className="flex-1 min-w-0">
        <div className="text-body-sm font-medium text-on-surface truncate font-mono">
          {fullName}
        </div>
        <div className="text-label-sm text-on-surface-subtle truncate">
          {isError
            ? 'Repository missing or no access'
            : storage
            ? `${storage.value} ${storage.unit}${data?.is_public ? ' · Public' : ''}`
            : 'Loading…'}
        </div>
      </div>
      <ArrowRight
        className="w-4 h-4 text-on-surface-subtle"
        aria-hidden="true"
      />
    </Link>
  )
}

function PinnedEmpty() {
  return (
    <div className="p-2xl text-center flex flex-col items-center gap-md">
      <PinIllustration className="w-24 h-24 text-primary" />
      <div className="max-w-prose">
        <h3 className="text-heading-sm font-semibold text-on-surface">
          Pin your most-used repos
        </h3>
        <p className="mt-xs text-body-sm text-on-surface-muted">
          Open any repository and use the pin icon next to its name to
          surface it here. Pinned repos persist on this browser.
        </p>
      </div>
      <Link
        to="/repositories"
        search={{ new: false }}
        className="inline-flex items-center gap-xs h-9 px-md rounded-sm border border-border bg-surface text-body-sm font-medium text-on-surface hover:bg-surface-muted hover:border-border-strong transition-colors"
      >
        Browse repositories
        <ArrowRight className="w-3.5 h-3.5" aria-hidden="true" />
      </Link>
    </div>
  )
}
