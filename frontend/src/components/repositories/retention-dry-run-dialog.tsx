import * as React from "react";
import { AlertTriangle, FileSearch, Lock, Trash2 } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import type { DryRunResponse, UpdateRetentionBody } from "@/lib/api/retention";
import { useDryRunRetention } from "@/lib/api/retention";
import {
  formatAbsoluteDate,
  formatBytes,
  formatRelativeDate,
  shortenDigest,
} from "@/lib/format";

// Beacon — RetentionDryRunDialog (S11 Slice 2, FE-API-038).
//
// Modal that the rule editor opens before any save lands on the server.
// Runs the candidate policy against POST .../retention/dry-run and renders
// the would-delete + protected-skipped previews so the operator can audit
// the blast radius before committing.
//
// The dialog accepts an `onConfirm` callback that runs the actual PUT —
// the dry-run + save are two server hits, but the operator only sees one
// dialog dismissal. Saving is gated on a successful dry-run (no save
// button is rendered until the response arrives) so we never persist a
// policy the operator hasn't reviewed.

const ROW_LIMIT = 50;

interface RetentionDryRunDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
  // The candidate policy to preview. Always supplied — the editor only
  // opens the dialog once the form passes its own client-side validation.
  candidate: UpdateRetentionBody;
  // onConfirm runs after the operator clicks "Save policy" — typically a
  // PUT call. The dialog closes itself on resolve; on reject, the
  // dry-run result stays visible so the operator can retry.
  onConfirm: () => Promise<void>;
  // Whether the save mutation is in flight, used to disable the button
  // and avoid double-submits.
  saving: boolean;
}

