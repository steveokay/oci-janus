/**
 * RepositoriesTable — TanStack Table for the /repositories list.
 *
 * Columns:
 *   * Name        — initial chip + full "org/repo" form, clickable
 *                   (Sprint 1d wires the detail route)
 *   * Visibility  — Public (success) / Private (neutral) pill
 *   * Storage     — used (formatted) with quota subtitle
 *   * Created     — relative time
 *   * Actions     — trash button per row → opens delete dialog
 *
 * Sort is client-side via TanStack Table. The visibility filter is
 * server-applied (passed up via `useRepositories`'s query param); name
 * search is client-side because we have at most 100 rows per page.
 *
 * Sprint 1d will add row navigation to the detail page; for now clicking
 * a name shows a "coming soon" toast.
 */
import { useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from '@tanstack/react-table'
import { ArrowDown, ArrowUp, ArrowUpDown, Globe, Lock, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils/cn'
import { formatBytes } from '@/lib/format/bytes'
import { relativeTime } from '@/lib/format/time'
import type { RepoResponse } from '@/lib/api/hooks/useRepositories'

export interface RepositoriesTableProps {
  repos: RepoResponse[]
  onDelete: (fullName: string) => void
}

export function RepositoriesTable({ repos, onDelete }: RepositoriesTableProps) {
  const navigate = useNavigate()
  const [sorting, setSorting] = useState<SortingState>([
    { id: 'created_at', desc: true },
  ])

  const columns = useMemo<ColumnDef<RepoResponse>[]>(
    () => [
      {
        accessorKey: 'name',
        header: 'Name',
        cell: (info) => {
          const name = info.getValue<string>()
          // The management `RepoResponse.name` is just the leaf today —
          // org lives in `org_id` (UUID) and the org name isn't on the
          // response (tracked as FE-API-010 in status.md). Until that
          // ships, fall back to the dev tenant's seeded `dev` org so
          // the click takes the user somewhere useful instead of
          // 404'ing. Full org/repo names (when present) still split
          // on the first slash.
          const slash = name.indexOf('/')
          const org = slash >= 0 ? name.slice(0, slash) : 'dev'
          const repo = slash >= 0 ? name.slice(slash + 1) : name
          return (
            <button
              type="button"
              onClick={() => {
                navigate({
                  to: '/repositories/$org/$repo',
                  params: { org, repo },
                })
              }}
              className="flex items-center gap-md text-left group"
            >
              <span
                aria-hidden="true"
                className="inline-flex items-center justify-center w-8 h-8 rounded-sm bg-neutral-100 text-on-surface font-mono text-label-md font-semibold shrink-0"
              >
                {name.charAt(0).toUpperCase()}
              </span>
              <span className="text-body-sm font-medium text-on-surface group-hover:text-primary transition-colors truncate">
                {name}
              </span>
            </button>
          )
        },
        sortingFn: 'alphanumeric',
      },
      {
        accessorKey: 'is_public',
        id: 'visibility',
        header: 'Visibility',
        cell: (info) => <VisibilityPill isPublic={info.getValue<boolean>()} />,
        sortingFn: 'basic',
      },
      {
        accessorKey: 'storage_used_bytes',
        id: 'storage',
        header: 'Storage',
        cell: (info) => {
          const used = info.getValue<number>()
          const quota = info.row.original.storage_quota_bytes
          const usedF = formatBytes(used)
          const quotaF = formatBytes(quota)
          return (
            <div>
              <div className="text-body-sm text-on-surface tabular-nums">
                {usedF.value} {usedF.unit}
              </div>
              {quota > 0 && (
                <div className="text-label-sm text-on-surface-subtle tabular-nums">
                  of {quotaF.value} {quotaF.unit}
                </div>
              )}
            </div>
          )
        },
        sortingFn: 'basic',
      },
      {
        accessorKey: 'created_at',
        header: 'Created',
        cell: (info) => (
          <span className="text-body-sm text-on-surface-muted">
            {relativeTime(info.getValue<string>())}
          </span>
        ),
        sortingFn: (a, b) =>
          // Stable date compare — we sort by the underlying ISO string's
          // numeric Date value, not the rendered relative string.
          new Date(a.original.created_at).getTime() -
          new Date(b.original.created_at).getTime(),
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">Actions</span>,
        cell: ({ row }) => (
          <div className="flex items-center justify-end">
            <button
              type="button"
              aria-label={`Delete ${row.original.name}`}
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
    [onDelete],
  )

  const table = useReactTable({
    data: repos,
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

/** Visibility pill — green for Public, neutral for Private. */
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

/** Sort indicator — neutral both-arrows when off, single arrow when sorting. */
function SortIcon({ dir }: { dir: 'asc' | 'desc' | false }) {
  if (dir === 'asc') return <ArrowUp className="w-3 h-3" aria-hidden="true" />
  if (dir === 'desc') return <ArrowDown className="w-3 h-3" aria-hidden="true" />
  return <ArrowUpDown className="w-3 h-3 opacity-50" aria-hidden="true" />
}
