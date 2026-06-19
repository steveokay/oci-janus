import * as React from "react";
import { useNavigate } from "@tanstack/react-router";
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
  useDeleteWebhook,
  type WebhookEndpoint,
} from "@/lib/api/webhooks";

interface DeleteWebhookDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  webhook: WebhookEndpoint;
}

// Beacon — type-the-URL-to-confirm. Same destructive pattern as the
// repository delete dialog. Using the URL (which the operator pasted in
// to begin with) is enough friction; we don't need them to type the UUID.
export function DeleteWebhookDialog({
  open,
  onOpenChange,
  webhook,
}: DeleteWebhookDialogProps): React.ReactElement {
  const [confirmText, setConfirmText] = React.useState("");
  const navigate = useNavigate();
  const del = useDeleteWebhook();
  const matches = confirmText.trim() === webhook.url;
  const submitting = del.isPending;

  React.useEffect(() => {
    if (!open) setConfirmText("");
  }, [open]);

  async function onConfirm(): Promise<void> {
    try {
      await del.mutateAsync(webhook.endpoint_id);
      toast.success("Webhook deleted.");
      onOpenChange(false);
      void navigate({ to: "/webhooks" });
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const message =
        status === 403
          ? "You don't have permission to delete this webhook."
          : "Delete failed. Try again, or check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-danger)]">
            <AlertTriangle className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Destructive action
            </span>
          </div>
          <DialogTitle>Delete webhook</DialogTitle>
          <DialogDescription>
            Stops delivery to this endpoint and drops its delivery log. The
            signing secret is invalidated; a re-created endpoint will issue a
            fresh one. Cannot be undone.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label htmlFor="confirm-webhook">
            Type the URL to confirm
          </Label>
          <Input
            id="confirm-webhook"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={webhook.url}
            className="font-mono text-xs"
          />
          <p className="break-all font-mono text-[10px] text-[var(--color-fg-subtle)]">
            {webhook.url}
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
            variant="danger"
            onClick={() => void onConfirm()}
            disabled={!matches || submitting}
            loading={submitting}
          >
            {submitting ? "Deleting" : "Delete webhook"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
