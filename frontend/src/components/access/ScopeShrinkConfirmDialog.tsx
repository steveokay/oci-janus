import * as React from "react";
import { X } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";

// ScopeShrinkConfirmDialog — FE-API-048 T26.
//
// Shown when an operator narrows `allowed_scopes` for a service account and
// one or more existing API keys would be affected. The preflight query returns
// `affected_keys` (N); we surface that count here alongside the removed scopes
// so the operator understands the blast radius before committing.
//
// The dialog is data-free — it receives already-resolved preflight data from
// the parent (ServiceAccountDetail) so it can render synchronously with no
// internal network calls.

interface ScopeShrinkConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // The human-readable name of the service account being modified.
  saName: string;
  // Scopes that will be REMOVED after the change.
  removed: string[];
  // Number of active API keys that would be immediately affected.
  affectedKeys: number;
  // Whether the parent is currently executing the PATCH mutation.
  confirming: boolean;
  // Called when the operator confirms the scope change.
  onConfirm: () => void;
}

// ScopeShrinkConfirmDialog renders a blocking dialog when narrowing scopes
// would affect at least one active key. The parent is responsible for closing
// the dialog (via onOpenChange) after the confirm mutation resolves.
export function ScopeShrinkConfirmDialog({
  open,
  onOpenChange,
  saName,
  removed,
  affectedKeys,
  confirming,
  onConfirm,
}: ScopeShrinkConfirmDialogProps): React.ReactElement {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            Narrow allowed scopes for {saName}?
          </DialogTitle>
          <DialogDescription>
            This will{" "}
            <strong className="text-[var(--color-danger)]">
              immediately stop {affectedKeys} active key
              {affectedKeys !== 1 ? "s" : ""}
            </strong>{" "}
            from using the removed scope{removed.length !== 1 ? "s" : ""}:
          </DialogDescription>
        </DialogHeader>

        {/* Removed scope chips */}
        <div className="flex flex-wrap gap-1.5 rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-3 py-2.5">
          {removed.map((scope) => (
            <span
              key={scope}
              className="inline-flex items-center gap-1 rounded-full border border-[var(--color-danger)]/40 bg-[var(--color-danger)]/10 px-2 py-0.5 font-mono text-[11px] text-[var(--color-danger)]"
            >
              <X className="size-3" aria-hidden />
              {scope}
            </span>
          ))}
        </div>

        {/* Impact note */}
        <p className="text-sm text-[var(--color-fg-muted)]">
          Existing tokens will keep working for non-removed scopes only. There
          is no undo — re-add the scope and ask the key owner to retry if you
          need to reverse this.
        </p>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={confirming}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            loading={confirming}
            disabled={confirming}
            onClick={onConfirm}
          >
            Yes, narrow scopes
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
