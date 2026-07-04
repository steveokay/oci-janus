import * as React from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { Globe, Lock, ArrowUp, ArrowDown, ChevronsUpDown } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { formatBytes, formatRelativeDate, formatAbsoluteDate } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { Repository } from "@/lib/api/types";

// Client-side sort support for the Storage + Created columns.
//
// TRADEOFF (commented at the call site too): the repositories list is an
// INFINITE query, so this sort only reorders the pages already loaded — it is
// NOT a global sort of the whole catalog. When more pages remain the table
// surfaces a one-line note telling the operator to load more for a complete
// ordering. True server-side sort is tracked as FE-API-future (the BFF list
// endpoint has no sort param yet).
export type RepoSortKey = "storage" | "created";
export type SortDir = "asc" | "desc";

// sortRepositories returns a NEW array sorted by the given key/dir. Pure +
// stable-ish (relies on Array.prototype.sort) so it's unit-testable without
// rendering. Storage sorts on bytes used; Created on the parsed timestamp.
export function sortRepositories(
  repos: Repository[],
  key: RepoSortKey | null,
  dir: SortDir,
): Repository[] {
  if (key === null) return repos;
  const sign = dir === "asc" ? 1 : -1;
  // Copy first — never mutate the caller's array (it comes from a memo).
  return [...repos].sort((a, b) => {
    const av =
      key === "storage" ? a.storage_used_bytes : Date.parse(a.created_at);
    const bv =
      key === "storage" ? b.storage_used_bytes : Date.parse(b.created_at);
    // NaN-safe: unparseable dates sort to the bottom regardless of direction.
    const aSafe = Number.isNaN(av) ? -Infinity : av;
    const bSafe = Number.isNaN(bv) ? -Infinity : bv;
    if (aSafe < bSafe) return -1 * sign;
    if (aSafe > bSafe) return 1 * sign;
    return 0;
  });
}

// F4 follow-up — when the table is rendered on /helm or /repositories
// (which pre-filter the listing by artifact_type), the row link carries
// the matching ?type=<artifact> so the Tags panel on the repo detail
// page opens with the correct chip pre-selected. Undefined means "no
// preference" — used by callers that aren't artifact-typed.
type RepoLinkArtifactType = "image" | "helm" | "signature" | "sbom" | "other";

interface Props {
  repositories: Repository[];
  loading?: boolean;
  // Sets ?type= on every row link so the repo detail page opens with
  // the matching artifact-type chip already engaged. /helm passes "helm";
  // /repositories passes "image". Other callers may omit it for the
  // legacy unfiltered behaviour.
  linkArtifactType?: RepoLinkArtifactType;
  // True when the infinite query still has unloaded pages. Used only to warn
  // that an active sort covers loaded rows only — the table doesn't fetch.
  hasNextPage?: boolean;
}

// Beacon — RepositoriesTable. The standard list view across all visibility
// filters. Click row → /repositories/:org/:repo (with optional ?type=).
export function RepositoriesTable({
  repositories,
  loading,
  linkArtifactType,
  hasNextPage,
}: Props): React.ReactElement {
  // Sort state is local to the table — the parent still owns fetching. null
  // key = server/insertion order (the default the list arrives in).
  const [sortKey, setSortKey] = React.useState<RepoSortKey | null>(null);
  const [sortDir, setSortDir] = React.useState<SortDir>("asc");

  // toggleSort: click an inactive header → activate ascending; click the
  // active header → flip direction. Simple asc/desc toggle per the brief.
  function toggleSort(key: RepoSortKey): void {
    if (sortKey === key) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    } else {
      setSortKey(key);
      setSortDir("asc");
    }
  }

  const sorted = React.useMemo(
    () => sortRepositories(repositories, sortKey, sortDir),
    [repositories, sortKey, sortDir],
  );

  return (
    <div className="space-y-2">
      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-[40%]">Repository</TableHead>
              <TableHead>Visibility</TableHead>
              <SortableHead
                label="Storage"
                sortKey="storage"
                activeKey={sortKey}
                dir={sortDir}
                onToggle={toggleSort}
              />
              <SortableHead
                label="Created"
                sortKey="created"
                activeKey={sortKey}
                dir={sortDir}
                onToggle={toggleSort}
                className="hidden md:table-cell"
              />
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading
              ? Array.from({ length: 6 }).map((_, i) => <SkeletonRow key={i} />)
              : sorted.map((r) => (
                  <Row
                    key={r.repo_id}
                    repo={r}
                    linkArtifactType={linkArtifactType}
                  />
                ))}
          </TableBody>
        </Table>
      </div>
      {/* Infinite-query caveat: sorting only reorders loaded pages. Warn when
          more remain so the operator knows the ordering isn't global yet. */}
      {sortKey !== null && hasNextPage ? (
        <p className="px-1 text-xs text-[var(--color-fg-subtle)]">
          Sorted across loaded repositories only — load more below to sort the
          full catalog.
        </p>
      ) : null}
    </div>
  );
}

