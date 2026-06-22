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
import { Button } from "@/components/ui/button";
import { useDeleteApiKey, type ApiKey } from "@/lib/api/api-keys";

interface DeleteApiKeyDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  apiKey: ApiKey;
}

// Beacon — DeleteApiKeyDialog. Single-click confirmation (no type-to-confirm —
// API keys are revocable artefacts, and showing the name is enough context).
// Any CI/script using the key starts failing the next time it tries to auth;
// that's the expected user contract.
export function DeleteApiKeyDialog({
  open,
  onOpenChange,
  apiKey,
}: DeleteApiKeyDialogProps): React.ReactElement {
  const [submitting, setSubmitting] = React.useState(false);
  const del = useDeleteApiKey();

  async function handleConfirm(): Promise<void> {
    setSubmitting(true);
    try {
      await del.mutateAsync(apiKey.id);
      toast.success(`Revoked "${apiKey.name}".`);
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      toast.error(
        status === 403
          ? "You don't have permission to revoke this key."
          : "Couldn't revoke. Try again.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-danger)]">
            <AlertTriangle className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Revoke access
            </span>
          </div>
          <DialogTitle>Revoke API key</DialogTitle>
          <DialogDescription>
            Any script or CI job using{" "}
            <span className="font-mono text-[var(--color-fg)]">
              {apiKey.name}
            </span>{" "}
            will start failing authentication immediately. If you need to rotate,
            issue a new key first and switch your consumers over.
          </DialogDescription>
        </DialogHeader>

        <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
          <div className="text-sm font-medium">{apiKey.name}</div>
          <div className="font-mono text-[10px] text-[var(--color-fg-subtle)]">
            {apiKey.prefix}…
          </div>
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
            variant="danger"
            onClick={() => void handleConfirm()}
            disabled={submitting}
            loading={submitting}
          >
            {submitting ? "Revoking" : "Revoke key"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
