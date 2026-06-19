/**
 * /repositories — list every repository in the current tenant.
 *
 * Page anatomy (top to bottom):
 *   1. PageHeader  — icon chip + title + workspace-scoped subtitle + CTA
 *   2. SummaryStrip — 4 mini-tiles (Total / Public / Private / Storage)
 *   3. Toolbar     — search input + visibility tabs
 *   4. Content     — empty state, error notice, skeleton, or the table
 *   5. Dialogs     — create + delete, mounted always so animations work
 *
 * Filter strategy: we fetch with `visibility='all'` so the SummaryStrip
 * can show workspace-wide counts even when the user has a tab applied.
 * The table view filters client-side off the same dataset, which is
 * fine for the per-page cap of 100 rows. Server-side filtering becomes
 * worthwhile when we wire infinite/keyset pagination later.
 *
 * Create/delete buttons in the topbar and the dashboard hero both route
 * here with `?new=true`, which auto-opens the create dialog on mount.
 */
import { useEffect, useMemo, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Globe, Lock, Package, Plus, Search } from 'lucide-react'
import { CreateRepositoryDialog } from '@/components/repositories/CreateRepositoryDialog'
import { DeleteRepositoryDialog } from '@/components/repositories/DeleteRepositoryDialog'
import { EmptyState } from '@/components/repositories/EmptyState'
import { RepositoriesTable } from '@/components/repositories/RepositoriesTable'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { useRepositories, type VisibilityFilter } from '@/lib/api/hooks/useRepositories'
import { formatBytes } from '@/lib/format/bytes'
import { cn } from '@/lib/utils/cn'