// SortableHead — a clickable column header that carries aria-sort + a
// direction glyph. Inactive columns show a faint up/down chevron to advertise
// that they're sortable; the active column shows a solid arrow for its dir.
function SortableHead({
  label,
  sortKey,
  activeKey,
  dir,
  onToggle,
  className,
}: {
  label: string;
  sortKey: RepoSortKey;
  activeKey: RepoSortKey | null;
  dir: SortDir;
  onToggle: (key: RepoSortKey) => void;
  className?: string;
}): React.ReactElement {
  const active = activeKey === sortKey;
  // aria-sort exposes the current sort to assistive tech per WAI-ARIA grid
  // semantics; inactive sortable columns report "none".
  const ariaSort = active ? (dir === "asc" ? "ascending" : "descending") : "none";
  const Icon = active ? (dir === "asc" ? ArrowUp : ArrowDown) : ChevronsUpDown;
  return (
    <TableHead aria-sort={ariaSort} className={cn("p-0", className)}>
      <button
        type="button"
        onClick={() => onToggle(sortKey)}
        // Fill the cell so the whole header area is the hit target; keep the
        // uppercase tracking the plain TableHead applies (it lives on the th,
        // which we've zeroed padding on, so re-apply the inset here).
        className={cn(
          "flex h-10 w-full items-center gap-1 px-4 text-left transition-colors",
          "hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40",
          active ? "text-[var(--color-fg)]" : "text-[var(--color-fg-subtle)]",
        )}
      >
        {label}
        <Icon
          className={cn(
            "size-3",
            active
              ? "text-[var(--color-accent)]"
              : "text-[var(--color-fg-subtle)] opacity-60",
          )}
          aria-hidden
        />
      </button>
    </TableHead>
  );
}

function Row({
  repo,
  linkArtifactType,
}: {
  repo: Repository;
  linkArtifactType?: RepoLinkArtifactType;
}): React.ReactElement {
  const navigate = useNavigate();
  const pct =
    repo.storage_quota_bytes > 0
      ? Math.min(
          100,
          Math.round((repo.storage_used_bytes / repo.storage_quota_bytes) * 100),
        )
      : 0;
  // Whole-row click is convenience; the real focusable target is the Link in
  // the first cell so keyboard nav + middle-click + right-click all behave.
  // F4 follow-up — pass ?type= when the caller specified an artifact type
  // (e.g. /helm passes "helm") so the tag-panel chip arrives pre-selected.
  const linkSearch = linkArtifactType ? { type: linkArtifactType } : {};
  return (
    <TableRow
      interactive
      onClick={() =>
        void navigate({
          to: "/repositories/$org/$repo",
          params: { org: repo.org, repo: repo.name },
          search: linkSearch,
        })
      }
    >
      <TableCell>
        <Link
          to="/repositories/$org/$repo"
          params={{ org: repo.org, repo: repo.name }}
          search={linkSearch}
          onClick={(e) => e.stopPropagation()}
          className="flex items-center gap-3 text-[var(--color-fg)]"
        >
          <span
            className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] font-display text-sm font-semibold text-[var(--color-accent)]"
            aria-hidden
          >
            {repo.name[0]?.toUpperCase() ?? "·"}
          </span>
          <div className="min-w-0">
            <div className="truncate text-sm font-medium">
              {/* Older dev rows may have an empty `org` (FE-API-010 only
                  populates new pushes). Skip the slash entirely when blank
                  so we don't print a lonely "/alpine". */}
              {repo.org ? (
                <span className="text-[var(--color-fg-muted)]">{repo.org}/</span>
              ) : null}
              <span>{repo.name}</span>
            </div>
            <div className="font-mono text-[11px] text-[var(--color-fg-subtle)]">
              {repo.repo_id.slice(0, 8)}
            </div>
          </div>
        </Link>
      </TableCell>
      <TableCell>
        {repo.is_public ? (
          <Badge tone="accent">
            <Globe className="size-3" aria-hidden /> Public
          </Badge>
        ) : (
          <Badge tone="neutral">
            <Lock className="size-3" aria-hidden /> Private
          </Badge>
        )}
      </TableCell>
      <TableCell className="min-w-[180px]">
        <div className="flex flex-col gap-1.5">
          <span className="font-mono text-xs text-[var(--color-fg)]">
            {formatBytes(repo.storage_used_bytes)}
            <span className="text-[var(--color-fg-subtle)]">
              {" "}/ {formatBytes(repo.storage_quota_bytes)}
            </span>
          </span>
          <Progress value={pct} className="h-1" />
        </div>
      </TableCell>
      {/* Absolute timestamp on hover — matches the app-wide relative-date +
          title-tooltip convention. */}
      <TableCell
        className="hidden text-[var(--color-fg-muted)] md:table-cell"
        title={formatAbsoluteDate(repo.created_at)}
      >
        {formatRelativeDate(repo.created_at)}
      </TableCell>
    </TableRow>
  );
}

function SkeletonRow(): React.ReactElement {
  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-3">
          <Skeleton className="size-8 rounded-md" />
          <div className="space-y-1.5">
            <Skeleton className="h-3.5 w-44" />
            <Skeleton className="h-2.5 w-20" />
          </div>
        </div>
      </TableCell>
      <TableCell>
        <Skeleton className="h-5 w-16 rounded-full" />
      </TableCell>
      <TableCell>
        <div className="space-y-1.5">
          <Skeleton className="h-3 w-28" />
          <Skeleton className="h-1 w-32" />
        </div>
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <Skeleton className="h-3 w-20" />
      </TableCell>
    </TableRow>
  );
}
