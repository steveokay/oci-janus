/**
 * /repositories — list every repository in the current tenant.
 *
 * Sprint 1c surface: table + name search + visibility filter + create
 * dialog + delete-with-confirm. Create/delete buttons in the topbar and
 * the dashboard hero both route here with `?new=true`, which auto-opens
 * the create dialog on mount.
 *
 * Filter state lives in local React state for now. URL-driven filter
 * state (so a filtered view is shareable + survives refresh) is a small
 * follow-up — captured below in the comments rather than the tracker
 * because the change is local to this file.
 */
import { useEffect, useMemo, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Search } from 'lucide-react'
import { CreateRepositoryDialog } from '@/components/repositories/CreateRepositoryDialog'
import { DeleteRepositoryDialog } from '@/components/repositories/DeleteRepositoryDialog'
import { EmptyState } from '@/components/repositories/EmptyState'
import { RepositoriesTable } from '@/components/repositories/RepositoriesTable'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import {
  useRepositories,
  type VisibilityFilter,
} from '@/lib/api/hooks/useRepositories'

export const Route = createFileRoute('/_authenticated/repositories')({
  staticData: { crumb: 'Repositories' },
  validateSearch: (search) => ({
    // `?new=true` opens the create dialog on mount; anything else
    // collapses to false so a stray query param doesn't surprise the user.
    new: search.new === true || search.new === 'true',
  }),
  component: RepositoriesPage,
})

function RepositoriesPage() {
  const navigate = useNavigate()
  const { new: openNew } = Route.useSearch()
  const [visibility, setVisibility] = useState<VisibilityFilter>('all')
  const [query, setQuery] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)

  const { data, isLoading, isError } = useRepositories(visibility)

  // Pop the create dialog when the route arrives with ?new=true, then
  // strip the param so a reload doesn't keep reopening it.
  useEffect(() => {
    if (openNew) {
      setCreateOpen(true)
      navigate({ to: '/repositories', search: { new: false }, replace: true })
    }
  }, [openNew, navigate])

  // Client-side name search across whatever the server returned.
  const filteredRepos = useMemo(() => {
    const all = data?.repositories ?? []
    if (!query.trim()) return all
    const needle = query.trim().toLowerCase()
    return all.filter((r) => r.name.toLowerCase().includes(needle))
  }, [data, query])

  return (
    <div className="p-xl space-y-lg">
      <PageHeader onCreate={() => setCreateOpen(true)} />

      <div className="flex flex-col gap-md sm:flex-row sm:items-center sm:justify-between">
        <div className="relative w-full sm:max-w-sm">
          <Search
            aria-hidden="true"
            className="absolute left-md top-1/2 -translate-y-1/2 w-4 h-4 text-on-surface-subtle pointer-events-none"
          />
          <Input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search by name…"
            aria-label="Search repositories by name"
            className="pl-2xl"
          />
        </div>
        <VisibilityTabs value={visibility} onChange={setVisibility} />
      </div>

      {isError && (
        <div className="rounded-sm border border-danger-500/30 bg-danger-100 text-danger-500 px-md py-sm text-label-md font-medium">
          Couldn't load repositories. The list will retry automatically.
        </div>
      )}

      {isLoading ? (
        <LoadingSkeleton />
      ) : (data?.repositories.length ?? 0) === 0 ? (
        <EmptyState onCreate={() => setCreateOpen(true)} />
      ) : filteredRepos.length === 0 ? (
        <div className="rounded-lg border border-border bg-surface p-2xl text-center text-body-sm text-on-surface-muted">
          No repositories match <strong className="text-on-surface">"{query}"</strong>.
        </div>
      ) : (
        <RepositoriesTable repos={filteredRepos} onDelete={setDeleteTarget} />
      )}

      <CreateRepositoryDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
      />
      <DeleteRepositoryDialog
        target={deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      />
    </div>
  )
}

/** Page-level header — title + create button on the right. */
function PageHeader({ onCreate }: { onCreate: () => void }) {
  return (
    <header className="flex flex-col gap-md sm:flex-row sm:items-end sm:justify-between">
      <div>
        <h1 className="text-display-lg font-semibold text-on-surface tracking-tight">
          Repositories
        </h1>
        <p className="mt-xs text-body-md text-on-surface-muted">
          Container image repositories in this workspace.
        </p>
      </div>
      <Button variant="primary" onClick={onCreate}>
        New repository
      </Button>
    </header>
  )
}

/** Tab-style visibility filter. Maps to the `visibility` query param. */
function VisibilityTabs({
  value,
  onChange,
}: {
  value: VisibilityFilter
  onChange: (v: VisibilityFilter) => void
}) {
  const tabs: { id: VisibilityFilter; label: string }[] = [
    { id: 'all', label: 'All' },
    { id: 'public', label: 'Public' },
    { id: 'private', label: 'Private' },
  ]
  return (
    <div
      role="tablist"
      aria-label="Filter by visibility"
      className="inline-flex items-center rounded-sm border border-border bg-surface p-0.5"
    >
      {tabs.map((tab) => {
        const active = tab.id === value
        return (
          <button
            key={tab.id}
            role="tab"
            aria-selected={active}
            type="button"
            onClick={() => onChange(tab.id)}
            className={
              active
                ? 'px-md h-7 rounded-xs bg-primary-soft text-primary text-label-md font-medium transition-colors'
                : 'px-md h-7 rounded-xs text-on-surface-muted hover:text-on-surface text-label-md font-medium transition-colors'
            }
          >
            {tab.label}
          </button>
        )
      })}
    </div>
  )
}

/** Skeleton placeholders — 5 rows of pulsing rectangles. */
function LoadingSkeleton() {
  return (
    <div className="rounded-lg border border-border bg-surface divide-y divide-border">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="flex items-center gap-md p-lg">
          <span className="w-8 h-8 rounded-sm bg-surface-muted animate-pulse" />
          <span className="flex-1 h-4 max-w-xs rounded-xs bg-surface-muted animate-pulse" />
          <span className="w-16 h-5 rounded-full bg-surface-muted animate-pulse" />
          <span className="w-20 h-4 rounded-xs bg-surface-muted animate-pulse" />
          <span className="w-24 h-4 rounded-xs bg-surface-muted animate-pulse" />
        </div>
      ))}
    </div>
  )
}
