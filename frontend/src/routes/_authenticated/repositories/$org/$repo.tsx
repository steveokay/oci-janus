/**
 * /repositories/$org/$repo — Image / tag detail page.
 *
 * Page anatomy (top to bottom):
 *   1. Hero banner — back link, icon chip + "org/repo" h1 + visibility
 *      pill, delete button. Layered like the dashboard hero so the page
 *      reads as part of the same warm visual family.
 *   2. Stats strip — Tags / Storage / Last push / Created.
 *   3. Tags table — TanStack Table with name / digest / pushed time /
 *      per-row delete.
 *
 * Sprint 1d ships everything except manifest layers + signing status —
 * those need new backend endpoints (`/manifest` for layer sizes, signer
 * service for verification). Tagged as follow-ups in REBUILD-PLAN.md so
 * the scope here stays small.
 */
import { useEffect, useMemo, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import {
  ArrowLeft,
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Calendar,
  Check,
  Clock,
  Copy,
  Globe,
  HardDrive,
  Lock,
  Package,
  ShieldAlert,
  ShieldCheck,
  ShieldQuestion,
  Tag as TagIcon,
  Terminal,
  Trash2,
  TriangleAlert,
  X,
} from 'lucide-react'
import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from '@tanstack/react-table'
import * as Dialog from '@radix-ui/react-dialog'
import { AxiosError } from 'axios'
import { toast } from 'sonner'
import { Search } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { Input, Label, FieldHint } from '@/components/ui/Input'
import { DeleteRepositoryDialog } from '@/components/repositories/DeleteRepositoryDialog'
import {
  useDeleteTag,
  useRepository,
  useScan,
  useTags,
  type TagResponse,
} from '@/lib/api/hooks/useRepositories'
import { formatBytes } from '@/lib/format/bytes'
import { relativeTime } from '@/lib/format/time'
import { cn } from '@/lib/utils/cn'

export const Route = createFileRoute('/_authenticated/repositories/$org/$repo')({
  staticData: { crumb: 'Repository' },
  component: RepositoryDetailPage,
})

function RepositoryDetailPage() {
  const { org, repo } = Route.useParams()
  const fullName = `${org}/${repo}`
  const navigate = useNavigate()

  const { data: repository, isLoading: repoLoading, isError: repoErr } =
    useRepository(org, repo)
  const { data: tags = [], isLoading: tagsLoading, isError: tagsErr } =
    useTags(org, repo)

  const [deleteRepoOpen, setDeleteRepoOpen] = useState(false)
  const [deleteTagName, setDeleteTagName] = useState<string | null>(null)
  const [tagQuery, setTagQuery] = useState('')

  // Client-side filter — tag name OR manifest digest substring,
  // case-insensitive. Digest search supports the common "paste a
  // partial sha256:abc…" workflow when you've pulled a specific
  // version somewhere and want to find it here.
  const filteredTags = useMemo(() => {
    const needle = tagQuery.trim().toLowerCase()
    if (!needle) return tags
    return tags.filter(
      (t) =>
        t.name.toLowerCase().includes(needle) ||
        t.manifest_digest.toLowerCase().includes(needle),
    )
  }, [tags, tagQuery])

  return (
    <div className="p-xl space-y-lg">
      <DetailHeader
        fullName={fullName}
        isPublic={repository?.is_public}
        loading={repoLoading}
        notFound={repoErr}
        onDelete={() => setDeleteRepoOpen(true)}
      />

      {repository && tags.length > 0 && (
        <PullCommandCard fullName={fullName} tags={tags} />
      )}

      {repository && (
        <StatsStrip
          tagCount={tags.length}
          storageUsedBytes={repository.storage_used_bytes}
          storageQuotaBytes={repository.storage_quota_bytes}
          createdAt={repository.created_at}
          lastPushAt={
            tags.length > 0
              ? tags
                  .slice()
                  .sort(
                    (a, b) =>
                      new Date(b.updated_at).getTime() -
                      new Date(a.updated_at).getTime(),
                  )[0].updated_at
              : undefined
          }
        />
      )}

      {tagsErr ? (
        <LoadErrorPanel />
      ) : tagsLoading ? (
        <TagsTableSkeleton />
      ) : tags.length === 0 ? (
        <EmptyTagsPanel fullName={fullName} />
      ) : (
        <>
          {/* Search row — explicit width so the input never collapses on
              intermediate viewports. Showing a result counter on the
              right when filtering gives the user a visual confirmation
              the filter is doing something. */}
          <div className="flex flex-col gap-sm sm:flex-row sm:items-center sm:justify-between">
            <div className="relative w-full sm:w-[28rem]">
              <Search
                aria-hidden="true"
                className="absolute left-md top-1/2 -translate-y-1/2 w-4 h-4 text-on-surface-subtle pointer-events-none"
              />
              <Input
                type="search"
                value={tagQuery}
                onChange={(e) => setTagQuery(e.target.value)}
                placeholder="Search by tag or digest…"
                aria-label="Search tags by name or manifest digest"
                className="pl-[2.5rem]"
              />
            </div>
            {tagQuery.trim() && (
              <span className="text-label-md text-on-surface-muted tabular-nums">
                {filteredTags.length} of {tags.length}
              </span>
            )}
          </div>

          {filteredTags.length === 0 ? (
            <NoMatchPanel query={tagQuery} onClear={() => setTagQuery('')} />
          ) : (
            <TagsTable
              tags={filteredTags}
              org={org}
              repo={repo}
              fullName={fullName}
              onDelete={setDeleteTagName}
            />
          )}
        </>
      )}

      <DeleteRepositoryDialog
        target={deleteRepoOpen ? fullName : null}
        onOpenChange={(open) => {
          if (!open) setDeleteRepoOpen(false)
          // After a successful repo delete the dialog closes itself; we
          // also bounce back to the list so the user lands on a useful
          // surface rather than a 404 for the now-gone repo.
          if (!open && !repository) navigate({ to: '/repositories', search: { new: false } })
        }}
      />
      <DeleteTagDialog
        org={org}
        repo={repo}
        target={deleteTagName}
        onOpenChange={(open) => !open && setDeleteTagName(null)}
      />
    </div>
  )
}

/** Hero banner for the detail page. Reuses the repositories hero image
    so the visual continuity from list → detail is obvious. */
function DetailHeader({
  fullName,
  isPublic,
  loading,
  notFound,
  onDelete,
}: {
  fullName: string
  isPublic: boolean | undefined
  loading: boolean
  notFound: boolean
  onDelete: () => void
}) {
  return (
    <section
      aria-labelledby="repo-detail-heading"
      className="relative overflow-hidden rounded-lg border border-border"
    >
      <div
        aria-hidden="true"
        className="absolute inset-0"
        style={{
          backgroundImage:
            'linear-gradient(110deg, oklch(0.95 0.06 50) 0%, oklch(0.96 0.04 30) 45%, oklch(0.99 0.02 60) 90%)',
        }}
      />
      <img
        src="/hero/repositories.png"
        alt=""
        aria-hidden="true"
        onError={(e) => {
          ;(e.currentTarget as HTMLImageElement).style.display = 'none'
        }}
        className="absolute inset-0 w-full h-full object-cover opacity-60 mix-blend-overlay pointer-events-none"
      />
      <div
        aria-hidden="true"
        className="absolute inset-0"
        style={{
          background:
            'linear-gradient(105deg, oklch(1 0 0 / 0.65), oklch(1 0 0 / 0.30) 60%, transparent 90%)',
        }}
      />

      <div className="relative px-xl py-xl">
        <Link
          to="/repositories"
          search={{ new: false }}
          className="inline-flex items-center gap-xs text-label-md text-on-surface-muted hover:text-on-surface transition-colors"
        >
          <ArrowLeft className="w-3.5 h-3.5" aria-hidden="true" />
          Repositories
        </Link>

        <div className="mt-md flex flex-col gap-md md:flex-row md:items-end md:justify-between">
          <div className="flex items-start gap-md min-w-0">
            <span
              aria-hidden="true"
              className="inline-flex items-center justify-center w-12 h-12 rounded-md bg-primary-soft text-primary shadow-xs shrink-0"
            >
              <Package className="w-6 h-6" />
            </span>
            <div className="min-w-0">
              <h1
                id="repo-detail-heading"
                className="text-display-lg font-semibold text-on-surface tracking-tight font-mono truncate"
              >
                {fullName}
              </h1>
              <div className="mt-xs flex items-center gap-sm">
                {loading ? (
                  <span className="inline-block w-16 h-5 rounded-full bg-surface-muted animate-pulse" />
                ) : notFound ? (
                  <span className="text-body-sm text-danger-500 font-medium">
                    Repository not found
                  </span>
                ) : (
                  <VisibilityPill isPublic={!!isPublic} />
                )}
              </div>
            </div>
          </div>

          {!notFound && (
            <Button variant="secondary" onClick={onDelete} disabled={loading}>
              <Trash2 className="w-4 h-4" aria-hidden="true" />
              Delete repository
            </Button>
          )}
        </div>
      </div>
    </section>
  )
}

/** 4 mini-tiles summarising the repo. */
function StatsStrip({
  tagCount,
  storageUsedBytes,
  storageQuotaBytes,
  createdAt,
  lastPushAt,
}: {
  tagCount: number
  storageUsedBytes: number
  storageQuotaBytes: number
  createdAt: string
  lastPushAt: string | undefined
}) {
  const used = formatBytes(storageUsedBytes)
  const quota = formatBytes(storageQuotaBytes)
  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 gap-md">
      <MiniStat
        icon={<TagIcon className="w-[18px] h-[18px]" />}
        label="Tags"
        value={tagCount.toLocaleString()}
        chip="bg-primary-soft text-primary"
      />
      <MiniStat
        icon={<HardDrive className="w-[18px] h-[18px]" />}
        label="Storage"
        value={`${used.value} ${used.unit}`}
        subtitle={
          storageQuotaBytes > 0 ? `of ${quota.value} ${quota.unit}` : undefined
        }
        chip="bg-warning-100 text-warning-500"
      />
      <MiniStat
        icon={<Clock className="w-[18px] h-[18px]" />}
        label="Last push"
        value={lastPushAt ? relativeTime(lastPushAt) : '—'}
        chip="bg-success-100 text-success-500"
      />
      <MiniStat
        icon={<Calendar className="w-[18px] h-[18px]" />}
        label="Created"
        value={relativeTime(createdAt)}
        chip="bg-info-100 text-info-500"
      />
    </div>
  )
}

