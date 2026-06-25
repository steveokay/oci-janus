import * as React from "react";
import {
  Box,
  FileCheck2,
  FileSignature,
  Lock,
  Package,
  Pin,
  Play,
  Ship,
  Tag as TagIcon,
  Trash2,
} from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { AxiosError } from "axios";
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
import { useBulkScanRepo } from "@/lib/api/scan";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Search } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { ArtifactType } from "@/lib/api/types";
import { cn } from "@/lib/utils";

interface TagsPanelProps {
  org: string;
  repo: string;
  // F4 follow-up — initial chip selection lifted from the route's
  // ?type= search param so /helm → repo detail starts on the Helm chip
  // and /repositories → repo detail starts on Images. Undefined means
  // "no preference, show all".
  initialFilter?: ArtifactFilter;
}

// S-MAINT-1 Batch 5 (F4) — discriminator state for the filter chip row.
// "all" is the sentinel "no filter"; other values match Tag.artifact_type
// emitted by the BFF (deriveArtifactType source-of-truth). The "unknown"
// row catches legacy manifests whose config_media_type didn't backfill
// — surfaced as its own chip so an operator can find + repush them.
type ArtifactFilter = "all" | ArtifactType;

const ARTIFACT_FILTERS: ReadonlyArray<{
  value: ArtifactFilter;
  label: string;
  // null → no leading icon; all other rows surface their type glyph.
  Icon: React.ComponentType<{ className?: string }> | null;
}> = [
  { value: "all", label: "All", Icon: null },
  { value: "image", label: "Images", Icon: Box },
  { value: "helm", label: "Helm charts", Icon: Ship },
  { value: "signature", label: "Signatures", Icon: FileSignature },
  { value: "sbom", label: "SBOMs", Icon: FileCheck2 },
  { value: "other", label: "Other", Icon: Package },
];

