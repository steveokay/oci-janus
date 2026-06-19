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
import {
  useUpdateWebhook,
  validateWebhookURL,
  type WebhookEndpoint,
} from "@/lib/api/webhooks";

interface EditWebhookDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  webhook: WebhookEndpoint;
}

export function EditWebhookDialog({
  open,
  onOpenChange,
  webhook,
}: EditWebhookDialogProps): React.ReactElement {
  const [url, setUrl] = React.useState(webhook.url);
  const [events, setEvents] = React.useState(webhook.events);
  const [active, setActive] = React.useState(webhook.active);
  const [urlError, setUrlError] = React.useState<string | null>(null);
  const update = useUpdateWebhook();

  // Reset to fresh server state whenever the dialog opens or the underlying
  // webhook changes (e.g. a background refetch ticked between renders).
  React.useEffect(() => {
    if (open) {
      setUrl(webhook.url);
      setEvents(webhook.events);
      setActive(webhook.active);
      setUrlError(null);
    }
  }, [open, webhook.url, webhook.events, webhook.active]);

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
      // Only send fields that actually changed — the BFF PATCH semantics
      // are "leave unspecified fields alone".
      const patch: Parameters<typeof update.mutateAsync>[0] = {
        id: webhook.endpoint_id,
      };
      if (url !== webhook.url) patch.url = url;
      if (
        events.length !== webhook.events.length ||
        events.some((e2) => !webhook.events.includes(e2))
      ) {
        patch.events = events;
      }
      if (active !== webhook.active) patch.active = active;

      await update.mutateAsync(patch);
      toast.success("Webhook updated.");
      onOpenChange(false);
    } catch (e2) {
      const status = (e2 as { response?: { status?: number } })?.response?.status;
      const message =
        status === 403
          ? "You don't have permission to edit this webhook."
          : status === 400
            ? "Backend rejected the change — most likely the SSRF guard."
            : "Couldn't update webhook. Try again, or check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[560px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Webhook className="size-4 text-[var(--color-accent)]" />
            Edit webhook
          </DialogTitle>
          <DialogDescription>
            Change URL, event subscriptions, or pause delivery. The signing
            secret stays the same — rotate it separately if you suspect leak.
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
            showActive
            active={active}
            onActiveChange={setActive}
          />

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={update.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              loading={update.isPending}
              disabled={update.isPending}
            >
              {update.isPending ? "Saving" : "Save changes"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
