import * as React from "react";
import { toast } from "sonner";
import { AlertTriangle } from "lucide-react";
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
  useSetActiveAdapter,
  type AdminAdapter,
} from "@/lib/api/admin-scanners";

interface SetActiveDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  adapter: AdminAdapter;
  // The currently-active adapter, shown for context. Optional because the
  // page may not have resolved it yet (we still allow promotion).
  currentActive?: AdminAdapter | null;
}

// Beacon — SetActiveDialog.
//
// Promoting a different adapter is a platform-wide change: every tenant's
// next scan will run through the new binary. Type-the-name gating
// mirrors DeleteTenantDialog so the operator can't fat-finger a swap.
// Backend is atomic — in-memory worker pool swap + scanner_settings DB
// persist happen in a single PATCH. No container restart required.
export function SetActiveDialog({
  open,
  onOpenChange,
  adapter,
  currentActive,
}: SetActiveDialogProps): React.ReactElement {
  const [confirmText, setConfirmText] = React.useState("");
  const setActive = useSetActiveAdapter();
  const matches = confirmText.trim() === adapter.name;
  const submitting = setActive.isPending;

  React.useEffect(() => {
    if (!open) setConfirmText("");
  }, [open]);

  async function onConfirm(): Promise<void> {
    try {
      await setActive.mutateAsync({ adapter_path: adapter.path });
      toast.success(`Active scanner is now ${adapter.name}.`);
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response
        ?.status;
      const message =
        status === 403
          ? "Platform-admin grant required."
          : status === 404
            ? "Adapter not found on the scanner service (path may have changed)."
            : status === 400
              ? "Backend rejected the swap — adapter may have failed its handshake."
              : "Swap failed. Check the management BFF + scanner service logs.";
      toast.error(message);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-highlight)]">
            <AlertTriangle className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Platform-wide change
            </span>
          </div>
          <DialogTitle>Set active scanner adapter</DialogTitle>
          <DialogDescription>
            Every new scan on this control plane will run through{" "}
            <span className="font-mono text-[var(--color-fg)]">
              {adapter.name}
            </span>{" "}
            (v{adapter.version}). In-flight scans complete on the old adapter;
            the next ones pick up the new binary without a container restart.
            {currentActive ? (
              <>
                {" "}
                Replaces{" "}
                <span className="font-mono text-[var(--color-fg)]">
                  {currentActive.name}
                </span>
                .
              </>
            ) : null}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label htmlFor="confirm-adapter">
            Type{" "}
            <span className="font-mono normal-case text-[var(--color-fg)]">
              {adapter.name}
            </span>{" "}
            to confirm
          </Label>
          <Input
            id="confirm-adapter"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={adapter.name}
            className="font-mono"
          />
          <p className="font-mono text-[10px] text-[var(--color-fg-subtle)]">
            {adapter.path}
          </p>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="highlight"
            onClick={() => void onConfirm()}
            disabled={!matches || submitting}
            loading={submitting}
          >
            {submitting ? "Switching" : "Make active"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