function MiniStat({
  icon,
  label,
  value,
  subtitle,
  chip,
}: {
  icon: React.ReactNode
  label: string
  value: string
  subtitle?: string
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
        <div className="text-heading-sm font-semibold text-on-surface tabular-nums truncate">
          {value}
        </div>
        {subtitle && (
          <div className="text-label-sm text-on-surface-subtle tabular-nums">
            {subtitle}
          </div>
        )}
      </div>
    </div>
  )
}

/** Tags table — TanStack Table, client-side sort, no pagination yet. */
function TagsTable({
  tags,
  org,
  repo,
  fullName,
  onDelete,
}: {
  tags: TagResponse[]
  org: string
  repo: string
  fullName: string
  onDelete: (tagName: string) => void
}) {
  const [sorting, setSorting] = useState<SortingState>([
    { id: 'updated_at', desc: true },
  ])

  const columns = useMemo<ColumnDef<TagResponse>[]>(
    () => [
      {
        accessorKey: 'name',
        header: 'Tag',
        cell: (info) => (
          <span className="font-mono text-body-sm font-medium text-on-surface">
            {info.getValue<string>()}
          </span>
        ),
      },
      {
        accessorKey: 'manifest_digest',
        header: 'Digest',
        cell: (info) => <DigestCell digest={info.getValue<string>()} />,
        enableSorting: false,
      },
      {
        id: 'scan',
        header: 'Scan',
        cell: ({ row }) => (
          <ScanCell org={org} repo={repo} tag={row.original.name} />
        ),
        enableSorting: false,
      },
      {
        accessorKey: 'updated_at',
        header: 'Last push',
        cell: (info) => (
          <span className="text-body-sm text-on-surface-muted">
            {relativeTime(info.getValue<string>())}
          </span>
        ),
        sortingFn: (a, b) =>
          new Date(a.original.updated_at).getTime() -
          new Date(b.original.updated_at).getTime(),
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">Actions</span>,
        cell: ({ row }) => (
          <div className="flex items-center justify-end gap-xs">
            <PullCopyButton fullName={fullName} tag={row.original.name} />
            <button
              type="button"
              aria-label={`Delete tag ${row.original.name}`}
              onClick={() => onDelete(row.original.name)}
              className="inline-flex items-center justify-center w-8 h-8 rounded-xs text-on-surface-subtle hover:text-danger-500 hover:bg-danger-100 transition-colors"
            >
              <Trash2 className="w-4 h-4" aria-hidden="true" />
            </button>
          </div>
        ),
        enableSorting: false,
      },
    ],
    [onDelete, org, repo, fullName],
  )

  const table = useReactTable({
    data: tags,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  })

  return (
    <div className="rounded-lg border border-border bg-surface overflow-hidden">
      <table className="w-full text-left">
        <thead className="border-b border-border bg-surface-muted/40">
          {table.getHeaderGroups().map((headerGroup) => (
            <tr key={headerGroup.id}>
              {headerGroup.headers.map((header) => {
                const canSort = header.column.getCanSort()
                const sortDir = header.column.getIsSorted()
                return (
                  <th
                    key={header.id}
                    scope="col"
                    className="px-lg py-sm text-label-sm uppercase tracking-wider font-semibold text-on-surface-subtle"
                  >
                    {canSort ? (
                      <button
                        type="button"
                        onClick={header.column.getToggleSortingHandler()}
                        className={cn(
                          'inline-flex items-center gap-xs',
                          'hover:text-on-surface transition-colors',
                          sortDir && 'text-on-surface',
                        )}
                      >
                        {flexRender(
                          header.column.columnDef.header,
                          header.getContext(),
                        )}
                        <SortIcon dir={sortDir || false} />
                      </button>
                    ) : (
                      flexRender(
                        header.column.columnDef.header,
                        header.getContext(),
                      )
                    )}
                  </th>
                )
              })}
            </tr>
          ))}
        </thead>
        <tbody className="divide-y divide-border">
          {table.getRowModel().rows.map((row) => (
            <tr
              key={row.id}
              className="hover:bg-surface-muted/40 transition-colors"
            >
              {row.getVisibleCells().map((cell) => (
                <td key={cell.id} className="px-lg py-md align-middle">
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Truncated digest + copy button. The full digest is 71 chars (sha256:
    + 64 hex), which is too wide for a table column on most viewports. */
function DigestCell({ digest }: { digest: string }) {
  const short = digest.startsWith('sha256:')
    ? `${digest.slice(0, 7 + 12)}…`
    : digest.length > 18
    ? `${digest.slice(0, 18)}…`
    : digest

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(digest)
      toast.success('Digest copied')
    } catch {
      toast.error("Couldn't copy to clipboard")
    }
  }

  return (
    <button
      type="button"
      onClick={onCopy}
      aria-label={`Copy digest ${digest}`}
      className="inline-flex items-center gap-xs font-mono text-code-sm text-on-surface-muted hover:text-on-surface transition-colors"
    >
      {short}
      <Copy className="w-3 h-3 text-on-surface-subtle" aria-hidden="true" />
    </button>
  )
}

/**
 * Empty state for a repository with no tags yet. Two columns on lg+:
 * pitch on the left, copy-pasteable push snippet on the right — same
 * pattern as the workspace-level empty state on /repositories. The
 * earlier version put `max-w-md mx-auto` on a `<p>` inside a
 * `text-center` parent, which on intermediate widths collapsed to
 * its `<code>` child's intrinsic width and wrapped one-word-per-line.
 * This rewrite uses an explicit grid + `w-full max-w-prose` so the
 * paragraph can't collapse.
 */
function EmptyTagsPanel({ fullName }: { fullName: string }) {
  const command = `docker tag my-app:latest ${DEV_REGISTRY_HOST}/${fullName}:v1\ndocker push ${DEV_REGISTRY_HOST}/${fullName}:v1`
  const [copied, setCopied] = useState(false)
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      toast.error("Couldn't copy to clipboard")
    }
  }

  return (
    <section
      aria-labelledby="empty-tags-heading"
      className="relative overflow-hidden rounded-lg border border-border bg-surface"
      style={{
        backgroundImage:
          'linear-gradient(110deg, oklch(0.97 0.04 60) 0%, oklch(0.98 0.025 350) 55%, oklch(1 0 0) 100%)',
      }}
    >
      <div className="grid grid-cols-1 lg:grid-cols-5 gap-lg p-2xl">
        <div className="lg:col-span-2 flex flex-col justify-center">
          <span
            aria-hidden="true"
            className="inline-flex items-center justify-center w-12 h-12 rounded-md bg-primary-soft text-primary"
          >
            <TagIcon className="w-6 h-6" />
          </span>
          <h2
            id="empty-tags-heading"
            className="mt-md text-heading-sm font-semibold text-on-surface"
          >
            No tags yet
          </h2>
          <p className="mt-xs w-full max-w-prose text-body-sm text-on-surface-muted">
            Push an image to{' '}
            <code className="font-mono text-code-sm bg-surface-muted px-xs rounded-xs whitespace-nowrap">
              {fullName}
            </code>{' '}
            from your CLI or CI to populate this list.
          </p>
        </div>

        <div className="lg:col-span-3">
          <div className="rounded-md overflow-hidden border border-neutral-800">
            <header className="flex items-center justify-between px-md py-sm bg-neutral-900 border-b border-neutral-800">
              <div className="flex items-center gap-xs text-label-sm font-mono text-on-surface-inverse/70">
                <span className="w-2.5 h-2.5 rounded-full bg-danger-500/80" aria-hidden="true" />
                <span className="w-2.5 h-2.5 rounded-full bg-warning-500/80" aria-hidden="true" />
                <span className="w-2.5 h-2.5 rounded-full bg-success-500/80" aria-hidden="true" />
                <span className="ml-sm">terminal</span>
              </div>
              <button
                type="button"
                onClick={onCopy}
                aria-label={copied ? 'Copied' : 'Copy push commands'}
                className={cn(
                  'inline-flex items-center gap-xs h-7 px-sm rounded-xs',
                  'text-label-sm font-medium transition-colors',
                  copied
                    ? 'text-success-500 bg-success-500/15'
                    : 'text-on-surface-inverse/70 hover:text-on-surface-inverse hover:bg-white/10',
                )}
              >
                {copied ? (
                  <Check className="w-3.5 h-3.5" aria-hidden="true" />
                ) : (
                  <Copy className="w-3.5 h-3.5" aria-hidden="true" />
                )}
                {copied ? 'Copied' : 'Copy'}
              </button>
            </header>
            <pre className="overflow-x-auto bg-neutral-950 text-on-surface-inverse px-lg py-md text-code-sm font-mono leading-relaxed whitespace-pre">
              <code>{command}</code>
            </pre>
          </div>
        </div>
      </div>
    </section>
  )
}

/**
 * Load-error panel — shown when the tags query fails. Proper card with
 * icon + heading + body instead of a thin red banner. The retry note
 * is honest: TanStack Query refetches automatically; we just tell the
 * user that's happening so they don't reload the page themselves.
 */
function LoadErrorPanel() {
  return (
    <div className="rounded-lg border border-danger-500/30 bg-danger-100 p-lg">
      <div className="flex items-start gap-md">
        <span
          aria-hidden="true"
          className="inline-flex items-center justify-center w-10 h-10 rounded-sm bg-danger-500/10 text-danger-500 shrink-0"
        >
          <TriangleAlert className="w-5 h-5" />
        </span>
        <div className="flex-1 min-w-0">
          <h3 className="text-body-md font-semibold text-danger-500">
            Couldn't load tags
          </h3>
          <p className="mt-xs text-body-sm text-danger-500/80">
            The list will retry automatically. If this persists, check that{' '}
            <code className="font-mono text-code-sm">registry-management</code>{' '}
            is healthy and reachable from the browser.
          </p>
        </div>
      </div>
    </div>
  )
}

/**
 * No-match panel — shown when the tag filter matches zero rows.
 * Mirrors the empty-tags pattern (icon chip + heading + body) so the
 * page stays visually consistent across states, with a Clear-search
 * action so the user has an obvious one-click way back to the full list.
 */
function NoMatchPanel({
  query,
  onClear,
}: {
  query: string
  onClear: () => void
}) {
  return (
    <div className="rounded-lg border border-border bg-surface p-2xl flex flex-col items-center text-center gap-md">
      <span
        aria-hidden="true"
        className="inline-flex items-center justify-center w-12 h-12 rounded-md bg-neutral-100 text-on-surface-muted"
      >
        <Search className="w-6 h-6" />
      </span>
      <div className="w-full max-w-prose">
        <h3 className="text-heading-sm font-semibold text-on-surface">
          No matches
        </h3>
        <p className="mt-xs text-body-sm text-on-surface-muted">
          No tags or digests match{' '}
          <code className="font-mono text-code-sm bg-surface-muted px-xs rounded-xs">
            {query}
          </code>
          .
        </p>
      </div>
      <Button variant="secondary" size="sm" onClick={onClear}>
        Clear search
      </Button>
    </div>
  )
}

/** Pulsing skeleton placeholder for the tags table. */
function TagsTableSkeleton() {
  return (
    <div className="rounded-lg border border-border bg-surface divide-y divide-border">
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="flex items-center gap-md p-lg">
          <span className="flex-1 h-4 max-w-[160px] rounded-xs bg-surface-muted animate-pulse" />
          <span className="flex-1 h-4 max-w-[240px] rounded-xs bg-surface-muted animate-pulse" />
          <span className="w-24 h-4 rounded-xs bg-surface-muted animate-pulse" />
          <span className="w-8 h-8 rounded-xs bg-surface-muted animate-pulse" />
        </div>
      ))}
    </div>
  )
}

/** Public / Private visibility pill — same component as the list table. */
function VisibilityPill({ isPublic }: { isPublic: boolean }) {
  if (isPublic) {
    return (
      <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded-full border border-success-500/30 bg-success-100 text-success-500 text-label-sm font-medium">
        <Globe className="w-3 h-3" aria-hidden="true" />
        Public
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded-full border border-border bg-neutral-100 text-on-surface-muted text-label-sm font-medium">
      <Lock className="w-3 h-3" aria-hidden="true" />
      Private
    </span>
  )
}

/**
 * Pull-command card at the top of the detail page.
 *
 * Default tag picks `:latest` if it exists; otherwise the most recently
 * pushed tag. The pull host is hardcoded to the dev registry until
 * FE-API-007 (per-tenant hostname via API) lands — when it does, swap
 * `DEV_REGISTRY_HOST` here for the real value.
 */
const DEV_REGISTRY_HOST = 'registry.localhost:5000'

function PullCommandCard({
  fullName,
  tags,
}: {
  fullName: string
  tags: TagResponse[]
}) {
  const [copied, setCopied] = useState(false)
  const defaultTag = useMemo(() => {
    const latest = tags.find((t) => t.name === 'latest')
    if (latest) return 'latest'
    return tags
      .slice()
      .sort(
        (a, b) =>
          new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
      )[0].name
  }, [tags])

  const command = `docker pull ${DEV_REGISTRY_HOST}/${fullName}:${defaultTag}`

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      toast.error("Couldn't copy to clipboard")
    }
  }

  return (
    <section
      aria-label="Pull command"
      className="rounded-lg border border-border bg-surface"
    >
      <div className="grid grid-cols-1 lg:grid-cols-5 gap-lg p-lg">
        <div className="lg:col-span-2 flex flex-col justify-center">
          <span
            aria-hidden="true"
            className="inline-flex items-center justify-center w-9 h-9 rounded-sm bg-primary-soft text-primary"
          >
            <Terminal className="w-4 h-4" />
          </span>
          <h2 className="mt-sm text-heading-sm font-semibold text-on-surface">
            Pull this image
          </h2>
          <p className="mt-xs text-body-sm text-on-surface-muted">
            Default tag is{' '}
            <code className="font-mono text-code-sm">{defaultTag}</code>. Use
            the per-row copy button below for any other tag.
          </p>
        </div>
        <div className="lg:col-span-3">
          <div className="rounded-md overflow-hidden border border-neutral-800">
            <header className="flex items-center justify-between px-md py-sm bg-neutral-900 border-b border-neutral-800">
              <div className="flex items-center gap-xs text-label-sm font-mono text-on-surface-inverse/70">
                <span className="w-2.5 h-2.5 rounded-full bg-danger-500/80" aria-hidden="true" />
                <span className="w-2.5 h-2.5 rounded-full bg-warning-500/80" aria-hidden="true" />
                <span className="w-2.5 h-2.5 rounded-full bg-success-500/80" aria-hidden="true" />
                <span className="ml-sm">terminal</span>
              </div>
              <button
                type="button"
                onClick={onCopy}
                aria-label={copied ? 'Copied' : 'Copy pull command'}
                className={cn(
                  'inline-flex items-center gap-xs h-7 px-sm rounded-xs',
                  'text-label-sm font-medium transition-colors',
                  copied
                    ? 'text-success-500 bg-success-500/15'
                    : 'text-on-surface-inverse/70 hover:text-on-surface-inverse hover:bg-white/10',
                )}
              >
                {copied ? (
                  <Check className="w-3.5 h-3.5" aria-hidden="true" />
                ) : (
                  <Copy className="w-3.5 h-3.5" aria-hidden="true" />
                )}
                {copied ? 'Copied' : 'Copy'}
              </button>
            </header>
            <pre className="overflow-x-auto bg-neutral-950 text-on-surface-inverse px-lg py-md text-code-sm font-mono leading-relaxed">
              <code>{command}</code>
            </pre>
          </div>
        </div>
      </div>
    </section>
  )
}