export function RetentionDryRunDialog({
  open,
  onOpenChange,
  org,
  repo,
  candidate,
  onConfirm,
  saving,
}: RetentionDryRunDialogProps): React.ReactElement {
  const dryRun = useDryRunRetention(org, repo);

  // Trigger the dry-run whenever the dialog opens (or the candidate
  // changes while open). We don't memoize per (org, repo, candidate)
  // because each open is logically a new evaluation — operators expect
  // fresh numbers after toggling chips between opens.
  React.useEffect(() => {
    if (!open) return;
    dryRun.mutate(candidate);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, candidate]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[720px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <FileSearch className="size-4 text-[var(--color-accent)]" />
            Preview retention impact
          </DialogTitle>
          <DialogDescription>
            This is what the policy would delete if you saved it right now.
            Nothing has been written yet. Protected tag patterns are checked
            first — manifests matching any pattern are spared even if a rule
            would otherwise sweep them.
          </DialogDescription>
        </DialogHeader>

        {dryRun.isError ? (
          <ErrorState
            title="Dry-run failed"
            description="The retention evaluator didn't answer. Try again, or check the BFF logs."
            onRetry={() => dryRun.mutate(candidate)}
          />
        ) : dryRun.isPending || !dryRun.data ? (
          <DryRunSkeleton />
        ) : (
          <DryRunBody data={dryRun.data} />
        )}

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            Cancel
          </Button>
          <Button
            onClick={async () => {
              try {
                await onConfirm();
                onOpenChange(false);
              } catch {
                // Caller toasts the failure — keep the dialog open so the
                // operator can decide whether to retry without re-running
                // the dry-run from scratch.
              }
            }}
            // Disable until the dry-run completes so the operator can't
            // bypass the preview. Also disable while saving so the
            // button can't be double-clicked.
            disabled={!dryRun.data || saving}
            loading={saving}
          >
            {saving ? "Saving" : "Save policy"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// DryRunBody — totals + would-delete table + protected-skipped table.
// Both tables truncate to ROW_LIMIT to keep the dialog from rendering a
// 5000-row grid; the totals strip is unchanged so the operator still
// sees the true blast radius.
function DryRunBody({ data }: { data: DryRunResponse }): React.ReactElement {
  const wouldDelete = data.would_delete.slice(0, ROW_LIMIT);
  const wouldDeleteHidden = Math.max(
    0,
    data.would_delete.length - wouldDelete.length,
  );
  const protectedShown = data.protected_skipped.slice(0, ROW_LIMIT);
  const protectedHidden = Math.max(
    0,
    data.protected_skipped.length - protectedShown.length,
  );

  return (
    <div className="space-y-5">
      {/* Totals strip — the headline impact. Three stats in a row so the
          operator sees them without scrolling on a small viewport. */}
      <div className="grid grid-cols-3 gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
        <Stat
          label="Would delete"
          value={data.total_count.toLocaleString()}
          tone={data.total_count > 0 ? "danger" : "neutral"}
          icon={<Trash2 className="size-3.5" aria-hidden />}
        />
        <Stat
          label="Bytes freed"
          value={formatBytes(data.total_bytes)}
          tone="neutral"
        />
        <Stat
          label="Protected"
          value={data.protected_skipped.length.toLocaleString()}
          tone="neutral"
          icon={<Lock className="size-3.5" aria-hidden />}
        />
      </div>

      {data.truncated ? (
        <div className="flex items-center gap-2 rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 px-3 py-2 text-xs text-[var(--color-fg)]">
          <AlertTriangle className="size-3.5 text-[var(--color-warning)]" aria-hidden />
          The evaluator capped the would-delete list at the maximum
          response size. Totals above still reflect the full impact;
          rows below are the first {wouldDelete.length}.
        </div>
      ) : null}

      <Section
        title={`Would delete (${wouldDelete.length}${wouldDeleteHidden > 0 ? `+${wouldDeleteHidden} more` : ""})`}
      >
        {wouldDelete.length === 0 ? (
          <EmptyHint>
            No manifests match this policy right now. Saving is safe — nothing
            would be marked for deletion.
          </EmptyHint>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Digest</TableHead>
                <TableHead>Tags</TableHead>
                <TableHead>Pushed</TableHead>
                <TableHead className="text-right">Size</TableHead>
                <TableHead>Reason</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {wouldDelete.map((row) => (
                <TableRow key={row.manifest_id}>
                  <TableCell>
                    <code className="font-mono text-[11px] text-[var(--color-fg-muted)]">
                      {shortDigest(row.manifest_digest)}
                    </code>
                  </TableCell>
                  <TableCell>
                    <TagList tags={row.tags} />
                  </TableCell>
                  <TableCell
                    className="text-xs text-[var(--color-fg-muted)]"
                    title={formatAbsoluteDate(row.pushed_at)}
                  >
                    {formatRelativeDate(row.pushed_at)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums text-xs text-[var(--color-fg-muted)]">
                    {formatBytes(row.size_bytes)}
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {row.reasons.map((r) => (
                        <Badge key={r} tone="danger">
                          {r}
                        </Badge>
                      ))}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Section>

      {protectedShown.length > 0 ? (
        <Section
          title={`Protected — spared by tag patterns (${protectedShown.length}${protectedHidden > 0 ? `+${protectedHidden} more` : ""})`}
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Digest</TableHead>
                <TableHead>Tags</TableHead>
                <TableHead>Matched pattern</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {protectedShown.map((row) => (
                <TableRow key={row.manifest_id}>
                  <TableCell>
                    <code className="font-mono text-[11px] text-[var(--color-fg-muted)]">
                      {shortDigest(row.manifest_digest)}
                    </code>
                  </TableCell>
                  <TableCell>
                    <TagList tags={row.tags} />
                  </TableCell>
                  <TableCell>
                    <code className="rounded border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-1.5 py-0.5 font-mono text-[11px] text-[var(--color-fg)]">
                      {row.matched_pattern}
                    </code>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Section>
      ) : null}

      <p className="text-[11px] text-[var(--color-fg-subtle)]">
        Evaluated {formatRelativeDate(data.evaluated_at)}.
      </p>
    </div>
  );
}

function Stat({
  label,
  value,
  tone,
  icon,
}: {
  label: string;
  value: string;
  tone: "danger" | "neutral";
  icon?: React.ReactNode;
}): React.ReactElement {
  return (
    <div className="text-center">
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        <span className="inline-flex items-center gap-1">
          {icon}
          {label}
        </span>
      </div>
      <div
        className={
          tone === "danger"
            ? "mt-0.5 font-display text-2xl font-medium tabular-nums text-[var(--color-danger)]"
            : "mt-0.5 font-display text-2xl font-medium tabular-nums text-[var(--color-fg)]"
        }
      >
        {value}
      </div>
    </div>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <section className="space-y-2">
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        {title}
      </div>
      {children}
    </section>
  );
}

// EmptyHint — soft empty state shown inside the dialog when a section
// has no rows. Visually distinct from EmptyState (which is full-card) so
// the dialog stays compact.
function EmptyHint({
  children,
}: {
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <div className="rounded-md border border-dashed border-[var(--color-border)] p-3 text-xs text-[var(--color-fg-muted)]">
      {children}
    </div>
  );
}

// TagList — comma-separated tag chips. Truncates at 3 tags + counter so
// a 50-tag manifest doesn't blow out a row. The full list is in the
// title attribute for hover discovery.
function TagList({ tags }: { tags: string[] }): React.ReactElement {
  if (tags.length === 0) {
    return (
      <span className="text-xs italic text-[var(--color-fg-subtle)]">
        untagged
      </span>
    );
  }
  const shown = tags.slice(0, 3);
  const extra = tags.length - shown.length;
  return (
    <span
      className="text-xs text-[var(--color-fg-muted)]"
      title={tags.join(", ")}
    >
      {shown.join(", ")}
      {extra > 0 ? ` +${extra}` : ""}
    </span>
  );
}

// shortDigest is now a thin re-export of the shared helper in
// @/lib/format so the proxy-cache + tags + retention surfaces stay in
// visual lockstep. Kept as a local name so the existing call sites
// don't churn.
const shortDigest = shortenDigest;

function DryRunSkeleton(): React.ReactElement {
  return (
    <div className="space-y-5">
      <div className="grid grid-cols-3 gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
      </div>
      <div className="space-y-2">
        <Skeleton className="h-3 w-32" />
        <Skeleton className="h-32 w-full" />
      </div>
    </div>
  );
}