export const Route = createFileRoute('/_authenticated/repositories/')({
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

  // Always fetch the whole list — the SummaryStrip uses the full set so
  // its counts don't change with the visibility tab.
  const { data, isLoading, isError } = useRepositories('all')
  const allRepos = data?.repositories ?? []

  // Pop the create dialog when the route arrives with ?new=true, then
  // strip the param so a reload doesn't keep reopening it.
  useEffect(() => {
    if (openNew) {
      setCreateOpen(true)
      navigate({ to: '/repositories', search: { new: false }, replace: true })
    }
  }, [openNew, navigate])

  // Client-side filtering: visibility tab first, then name search.
  const filteredRepos = useMemo(() => {
    let rows = allRepos
    if (visibility !== 'all') {
      rows = rows.filter((r) =>
        visibility === 'public' ? r.is_public : !r.is_public,
      )
    }
    if (query.trim()) {
      const needle = query.trim().toLowerCase()
      rows = rows.filter((r) => r.name.toLowerCase().includes(needle))
    }
    return rows
  }, [allRepos, visibility, query])

  const hasRepos = allRepos.length > 0

  return (
    <div className="p-xl space-y-lg">
      <PageHeader
        count={allRepos.length}
        onCreate={() => setCreateOpen(true)}
      />

      {hasRepos && <SummaryStrip repos={allRepos} />}

      {hasRepos && (
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
      )}

      {isError && (
        <div className="rounded-sm border border-danger-500/30 bg-danger-100 text-danger-500 px-md py-sm text-label-md font-medium">
          Couldn't load repositories. The list will retry automatically.
        </div>
      )}

      {isLoading ? (
        <LoadingSkeleton />
      ) : !hasRepos ? (
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

/**
 * PageHeader — banner-style header for the repositories page.
 *
 * Same layering pattern as the dashboard hero:
 *   1. Warm gradient fallback (always renders)
 *   2. Higgsfield photograph at opacity-60 mix-blend-overlay
 *   3. Left-fading white veil so the heading + CTA stay readable
 *
 * The flat icon+title+button row previously sitting here read as a
 * "wall of text" — flagged in review. The banner gives the page a
 * single strong visual anchor that ties it back to the dashboard's
 * warm aesthetic without copy-pasting the dashboard hero.
 */
function PageHeader({
  count,
  onCreate,
}: {
  count: number
  onCreate: () => void
}) {
  return (
    <section
      aria-labelledby="repos-heading"
      className="relative overflow-hidden rounded-lg border border-border"
    >
      {/* Layer 1: fallback gradient — always renders. */}
      <div
        aria-hidden="true"
        className="absolute inset-0"
        style={{
          backgroundImage:
            'linear-gradient(110deg, oklch(0.95 0.06 50) 0%, oklch(0.96 0.04 30) 45%, oklch(0.99 0.02 60) 90%)',
        }}
      />
      {/* Layer 2: Higgsfield photograph blended on top of the gradient. */}
      <img
        src="/hero/repositories.png"
        alt=""
        aria-hidden="true"
        onError={(e) => {
          // Hide gracefully if the asset isn't deployed yet.
          ;(e.currentTarget as HTMLImageElement).style.display = 'none'
        }}
        className="absolute inset-0 w-full h-full object-cover opacity-60 mix-blend-overlay pointer-events-none"
      />
      {/* Layer 3: left-fading white veil for text legibility. */}
      <div
        aria-hidden="true"
        className="absolute inset-0"
        style={{
          background:
            'linear-gradient(105deg, oklch(1 0 0 / 0.65), oklch(1 0 0 / 0.30) 60%, transparent 90%)',
        }}
      />

      <div className="relative flex flex-col gap-md md:flex-row md:items-center md:justify-between px-xl py-xl">
        <div className="flex items-start gap-md">
          <span
            aria-hidden="true"
            className="inline-flex items-center justify-center w-12 h-12 rounded-md bg-primary-soft text-primary shadow-xs shrink-0"
          >
            <Package className="w-6 h-6" />
          </span>
          <div>
            <h1
              id="repos-heading"
              className="text-display-lg font-semibold text-on-surface tracking-tight"
            >
              Repositories
            </h1>
            <p className="mt-xs text-body-md text-on-surface-muted">
              {count > 0
                ? `${count.toLocaleString()} ${count === 1 ? 'repository' : 'repositories'} in this workspace.`
                : 'Container image repositories live here once you push one.'}
            </p>
          </div>
        </div>
        <Button variant="primary" size="lg" onClick={onCreate}>
          <Plus className="w-4 h-4" aria-hidden="true" />
          New repository
        </Button>
      </div>
    </section>
  )
}

/** Workspace summary — 4 mini-stat tiles. Computed off the full list
    so they don't fluctuate as the user toggles the visibility tab. */
function SummaryStrip({ repos }: { repos: { is_public: boolean; storage_used_bytes: number }[] }) {
  const total = repos.length
  const publicCount = repos.filter((r) => r.is_public).length
  const privateCount = total - publicCount
  const storage = formatBytes(repos.reduce((sum, r) => sum + r.storage_used_bytes, 0))

  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 gap-md">
      <MiniStat
        icon={<Package className="w-[18px] h-[18px]" />}
        label="Total"
        value={total.toLocaleString()}
        chip="bg-primary-soft text-primary"
      />
      <MiniStat
        icon={<Globe className="w-[18px] h-[18px]" />}
        label="Public"
        value={publicCount.toLocaleString()}
        chip="bg-success-100 text-success-500"
      />
      <MiniStat
        icon={<Lock className="w-[18px] h-[18px]" />}
        label="Private"
        value={privateCount.toLocaleString()}
        chip="bg-info-100 text-info-500"
      />
      <MiniStat
        icon={<span className="text-label-md font-mono font-semibold">{storage.unit}</span>}
        label="Storage used"
        value={storage.value.toLocaleString()}
        chip="bg-warning-100 text-warning-500"
      />
    </div>
  )
}

/** Single mini-tile used by SummaryStrip — icon chip + label + value. */
function MiniStat({
  icon,
  label,
  value,
  chip,
}: {
  icon: React.ReactNode
  label: string
  value: string
  chip: string
}) {
  return (
    <div className="flex items-center gap-md rounded-lg border border-border bg-surface p-md">
      <span
        aria-hidden="true"
        className={cn(
          'inline-flex items-center justify-center w-9 h-9 rounded-sm shrink-0',
          chip,
        )}
      >
        {icon}
      </span>
      <div className="min-w-0">
        <div className="text-label-sm uppercase tracking-wider text-on-surface-subtle font-semibold">
          {label}
        </div>
        <div className="text-heading-sm font-semibold text-on-surface tabular-nums">
          {value}
        </div>
      </div>
    </div>
  )
}

/** Tab-style visibility filter. Maps to the local `visibility` state. */
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
