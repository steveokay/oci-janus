import * as React from "react";
import { toast } from "sonner";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import {
  useBulkDeleteTags,
  type BulkDeleteResult,
} from "@/lib/api/tags";

interface BulkDeleteTagsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  org: string;
  repo: string;
  tagNames: string[];
  onCompleted: () => void;
}

// FE-API-036 — confirm + run a bulk tag delete. Migrated onto the shared
// ConfirmDestructiveDialog primitive (DSGN-003) for consistent styling +
// the in-flight escape-lock.
//
// Type-to-confirm gate (strength preserved from the hand-rolled version):
//   - Multi-tag (N > 1): type the COUNT ("47"). Typing 47 names would be
//     useless friction on a swipe-select, and the visible list above the
//     input (passed as the primitive's children) lets the operator audit
//     the set before they confirm.
//   - Single-tag (N == 1): type the TAG NAME. "Type 1 to confirm" was a
//     bad hand-off — the operator could blindly type "1" without reading
//     what they're about to delete. Typing the tag name forces attention.
// Both map to severity="medium" with resourceName set to the expected
// string, so the primitive gates the confirm button on exact equality.
// Once the mutation runs, the per-tag result list is surfaced in a toast
// so the operator sees which tags failed (e.g. concurrent delete).
export function BulkDeleteTagsDialog({
  open,
  onOpenChange,
  org,
  repo,
  tagNames,
  onCompleted,
}: BulkDeleteTagsDialogProps): React.ReactElement {
  const del = useBulkDeleteTags();
  // Single-tag delete swaps the gate from a count to the tag name itself
  // — typing "1" to drop a single tag was friction without protection.
  const isSingle = tagNames.length === 1;
  const expected = isSingle ? (tagNames[0] ?? "") : String(tagNames.length);

  async function handleConfirm(): Promise<void> {
    try {
      const results = await del.mutateAsync({ org, repo, tagNames });
      summarise(results);
      onCompleted();
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response
        ?.status;
      toast.error(
        status === 403
          ? "Writer role on this repo (or its parent org) is required."
          : status === 400
            ? "Backend rejected the request — too many tags or an invalid name."
            : "Bulk delete failed. Retry, or check the BFF logs.",
      );
    }
  }

  return (
    <ConfirmDestructiveDialog
      open={open}
      onOpenChange={onOpenChange}
      severity="medium"
      // expected is the COUNT (multi) or the TAG NAME (single) — the gate
      // strength is identical to the previous bespoke implementation.
      resourceName={expected}
      title={`Delete ${tagNames.length} ${tagNames.length === 1 ? "tag" : "tags"}?`}
      confirmLabel={`Delete ${tagNames.length}`}
      loading={del.isPending}
      onConfirm={handleConfirm}
      description={
        <>
          This removes the selected tags from{" "}
          <code className="font-mono">
            {org}/{repo}
          </code>
          . The underlying manifests stay reachable by digest until the next GC
          sweep, but pulls by tag will fail immediately.
        </>
      }
    >
      {/* Audit list — the full set the operator is about to delete, rendered
          between the description and the type-to-confirm input. */}
      <div className="max-h-40 overflow-auto rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
        <ul className="space-y-0.5">
          {tagNames.map((t) => (
            <li
              key={t}
              className="truncate font-mono text-xs text-[var(--color-fg)]"
            >
              {t}
            </li>
          ))}
        </ul>
      </div>
    </ConfirmDestructiveDialog>
  );
}

// summarise — pick the right toast based on the per-tag result mix.
// All-success → success toast, partial → message with the failure list,
// total failure → error.
function summarise(results: BulkDeleteResult[]): void {
  const deleted = results.filter((r) => r.deleted);
  const failed = results.filter((r) => !r.deleted);
  if (failed.length === 0) {
    toast.success(
      `Deleted ${deleted.length} ${deleted.length === 1 ? "tag" : "tags"}.`,
    );
    return;
  }
  if (deleted.length === 0) {
    toast.error(
      `All ${failed.length} ${failed.length === 1 ? "tag" : "tags"} failed. First reason: ${failed[0]?.reason ?? "unknown"}.`,
    );
    return;
  }
  toast.message(
    `Deleted ${deleted.length} of ${results.length}.`,
    {
      description: `${failed.length} failed — ${failed
        .slice(0, 3)
        .map((f) => `${f.tag_name} (${f.reason ?? "failed"})`)
        .join(", ")}${failed.length > 3 ? "…" : ""}`,
    },
  );
}
