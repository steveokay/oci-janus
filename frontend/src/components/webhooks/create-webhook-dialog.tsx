import * as React from "react";
import { toast } from "sonner";
import { Webhook } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { WebhookFormFields } from "./webhook-form-fields";
import { SecretRevealDialog } from "./secret-reveal-dialog";
import {
  useCreateWebhook,
  validateWebhookURL,
} from "@/lib/api/webhooks";

interface CreateWebhookDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CreateWebhookDialog({
  open,
  onOpenChange,
}: CreateWebhookDialogProps): React.ReactElement {
  const [url, setUrl] = React.useState("");
  const [events, setEvents] = React.useState<string[]>([]);
  const [urlError, setUrlError] = React.useState<string | null>(null);
  const [secret, setSecret] = React.useState<string | null>(null);
  const create = useCreateWebhook();

  // Reset state every time the dialog closes — so reopening doesn't leak
  // a previous URL or partial selection.
  React.useEffect(() => {
    if (!open) {
      setUrl("");
      setEvents([]);
      setUrlError(null);
    }
  }, [open]);

  async function onSubmit(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    const urlIssue = validateWebhookURL(url);
    if (urlIssue) {
      setUrlError(urlIssue);
      return;
    }
    if (events.length === 0) {
      toast.error("Pick at least one event to subscribe to.");
      return;
    }
    try {
      const result = await create.mutateAsync({ url, events });
      onOpenChange(false);
      // Reveal the secret in the show-once dialog. We do this AFTER closing
      // the create dialog so they don't stack.
      setSecret(result.secret);
    } catch (e2) {
      const status = (e2 as { response?: { status?: number } })?.response?.status;
      const message =
        status === 403
          ? "You don't have permission to create webhooks here."
          : status === 400
            ? "Backend rejected the URL — most likely the SSRF guard."
            : "Couldn't create webhook. Try again, or check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="max-w-[560px]">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Webhook className="size-4 text-[var(--color-accent)]" />
              Create webhook
            </DialogTitle>
            <DialogDescription>
              Subscribe an external system to registry events. The signing
              secret is generated server-side and shown to you once on
              creation.
            </DialogDescription>
          </DialogHeader>

          <form onSubmit={(e) => void onSubmit(e)} className="space-y-5">
            <WebhookFormFields
              url={url}
              onUrlChange={(v) => {
                setUrl(v);
                if (urlError) setUrlError(null);
              }}
              urlError={urlError}
              selectedEvents={events}
              onEventsChange={setEvents}
            />

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={create.isPending}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                loading={create.isPending}
                disabled={create.isPending}
              >
                {create.isPending ? "Creating" : "Create webhook"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <SecretRevealDialog
        open={secret !== null}
        onOpenChange={(o) => {
          if (!o) setSecret(null);
        }}
        secret={secret}
        title="Webhook signing secret"
        description="This secret HMAC-signs every payload we deliver to your endpoint. Verify it on receipt before processing."
        onAcknowledge={() => {
          toast.success("Webhook created.");
        }}
      />
    </>
  );
}
