import * as React from "react";
import { Tag as TagIcon, Trash2 } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
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
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import { BulkDeleteTagsDialog } from "@/components/repositories/bulk-delete-tags-dialog";
import { formatBytes, formatRelativeDate } from "@/lib/format";
import { useTags } from "@/lib/api/tags";
import { BULK_DELETE_MAX } from "@/lib/api/tags";
import { cn } from "@/lib/utils";

interface TagsPanelProps {
  org: string;
  repo: string;
}

// TagsPanel — lives inside the repo-detail tab strip.
//
// FE-API-036 adds checkbox selection + a "Delete selected" toolbar above
// the table. Selection state is local to the panel — switching tabs
// re-mounts and clears it, which matches the expected "destructive
// actions don't survive context switches" gesture.
export function TagsPanel({ org, repo }: TagsPanelProps): React.ReactElement {
  const navigate = useNavigate();
  const { data, isLoading, isError, refetch } = useTags(org, repo);
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [confirmOpen, setConfirmOpen] = React.useState(false);

  const tags = React.useMemo(() => data ?? [], [data]);
  const visibleTagNames = React.useMemo(() => tags.map((t) => t.name), [tags]);
  // Cap selection at the server's hard limit so the toolbar can show a
  // friendlier "max 100" hint instead of letting the BFF 400 the request.
  const selectionCount = selected.size;
  const overCap = selectionCount > BULK_DELETE_MAX;

  function toggleOne(name: string): void {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }
  function toggleAll(): void {
    setSelected((prev) => {
      // If every visible row is selected, clear; otherwise select every
      // visible row. The set may carry tags no longer visible after a
      // refresh — those get dropped on clear and re-added if visible.
      const allSelected = visibleTagNames.every((n) => prev.has(n));
      if (allSelected) return new Set();
      return new Set(visibleTagNames);
    });
  }

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load tags"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  if (!isLoading && tags.length === 0) {
    return (
      <EmptyState
        icon={<TagIcon className="size-5" />}
        title="No tags yet"
        description="Push your first image with the pull command above (swap `pull` for `push`). The tag will appear here within a few seconds."
      />
    );
  }

  const allVisibleSelected =
    visibleTagNames.length > 0 &&
    visibleTagNames.every((n) => selected.has(n));
  const someVisibleSelected =
    !allVisibleSelected && visibleTagNames.some((n) => selected.has(n));

  return (
    <div className="space-y-3">
      <SelectionToolbar
        count={selectionCount}
        overCap={overCap}
        onClear={() => setSelected(new Set())}
        onDelete={() => setConfirmOpen(true)}
      />

      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-[40px] !pr-0">
                <SelectionCheckbox
                  checked={allVisibleSelected}
                  indeterminate={someVisibleSelected}
                  onChange={toggleAll}
                  ariaLabel={
                    allVisibleSelected
                      ? "Deselect all visible tags"
                      : "Select all visible tags"
                  }
                />
              </TableHead>
              <TableHead className="w-[28%]">Tag</TableHead>
              <TableHead>Digest</TableHead>
              <TableHead>Size</TableHead>
              <TableHead className="hidden md:table-cell">Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading ? (
              <SkeletonRows />
            ) : (
              tags.map((t) => {
                const target = {
                  to: "/repositories/$org/$repo/tags/$tag" as const,
                  params: { org, repo, tag: t.name },
                };
                const open = () => void navigate(target);
                const hrefTo = `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(t.name)}`;
                const isSelected = selected.has(t.name);
                return (
                  <TableRow
                    key={`${t.name}-${t.manifest_digest}`}
                    interactive
                    role="link"
                    tabIndex={0}
                    data-state={isSelected ? "selected" : undefined}
                    className={
                      isSelected ? "bg-[var(--color-accent-subtle)]/40" : ""
                    }
                    onClick={open}
                    onMouseDown={(e) => {
                      if (e.button === 0) open();
                    }}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        open();
                      }
                    }}
                  >
                    <TableCell className="w-[40px] !pr-0">
                      {/* stopPropagation on both click + mousedown so the
                          row-level navigate handler doesn't trigger. */}
                      <span
                        onClick={(e) => e.stopPropagation()}
                        onMouseDown={(e) => e.stopPropagation()}
                      >
                        <SelectionCheckbox
                          checked={isSelected}
                          onChange={() => toggleOne(t.name)}
                          ariaLabel={`Select tag ${t.name}`}
                        />
                      </span>
                    </TableCell>
                    <TableCell className="p-0">
                      <a
                        href={hrefTo}
                        onClick={(e) => {
                          e.preventDefault();
                          open();
                        }}
                        className="block px-4 py-3 text-inherit no-underline"
                      >
                        <div className="inline-flex items-center gap-1.5">
                          <Badge tone="accent">
                            <TagIcon className="size-3" /> {t.name}
                          </Badge>
                          {/* REM-013 gap 1 — pending-delete pill. Renders */}
                          {/* only when the manifest is in the retention   */}
                          {/* grace window; surfaces the ETA so an operator */}
                          {/* can act before hard-delete fires.            */}
                          <PendingDeletePill iso={t.retention_pending_delete_at} />
                        </div>
                      </a>
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <code
                          className="pointer-events-none truncate font-mono text-xs text-[var(--color-fg-muted)]"
                          title={t.manifest_digest}
                        >
                          {t.manifest_digest.slice(0, 19)}…
                        </code>
                        <span
                          onClick={(e) => e.stopPropagation()}
                          onMouseDown={(e) => e.stopPropagation()}
                        >
                          <CopyButton value={t.manifest_digest} iconOnly />
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="pointer-events-none font-mono text-xs">
                      {t.size_bytes > 0 ? (
                        formatBytes(t.size_bytes)
                      ) : (
                        <span className="text-[var(--color-fg-subtle)]">—</span>
                      )}
                    </TableCell>
                    <TableCell className="pointer-events-none hidden text-xs text-[var(--color-fg-muted)] md:table-cell">
                      {formatRelativeDate(t.updated_at)}
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>

      <BulkDeleteTagsDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        org={org}
        repo={repo}
        tagNames={Array.from(selected).slice(0, BULK_DELETE_MAX)}
        onCompleted={() => setSelected(new Set())}
      />
    </div>
  );
}