// TagsPanel — lives inside the repo-detail tab strip.
//
// FE-API-036 adds checkbox selection + a "Delete selected" toolbar above
// the table. Selection state is local to the panel — switching tabs
// re-mounts and clears it, which matches the expected "destructive
// actions don't survive context switches" gesture.
export function TagsPanel({
  org,
  repo,
  initialFilter,
}: TagsPanelProps): React.ReactElement {
  const navigate = useNavigate();
  const { data, isLoading, isError, refetch } = useTags(org, repo);
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [confirmOpen, setConfirmOpen] = React.useState(false);
  // S-MAINT-1 Batch 5 (F4) — filter chip state seeded from the route's
  // ?type= search param when provided. Subsequent changes go through
  // setArtifactFilter, which also pushes the new value onto the URL via
  // navigate({ search }) so deep-linking + browser-back work naturally.
  const [artifactFilter, setArtifactFilter] = React.useState<ArtifactFilter>(
    initialFilter ?? "all",
  );
  // Free-text tag-name filter. Composes with artifactFilter via two
  // independent `useMemo` derivations below: artifact filter runs first
  // (it's the chip-driven facet that also seeds the URL); search runs
  // after on the artifact-filtered subset. Empty string means no filter.
  const [search, setSearch] = React.useState("");

  // syncFilter wraps setArtifactFilter so the URL stays authoritative —
  // a chip click updates both the in-memory state AND the address bar.
  // We use TanStack Router's replace=true so a chain of chip clicks
  // doesn't pollute browser history with one entry per toggle. The
  // empty-string sentinel (legacy / unknown manifests) is intentionally
  // left out of the URL — it's only reachable via in-memory state today
  // and the route's validateSearch doesn't accept it.
  const syncFilter = React.useCallback(
    (next: ArtifactFilter) => {
      setArtifactFilter(next);
      const isUrlValue =
        next === "all" ||
        next === "image" ||
        next === "helm" ||
        next === "signature" ||
        next === "sbom" ||
        next === "other";
      if (!isUrlValue) {
        return; // unknown-legacy filter — keep in-memory only
      }
      void navigate({
        to: "/repositories/$org/$repo",
        params: { org, repo },
        search: next === "all" ? {} : { type: next },
        replace: true,
      });
    },
    [navigate, org, repo],
  );

  const allTags = React.useMemo(() => data ?? [], [data]);
  // Apply the artifact-type chip before exposing the array to the rest
  // of the panel — selection counts, "Select all" toggling, and empty-
  // state messages all need to see only the visible rows.
  const artifactFilteredTags = React.useMemo(() => {
    if (artifactFilter === "all") return allTags;
    if (artifactFilter === "") {
      // "unknown" path — match rows with empty artifact_type (legacy
      // / pre-Batch-5). Currently exposed only via deep-link, not the
      // ARTIFACT_FILTERS chip row.
      return allTags.filter((t) => !t.artifact_type);
    }
    return allTags.filter((t) => t.artifact_type === artifactFilter);
  }, [allTags, artifactFilter]);
  // Then apply the free-text name filter on top. Case-insensitive
  // substring so the operator can paste a partial digest-derived tag
  // ("sha256-abcd") and find it without retyping the prefix exactly.
  const tags = React.useMemo(() => {
    const q = search.trim().toLowerCase();
    if (q === "") return artifactFilteredTags;
    return artifactFilteredTags.filter((t) => t.name.toLowerCase().includes(q));
  }, [artifactFilteredTags, search]);
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

  if (!isLoading && allTags.length === 0) {
    return (
      <EmptyState
        icon={<TagIcon className="size-5" />}
        title="No tags yet"
        description="Push your first image with the pull command above (swap `pull` for `push`). The tag will appear here within a few seconds."
      />
    );
  }

  // Filter chips above the table — only when there's >1 tag, otherwise
  // the chips just add visual noise. The "no tags match the filter"
  // empty state below covers the "filtered to zero" case.
  const filterChipsVisible = allTags.length > 1;

  const allVisibleSelected =
    visibleTagNames.length > 0 &&
    visibleTagNames.every((n) => selected.has(n));
  const someVisibleSelected =
    !allVisibleSelected && visibleTagNames.some((n) => selected.has(n));

  return (
    <div className="space-y-3">
      {/* S-MAINT-1 F1 — bulk scan button + the existing chip row sit */}
      {/* on the same line so they share visual weight. Falls back to */}
      {/* a stacked layout below sm: width. */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        {filterChipsVisible ? (
          <ArtifactTypeFilterChips
            value={artifactFilter}
            onChange={syncFilter}
            tags={allTags}
          />
        ) : (
          <span />
        )}
        <BulkScanAllButton
          org={org}
          repo={repo}
          tagCount={allTags.length}
        />
      </div>

      {/* Free-text tag-name search. Sits on its own row so the chip row
          above stays visually grouped with the bulk-scan button, and the
          input gets the full available width on narrow viewports without
          fighting the chips for space. */}
      <div className="relative">
        <Search
          aria-hidden
          className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-[var(--color-fg-subtle)]"
        />
        <Label htmlFor="tag-search" className="sr-only">
          Filter tags by name
        </Label>
        <Input
          id="tag-search"
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Filter tags by name"
          className="pl-9 font-mono"
          autoComplete="off"
        />
      </div>

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
                          {/* S-MAINT-1 Batch 5 — artifact-type pill. */}
                          {/* Only renders for non-image artifacts so   */}
                          {/* the common path stays uncluttered; image  */}
                          {/* rows stay clean.                          */}
                          <ArtifactTypePill artifactType={t.artifact_type} />
                          {/* REM-013 gap 1 — pending-delete pill. Renders */}
                          {/* only when the manifest is in the retention   */}
                          {/* grace window; surfaces the ETA so an operator */}
                          {/* can act before hard-delete fires.            */}
                          <PendingDeletePill iso={t.retention_pending_delete_at} />
                          {/* FE-API-050 — quarantine pill. Renders only */}
                          {/* when the parent manifest is gated by scan   */}
                          {/* policy — pulls return 451 until an admin    */}
                          {/* lifts via the tag detail page.              */}
                          {t.quarantined ? <QuarantinePill /> : null}
                          {/* Futures.md Tier 1 #2 — per-tag pin pill.   */}
                          {/* Renders when `immutable: true` on the row. */}
                          {/* Independent of the repo-wide immutability  */}
                          {/* flag (which doesn't surface per-row).      */}
                          {t.immutable ? <PinnedPill /> : null}
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

// QuarantinePill — FE-API-050 lock-icon chip rendered on tag rows whose
// parent manifest is quarantined. Pulls of these tags return 451 from
// registry-core. The pill is intentionally compact (no countdown) —
// the why-it-was-quarantined detail lives on the tag detail Security
// tab where the operator can act on it.
// PinnedPill — Futures.md Tier 1 #2 per-tag pin chip. Renders when
// the tag's `immutable` flag is set; signals to the operator that
// this specific tag is locked against re-pushes even when the parent
// repository's immutable_tags toggle is off.
function PinnedPill(): React.ReactElement {
  return (
    <span
      title="Pinned — pushes that would move this tag are rejected. Unpin via the tag detail page."
      className="inline-flex items-center gap-1 rounded-full border border-[var(--color-accent)]/40 bg-[var(--color-accent-subtle)] px-2 py-0.5 text-[10px] font-medium text-[var(--color-accent)]"
    >
      <Pin className="size-2.5" aria-hidden />
      pinned
    </span>
  );
}

function QuarantinePill(): React.ReactElement {
  return (
    <span
      title="Quarantined by scan policy — pulls return 451. Open the tag's Security tab to review or lift."
      className="inline-flex items-center gap-1 rounded-full border border-[var(--color-danger)]/40 bg-[var(--color-danger)]/10 px-2 py-0.5 text-[10px] font-medium text-[var(--color-danger)]"
    >
      <Lock className="size-2.5" aria-hidden />
      quarantined
    </span>
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

// ArtifactTypePill — S-MAINT-1 Batch 5 (F4). Compact icon + label chip
// next to the tag name when the manifest is anything other than a
// container image. Image rows skip the pill entirely so the common
// path stays uncluttered.
//
// Empty artifact_type ("" — legacy / pre-Batch-5 manifest) renders a
// neutral-toned "unknown" chip so operators can spot rows that didn't
// backfill. They'll repush naturally over time.
function ArtifactTypePill({
  artifactType,
}: {
  artifactType: ArtifactType | undefined;
}): React.ReactElement | null {
  // Image is the default expectation — no pill needed.
  if (artifactType === "image") return null;

  // No artifact_type on the wire = legacy row pre-Batch-5. Surface as
  // a quiet neutral chip rather than nothing, so the operator can
  // tell apart "this is an image" from "we don't know".
  if (!artifactType) {
    return (
      <span
        title="Manifest pre-dates Batch 5 artifact-type backfill. Will be repaired on next push."
        className="inline-flex items-center gap-1 rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-2 py-0.5 text-[10px] font-medium text-[var(--color-fg-subtle)]"
      >
        unknown
      </span>
    );
  }

  const config = ARTIFACT_PILL_CONFIG[artifactType];
  const Icon = config.Icon;
  return (
    <span
      title={config.title}
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium border",
        config.classes,
      )}
    >
      <Icon className="size-2.5" aria-hidden />
      {config.label}
    </span>
  );
}

// Per-type pill styling. Same icon set as the filter chip row above
// so the visual association is immediate (chip in row matches the chip
// at the top).
const ARTIFACT_PILL_CONFIG: Record<
  Exclude<ArtifactType, "" | "image">,
  {
    label: string;
    title: string;
    classes: string;
    Icon: React.ComponentType<{ className?: string }>;
  }
> = {
  helm: {
    label: "helm",
    title: "Helm 3 chart — pull with `helm pull oci://...` (not `docker pull`).",
    classes:
      "border-[var(--color-accent)]/40 bg-[var(--color-accent-subtle)] text-[var(--color-accent)]",
    Icon: Ship,
  },
  signature: {
    label: "sig",
    title: "OCI signature — Cosign or DSSE envelope attached as a referrer to another manifest.",
    classes:
      "border-[var(--color-warning)]/40 bg-[var(--color-warning)]/10 text-[var(--color-warning)]",
    Icon: FileSignature,
  },
  sbom: {
    label: "sbom",
    title: "SBOM attestation — SPDX or CycloneDX bill-of-materials attached as a referrer.",
    classes:
      "border-[var(--color-success)]/40 bg-[var(--color-success)]/10 text-[var(--color-success)]",
    Icon: FileCheck2,
  },
  other: {
    label: "artifact",
    title: "Unrecognised OCI artifact — config.mediaType doesn't match any known category.",
    classes:
      "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
    Icon: Package,
  },
};

// ArtifactTypeFilterChips — S-MAINT-1 Batch 5 (F4). Chip row above the
// tags table letting an operator filter by artifact category. Skips
// chips whose count would be zero so the row stays compact on simple
// repos (e.g. an image-only repo doesn't show empty Helm / SBOM chips).
// The "All" chip is always present so the operator can clear the
// filter without having to pick another category.
function ArtifactTypeFilterChips({
  value,
  onChange,
  tags,
}: {
  value: ArtifactFilter;
  onChange: (next: ArtifactFilter) => void;
  // Used to compute per-chip counts + decide which chips to hide.
  tags: ReadonlyArray<{ artifact_type?: ArtifactType }>;
}): React.ReactElement {
  // Tally artifact types once. The "" entry (legacy / unknown) folds
  // into the "image" or "other" buckets depending on the operator's
  // mental model — we count it as its own value so the user can
  // see "5 unknown" pre-backfill rows. Today the chip row doesn't
  // surface "unknown" as a chip; it shows up via the per-row pill.
  const counts = React.useMemo(() => {
    const acc: Partial<Record<ArtifactType | "all", number>> = {};
    acc.all = tags.length;
    for (const t of tags) {
      const k = t.artifact_type ?? "";
      acc[k] = (acc[k] ?? 0) + 1;
    }
    return acc;
  }, [tags]);

  return (
    <div
      className="flex flex-wrap items-center gap-1.5"
      role="group"
      aria-label="Filter by artifact type"
    >
      {ARTIFACT_FILTERS.map((opt) => {
        const count = counts[opt.value] ?? 0;
        // Hide chips that would have zero rows — except "All" which is
        // always shown so the operator can revert from any filter.
        if (opt.value !== "all" && count === 0) return null;
        const active = value === opt.value;
        const Icon = opt.Icon;
        return (
          <button
            key={opt.value}
            type="button"
            onClick={() => onChange(opt.value)}
            aria-pressed={active}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
              active
                ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
                : "border-[var(--color-border-strong)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
            )}
          >
            {Icon ? <Icon className="size-3" aria-hidden /> : null}
            {opt.label}
            <span
              className={cn(
                "tabular-nums text-[10px]",
                active
                  ? "text-[var(--color-accent)]/80"
                  : "text-[var(--color-fg-subtle)]",
              )}
            >
              {count}
            </span>
          </button>
        );
      })}
    </div>
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

// BulkScanAllButton — S-MAINT-1 F1. One-click "scan every image tag in
// this repo" with a type-to-confirm dialog so a misclick doesn't queue
// hundreds of scans. The server caps fan-out at 500 per request; the
// response carries (queued, total, capped) so the toast can show the
// real numbers.
//
// Hidden when the repo has zero tags — the button would be a no-op.
function BulkScanAllButton({
  org,
  repo,
  tagCount,
}: {
  org: string;
  repo: string;
  tagCount: number;
}): React.ReactElement | null {
  const [open, setOpen] = React.useState(false);
  if (tagCount === 0) return null;
  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setOpen(true)}
      >
        <Play className="size-3.5" /> Scan all tags
      </Button>
      <BulkScanConfirmDialog
        open={open}
        onOpenChange={setOpen}
        org={org}
        repo={repo}
        tagCount={tagCount}
      />
    </>
  );
}

// BulkScanConfirmDialog — type "SCAN" to confirm, then POSTs to
// /repositories/{org}/{repo}/scan. Toast shows the returned counters
// so the operator sees exactly what got queued (and whether they
// need to click again because of the per-request cap).
function BulkScanConfirmDialog({
  open,
  onOpenChange,
  org,
  repo,
  tagCount,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
  tagCount: number;
}): React.ReactElement {
  const mutation = useBulkScanRepo();
  const [typed, setTyped] = React.useState("");
  const EXPECTED = "SCAN";
  React.useEffect(() => {
    if (!open) setTyped("");
  }, [open]);

  async function handleSubmit(): Promise<void> {
    try {
      const res = await mutation.mutateAsync({ org, repo });
      const cappedSuffix = res.capped
        ? ` · capped at ${res.limit.toLocaleString()} — click again to continue`
        : "";
      toast.success(
        `Queued ${res.scans_queued.toLocaleString()} of ${res.tags_count.toLocaleString()} scans${cappedSuffix}`,
      );
      onOpenChange(false);
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Writer role required on this repository."
          : code === 404
            ? "Repository not found."
            : "Couldn't queue the bulk scan. Check the BFF logs.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Play className="size-4 text-[var(--color-accent)]" />
            Scan all tags in {org}/{repo}
          </DialogTitle>
          <DialogDescription>
            Queues a vulnerability scan for every image tag in this
            repository — {tagCount.toLocaleString()} {tagCount === 1 ? "tag" : "tags"} total.
            Non-image artifacts (Helm charts, signatures, SBOMs) are
            skipped automatically. Server caps each request at 500;
            click again if the toast says we hit it.
          </DialogDescription>
        </DialogHeader>

        <div>
          <Label htmlFor="bulk-scan-confirm" className="mb-2 inline-block">
            Type{" "}
            <code className="font-mono text-[var(--color-accent)]">
              {EXPECTED}
            </code>{" "}
            to confirm
          </Label>
          <Input
            id="bulk-scan-confirm"
            autoComplete="off"
            autoFocus
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            className="font-mono"
          />
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={mutation.isPending}
          >
            Cancel
          </Button>
          <Button
            type="button"
            onClick={() => void handleSubmit()}
            loading={mutation.isPending}
            disabled={mutation.isPending || typed !== EXPECTED}
          >
            <Play className="size-4" />
            Queue scans
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
