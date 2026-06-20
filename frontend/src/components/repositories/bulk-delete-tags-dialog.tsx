import * as React from "react";
import { toast } from "sonner";
import { AlertTriangle, Trash2 } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
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

// FE-API-036 — confirm + run a bulk tag delete.
//
// Type-to-confirm gate: the operator types the count as a string ("47").
// Cheap insurance against an accidental swipe-select on a long list.
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
  const expected = String(tagNames.length);
  const [typed, setTyped] = React.useState("");

  // Reset the input when the dialog closes so a re-open starts clean.
  React.useEffect(() => {
    if (!open) setTyped("");
  }, [open]);

  const canConfirm = typed === expected && tagNames.length > 0;

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
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <AlertTriangle className="size-4 text-[var(--color-danger)]" />
            Delete {tagNames.length}{" "}
            {tagNames.length === 1 ? "tag" : "tags"}?
          </DialogTitle>
          <DialogDescription>
            This removes the selected tags from{" "}
            <code className="font-mono">
              {org}/{repo}
            </code>
            . The underlying manifests stay reachable by digest until the
            next GC sweep, but pulls by tag will fail immediately.
          </DialogDescription>
        </DialogHeader>

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

        <div>
          <Label htmlFor="bulk-confirm" className="mb-2 inline-block">
            Type{" "}
            <code className="font-mono text-[var(--color-danger)]">
              {expected}
            </code>{" "}
            to confirm
          </Label>
          <Input
            id="bulk-confirm"
            autoFocus
            autoComplete="off"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            className="font-mono"
            inputMode="numeric"
          />
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={del.isPending}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            onClick={() => void handleConfirm()}
            loading={del.isPending}
            disabled={!canConfirm || del.isPending}
          >
            <Trash2 className="size-4" />
            Delete {tagNames.length}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
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
