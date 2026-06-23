import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Webhook, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { WebhooksTable } from "@/components/webhooks/webhooks-table";
import { CreateWebhookDialog } from "@/components/webhooks/create-webhook-dialog";
import { useWebhooks } from "@/lib/api/webhooks";

export const Route = createFileRoute("/_authenticated/webhooks/")({
  component: WebhooksPage,
});

function WebhooksPage(): React.ReactElement {
  const { data, isLoading, isError, error, refetch } = useWebhooks();
  const [createOpen, setCreateOpen] = React.useState(false);

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            Integrations
          </p>
          <h1 className="font-display text-3xl font-medium tracking-tight">
            Webhooks
          </h1>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            Stream registry events to your CI, on-call, or supply-chain stack.
            Every payload is HMAC-signed with the secret issued on creation.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" />
          New webhook
        </Button>
      </header>

      {isError ? (
        <ErrorState
          title="Couldn't load webhooks"
          description="The BFF didn't answer. The webhook routes are gated by the WEBHOOK_GRPC_ADDR env var — confirm the service is wired in your stack."
          error={error}
          onRetry={() => void refetch()}
        />
      ) : !isLoading && (data?.length ?? 0) === 0 ? (
        <EmptyState
          icon={<Webhook className="size-5" />}
          title="No webhooks yet"
          description="Create your first delivery endpoint. We sign every payload with HMAC-SHA256 so you can verify origin server-side."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" />
              Create webhook
            </Button>
          }
          secondaryAction={
            <a
              href="https://github.com/steveokay/oci-janus/blob/main/docs/EVENTS.md"
              target="_blank"
              rel="noreferrer"
              className="text-sm text-[var(--color-accent)] underline-offset-4 hover:underline"
            >
              Event types &amp; payloads
            </a>
          }
        />
      ) : (
        <WebhooksTable webhooks={data ?? []} loading={isLoading} />
      )}

      <CreateWebhookDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
      />
    </div>
  );
}