/** Tiny icon button — copies `docker pull <full>:<tag>` for one tag. */
function PullCopyButton({
  fullName,
  tag,
}: {
  fullName: string
  tag: string
}) {
  const onClick = async () => {
    const command = `docker pull ${DEV_REGISTRY_HOST}/${fullName}:${tag}`
    try {
      await navigator.clipboard.writeText(command)
      toast.success(`Copied "${tag}" pull command`)
    } catch {
      toast.error("Couldn't copy to clipboard")
    }
  }
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={`Copy pull command for ${tag}`}
      className="inline-flex items-center justify-center w-8 h-8 rounded-xs text-on-surface-subtle hover:text-primary hover:bg-primary-soft transition-colors"
    >
      <Copy className="w-4 h-4" aria-hidden="true" />
    </button>
  )
}

/**
 * Per-tag scan badge. Three states:
 *   * loading — small pulsing placeholder
 *   * not scanned — neutral grey pill
 *   * scanned    — coloured pill with the highest-severity finding count
 *                  (red > amber > blue > green order). "Clean" if 0 across
 *                  every severity bucket.
 */
function ScanCell({
  org,
  repo,
  tag,
}: {
  org: string
  repo: string
  tag: string
}) {
  const { data, isLoading } = useScan(org, repo, tag)

  if (isLoading) {
    return (
      <span className="inline-block w-20 h-5 rounded-full bg-surface-muted animate-pulse" />
    )
  }

  if (!data || data.scanned === false) {
    return (
      <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded-full border border-border bg-neutral-100 text-on-surface-muted text-label-sm font-medium">
        <ShieldQuestion className="w-3 h-3" aria-hidden="true" />
        Not scanned
      </span>
    )
  }

  const counts = data.scan.severity_counts || {}
  const crit = counts.CRITICAL ?? 0
  const high = counts.HIGH ?? 0
  const med = counts.MEDIUM ?? 0
  const low = counts.LOW ?? 0
  const total = crit + high + med + low

  if (data.scan.status !== 'complete') {
    return (
      <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded-full border border-info-500/30 bg-info-100 text-info-500 text-label-sm font-medium capitalize">
        <ShieldQuestion className="w-3 h-3" aria-hidden="true" />
        {data.scan.status}
      </span>
    )
  }

  if (total === 0) {
    return (
      <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded-full border border-success-500/30 bg-success-100 text-success-500 text-label-sm font-medium">
        <ShieldCheck className="w-3 h-3" aria-hidden="true" />
        Clean
      </span>
    )
  }

  if (crit > 0) {
    return (
      <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded-full border border-danger-500/30 bg-danger-100 text-danger-500 text-label-sm font-medium">
        <ShieldAlert className="w-3 h-3" aria-hidden="true" />
        {crit} critical
      </span>
    )
  }
  if (high > 0) {
    return (
      <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded-full border border-warning-500/30 bg-warning-100 text-warning-500 text-label-sm font-medium">
        <ShieldAlert className="w-3 h-3" aria-hidden="true" />
        {high} high
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-xs px-sm py-0.5 rounded-full border border-info-500/30 bg-info-100 text-info-500 text-label-sm font-medium">
      <ShieldAlert className="w-3 h-3" aria-hidden="true" />
      {med + low} {med + low === 1 ? 'finding' : 'findings'}
    </span>
  )
}

function SortIcon({ dir }: { dir: 'asc' | 'desc' | false }) {
  if (dir === 'asc') return <ArrowUp className="w-3 h-3" aria-hidden="true" />
  if (dir === 'desc') return <ArrowDown className="w-3 h-3" aria-hidden="true" />
  return <ArrowUpDown className="w-3 h-3 opacity-50" aria-hidden="true" />
}

/**
 * Delete-tag confirmation dialog. We re-implement here rather than
 * promoting `DeleteRepositoryDialog` to a generic — the copy is
 * different (per-tag vs per-repo) and confirmation text is just the
 * tag name, not the full org/repo path.
 */
function DeleteTagDialog({
  org,
  repo,
  target,
  onOpenChange,
}: {
  org: string
  repo: string
  target: string | null
  onOpenChange: (open: boolean) => void
}) {
  const deleteMutation = useDeleteTag(org, repo)
  const [typed, setTyped] = useState('')
  const matches = !!target && typed === target

  useEffect(() => {
    if (target) setTyped('')
  }, [target])

  if (!target) return null

  const onConfirm = () => {
    deleteMutation.mutate(target, {
      onSuccess: () => {
        toast.success(`Tag ${target} deleted`)
        onOpenChange(false)
      },
      onError: (err) => {
        if (err instanceof AxiosError) {
          if (err.response?.status === 403) {
            toast.error("You don't have permission to delete this tag")
            return
          }
          if (err.response?.status === 404) {
            toast.error('Tag not found')
            return
          }
        }
        toast.error("Couldn't delete tag")
      },
    })
  }

  return (
    <Dialog.Root open={!!target} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-surface-overlay backdrop-blur-sm" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-[440px] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-surface shadow-xl focus:outline-none">
          <div className="flex items-start justify-between p-lg border-b border-border">
            <div className="flex items-start gap-md">
              <span
                aria-hidden="true"
                className="inline-flex items-center justify-center w-9 h-9 rounded-sm bg-danger-100 text-danger-500"
              >
                <TriangleAlert className="w-[18px] h-[18px]" />
              </span>
              <div>
                <Dialog.Title className="text-heading-sm font-semibold text-on-surface">
                  Delete tag
                </Dialog.Title>
                <Dialog.Description className="mt-xs text-body-sm text-on-surface-muted">
                  The manifest is removed from this tag, but blobs only GC
                  if no other tag references them.
                </Dialog.Description>
              </div>
            </div>
            <Dialog.Close
              aria-label="Close"
              className="inline-flex items-center justify-center w-8 h-8 rounded-xs text-on-surface-muted hover:text-on-surface hover:bg-surface-muted transition-colors"
            >
              <X className="w-4 h-4" aria-hidden="true" />
            </Dialog.Close>
          </div>

          <div className="p-lg space-y-md">
            <Label htmlFor="delete-tag-confirm">
              Type{' '}
              <code className="font-mono text-code-sm bg-surface-muted px-xs rounded-xs">
                {target}
              </code>{' '}
              to confirm
            </Label>
            <Input
              id="delete-tag-confirm"
              type="text"
              autoComplete="off"
              spellCheck={false}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="font-mono text-code-sm"
            />
            <FieldHint>This confirmation is case-sensitive.</FieldHint>
          </div>

          <div className="flex items-center justify-end gap-sm p-lg pt-0">
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={!matches}
              loading={deleteMutation.isPending}
              onClick={onConfirm}
            >
              Delete tag
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
