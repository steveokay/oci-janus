import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  ChevronRight,
  Pencil,
  RefreshCw,
  Trash2,
  PauseCircle,
  CheckCircle2,
  Globe,
} from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { DeliveriesPanel } from "@/components/webhooks/deliveries-panel";
import { TestDispatchPanel } from "@/components/webhooks/test-dispatch-panel";
import { EditWebhookDialog } from "@/components/webhooks/edit-webhook-dialog";
import { DeleteWebhookDialog } from "@/components/webhooks/delete-webhook-dialog";
import { SecretRevealDialog } from "@/components/webhooks/secret-reveal-dialog";
import { useWebhook, useRotateSecret } from "@/lib/api/webhooks";
import { formatAbsoluteDate } from "@/lib/format";

export const Route = createFileRoute("/_authenticated/webhooks/$id")({
  component: WebhookDetailPage,
});

function WebhookDetailPage(): React.ReactElement {
  const { id } = Route.useParams();
  const { data: webhook, isLoading, isError, refetch } = useWebhook(id);
  const [editOpen, setEditOpen] = React.useState(false);
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [revealSecret, setRevealSecret] = React.useState<string | null>(null);

  const rotate = useRotateSecret();

  async function handleRotate(): Promise<void> {
    try {
      const result = await rotate.mutateAsync(id);
      setRevealSecret(result.secret);
    } catch {
      toast.error("Couldn't rotate secret. Try again, or check the BFF logs.");
    }
  }

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load webhook"
        description="The BFF didn't answer. Retry or check logs."
        onRetry={() => void refetch()}
      />
    );
  }

  return (
    <div className="space-y-8">
      {/* Breadcrumb */}
      <nav
        aria-label="Breadcrumb"
        className="flex items-center gap-1 text-xs text-[var(--color-fg-muted)]"
      >
        <Link to="/webhooks" className="hover:text-[var(--color-fg)]">
          Webhooks
        </Link>
        <ChevronRight className="size-3 text-[var(--color-fg-subtle)]" />
        {isLoading ? (
          <Skeleton className="h-3 w-48" />
        ) : (
          <span className="font-mono text-[var(--color-fg)]">
            {truncateUrl(webhook?.url ?? "")}
          </span>
        )}
      </nav>

      {/* Identity card */}
      <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
        <div className="space-y-2">
          <div className="flex items-center gap-2.5">
            <span
              className="grid size-10 shrink-0 place-items-center rounded-lg bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
              aria-hidden
            >
              <Globe className="size-5" />
            </span>
            {isLoading ? (
              <Skeleton className="h-7 w-72" />
            ) : (
              <h1 className="break-all font-mono text-lg font-medium text-[var(--color-fg)]">
                {webhook?.url}
              </h1>
            )}
          </div>
          {isLoading ? (
            <Skeleton className="h-4 w-48" />
          ) : (
            <div className="flex flex-wrap items-center gap-2 pl-[52px] text-xs">
              {webhook?.active ? (
                <Badge tone="success">
                  <CheckCircle2 className="size-3" /> Active
                </Badge>
              ) : (
                <Badge tone="neutral">
                  <PauseCircle className="size-3" /> Paused
                </Badge>
              )}
              <span className="text-[var(--color-fg-subtle)]">
                Created {formatAbsoluteDate(webhook?.created_at)}
              </span>
              <code className="font-mono text-[10px] text-[var(--color-fg-subtle)]">
                {id.slice(0, 12)}
              </code>
            </div>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setEditOpen(true)}
            disabled={isLoading}
          >
            <Pencil className="size-3.5" />
            Edit
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void handleRotate()}
            loading={rotate.isPending}
            disabled={isLoading || rotate.isPending}
          >
            <RefreshCw className="size-3.5" />
            Rotate secret
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setDeleteOpen(true)}
            disabled={isLoading}
            className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
          >
            <Trash2 className="size-3.5" />
            Delete
          </Button>
        </div>
      </div>

      {/* Subscribed events */}
      {!isLoading && webhook ? (
        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Subscribed events
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex flex-wrap gap-1.5">
              {webhook.events.map((e) => (
                <Badge key={e} tone="accent" className="font-mono">
                  {e}
                </Badge>
              ))}
            </div>
          </CardContent>
        </Card>
      ) : null}

      {/* Test dispatch */}
      <TestDispatchPanel endpointId={id} />

      {/* Delivery log */}
      <DeliveriesPanel endpointId={id} />

      {/* Dialogs */}
      {webhook ? (
        <>
          <EditWebhookDialog
            open={editOpen}
            onOpenChange={setEditOpen}
            webhook={webhook}
          />
          <DeleteWebhookDialog
            open={deleteOpen}
            onOpenChange={setDeleteOpen}
            webhook={webhook}
          />
        </>
      ) : null}

      <SecretRevealDialog
        open={revealSecret !== null}
        onOpenChange={(o) => {
          if (!o) setRevealSecret(null);
        }}
        secret={revealSecret}
        title="New signing secret"
        description="The old secret is immediately invalidated. Update any receivers before events fire."
        onAcknowledge={() => {
          toast.success("Secret rotated.");
        }}
      />
    </div>
  );
}

function truncateUrl(url: string): string {
  try {
    const u = new URL(url);
    const path = u.pathname.length > 24 ? `${u.pathname.slice(0, 21)}…` : u.pathname;
    return `${u.host}${path}`;
  } catch {
    return url.slice(0, 40);
  }
}