interface SelectionToolbarProps {
  count: number;
  overCap: boolean;
  onClear: () => void;
  onDelete: () => void;
}

function SelectionToolbar({
  count,
  overCap,
  onClear,
  onDelete,
}: SelectionToolbarProps): React.ReactElement {
  // Always render the toolbar in the same slot — but make it visually
  // recede when no selection is active so it doesn't shift the table down
  // on first interaction.
  const active = count > 0;
  return (
    <div
      role="toolbar"
      aria-label="Tag selection actions"
      className={cn(
        "flex h-9 items-center justify-between rounded-md border px-3 transition-colors",
        active
          ? "border-[var(--color-accent-border)] bg-[var(--color-accent-subtle)]/60"
          : "border-[var(--color-border)] bg-[var(--color-surface-sunken)]",
      )}
    >
      <div className="text-xs">
        {active ? (
          <span className="font-medium text-[var(--color-fg)]">
            {count.toLocaleString()} selected
            {overCap ? (
              <span className="ml-2 text-[var(--color-danger)]">
                · capped at {BULK_DELETE_MAX} per request
              </span>
            ) : null}
          </span>
        ) : (
          <span className="text-[var(--color-fg-subtle)]">
            Tick rows to bulk-delete tags (cap {BULK_DELETE_MAX} per request)
          </span>
        )}
      </div>
      {active ? (
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" onClick={onClear}>
            Clear
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={onDelete}
            className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
          >
            <Trash2 className="size-3.5" />
            Delete selected
          </Button>
        </div>
      ) : null}
    </div>
  );
}

interface SelectionCheckboxProps {
  checked: boolean;
  indeterminate?: boolean;
  onChange: () => void;
  ariaLabel: string;
}

// Native input[type=checkbox] styled to match Beacon. No Radix dependency
// because we only need a single visual variant — the styled accent ring
// is enough.
function SelectionCheckbox({
  checked,
  indeterminate,
  onChange,
  ariaLabel,
}: SelectionCheckboxProps): React.ReactElement {
  const ref = React.useRef<HTMLInputElement>(null);
  React.useEffect(() => {
    if (ref.current) ref.current.indeterminate = Boolean(indeterminate);
  }, [indeterminate]);
  return (
    <input
      ref={ref}
      type="checkbox"
      checked={checked}
      onChange={onChange}
      aria-label={ariaLabel}
      className="size-4 cursor-pointer rounded border-[var(--color-border-strong)] accent-[var(--color-accent)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40"
    />
  );
}

// PendingDeletePill — soft-delete countdown surfaced on each tag row when
// the manifest is inside the retention grace window. Renders nothing for
// the common path (no stamp on the wire).
//
// Grace window is 7 days by default (services/gc RETENTION_GRACE_DAYS). The
// pill turns danger-toned in the final 24h so an operator scanning the
// table sees the imminent rows first. The relative date already handles
// "in 4 days" vs "in 6 hours" so the pill stays compact.
function PendingDeletePill({
  iso,
}: {
  iso: string | undefined;
}): React.ReactElement | null {
  if (!iso) return null;
  // Stamp + 7d grace window = ETA. We compute client-side because the BFF
  // doesn't return the platform grace setting; if it ever does the value
  // should flow in from useWorkspace() instead of being hardcoded.
  const GRACE_DAYS = 7;
  const eta = new Date(iso).getTime() + GRACE_DAYS * 24 * 60 * 60 * 1000;
  const now = Date.now();
  const msLeft = eta - now;
  // Already past grace — should be hard-deleted shortly. Still surface it
  // distinctly so the operator knows what's going on if the grace ticker
  // is running late.
  const overdue = msLeft <= 0;
  const urgent = !overdue && msLeft <= 24 * 60 * 60 * 1000;
  return (
    <span
      title={
        overdue
          ? "Past grace window — will be hard-deleted on the next retention_grace tick."
          : `Retention will hard-delete this manifest ${formatRelativeDate(new Date(eta).toISOString())}. Marked at ${iso}.`
      }
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium border",
        overdue
          ? "border-[var(--color-danger)]/40 bg-[var(--color-danger)]/10 text-[var(--color-danger)]"
          : urgent
            ? "border-[var(--color-warning)]/40 bg-[var(--color-warning)]/10 text-[var(--color-warning)]"
            : "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
      )}
    >
      <Trash2 className="size-2.5" aria-hidden />
      {overdue ? "past grace" : `del ${formatRelativeDate(new Date(eta).toISOString())}`}
    </span>
  );
}

function SkeletonRows(): React.ReactElement {
  return (
    <>
      {Array.from({ length: 4 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell className="w-[40px]">
            <Skeleton className="size-4 rounded" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-5 w-20 rounded-full" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-3 w-44" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-3 w-16" />
          </TableCell>
          <TableCell className="hidden md:table-cell">
            <Skeleton className="h-3 w-20" />
          </TableCell>
        </TableRow>
      ))}
    </>
  );
}
