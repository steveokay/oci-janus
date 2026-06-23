import * as React from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

// Beacon — ConfirmDestructiveDialog (DSGN-003).
//
// One primitive for every destructive confirmation across the dashboard so
// `window.confirm` is gone, dialog styling stays consistent, and the
// severity gradient is enforced — operators learn that the type-to-confirm
// pattern always means "this action is irreversible and named."
//
// Three severity levels:
//
//   low    — single confirm button, dangerous but reversible (clear an
//            inherited policy, pause a webhook, dismiss a notification).
//   medium — type the resource name to confirm (remove a trusted key,
//            delete a repository, drop a custom domain).
//   high   — type a fixed phrase to confirm ("RUN GC", "DELETE TENANT").
//            Reserved for cross-cutting / cascading actions.
//
// The button copy + colour follow the severity. The dialog blocks Escape /
// outside-click while the action is in flight so the operator can't
// dismiss without a definitive outcome (matching SecretRevealDialog).

export type DestructiveSeverity = "low" | "medium" | "high";

interface ConfirmDestructiveDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: React.ReactNode;
  severity?: DestructiveSeverity;
  // For severity="medium": the resource name the user must retype. The
  // dialog shows it in a code badge above the input and gates the
  // confirm button on case-sensitive equality.
  resourceName?: string;
  // For severity="high": the fixed phrase the user must retype. Defaults
  // to "CONFIRM" if omitted but a deliberate phrase ("DELETE TENANT")
  // reads better.
  confirmPhrase?: string;
  // Confirm-button label. Defaults to "Confirm" / "Remove" / "Delete"
  // depending on severity if omitted.
  confirmLabel?: string;
  // Optional extra content rendered between description and the confirm
  // input — e.g. an inline warning about post-action behaviour.
  children?: React.ReactNode;
  onConfirm: () => Promise<void> | void;
  loading?: boolean;
}

export function ConfirmDestructiveDialog({
  open,
  onOpenChange,
  title,
  description,
  severity = "low",
  resourceName,
  confirmPhrase,
  confirmLabel,
  children,
  onConfirm,
  loading = false,
}: ConfirmDestructiveDialogProps): React.ReactElement {
  const [typed, setTyped] = React.useState("");

  // Clear typed input whenever the dialog opens — leftover text from a
  // previously cancelled flow would let an operator double-tap confirm.
  React.useEffect(() => {
    if (open) setTyped("");
  }, [open]);

  const expected =
    severity === "medium" ? resourceName : severity === "high" ? confirmPhrase ?? "CONFIRM" : null;
  const canConfirm = expected === null || typed === expected;

  const defaultLabel =
    severity === "high" ? "Delete" : severity === "medium" ? "Remove" : "Confirm";
  const buttonLabel = confirmLabel ?? defaultLabel;

  function handleOpenChange(next: boolean): void {
    // Loading flows keep the dialog open; user can't escape mid-mutation.
    if (loading) return;
    onOpenChange(next);
  }

  async function handleConfirm(): Promise<void> {
    if (!canConfirm) return;
    await onConfirm();
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription asChild>
            <div className="text-sm text-[var(--color-fg-muted)]">{description}</div>
          </DialogDescription>
        </DialogHeader>

        {children ? <div className="mb-4">{children}</div> : null}

        {expected !== null ? (
          <div className="space-y-1.5">
            <Label htmlFor="confirm-destructive-input">
              Type{" "}
              <code className="rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 font-mono text-xs text-[var(--color-fg)]">
                {expected}
              </code>{" "}
              to confirm
            </Label>
            <Input
              id="confirm-destructive-input"
              autoFocus
              autoComplete="off"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              aria-invalid={typed !== "" && !canConfirm}
            />
          </div>
        ) : null}

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={loading}
          >
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={() => void handleConfirm()}
            disabled={!canConfirm || loading}
            loading={loading}
          >
            {buttonLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
