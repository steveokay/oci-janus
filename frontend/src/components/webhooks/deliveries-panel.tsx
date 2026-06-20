import * as React from "react";
import {
  CircleCheck,
  CircleX,
  Clock,
  CircleAlert,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Badge } from "@/components/ui/badge";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import {
  useDeliveries,
  type DeliveryStatus,
  type WebhookDelivery,
} from "@/lib/api/webhooks";
import { ComingSoonHint } from "@/components/common/coming-soon-hint";
import { cn } from "@/lib/utils";

interface DeliveriesPanelProps {
  endpointId: string;
}

const STATUS_META: Record<
  DeliveryStatus,
  {
    icon: React.ComponentType<{ className?: string }>;
    tone: "success" | "warning" | "danger" | "neutral";
    badge: React.ComponentProps<typeof Badge>["tone"];
  }
> = {
  delivered: { icon: CircleCheck, tone: "success", badge: "success" },
  pending: { icon: Clock, tone: "warning", badge: "warning" },
  failed: { icon: CircleAlert, tone: "warning", badge: "warning" },
  dead: { icon: CircleX, tone: "danger", badge: "danger" },
};

export function DeliveriesPanel({
  endpointId,
}: DeliveriesPanelProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useDeliveries({
    id: endpointId,
    limit: 50,
  });

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load deliveries"
        description="The delivery log didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Recent deliveries
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="space-y-5">
            {Array.from({ length: 3 }).map((_, i) => (
              <SkeletonRow key={i} />
            ))}
          </div>
        </CardContent>
      </Card>
    );
  }

  if (!data || data.length === 0) {
    return (
      <EmptyState
        icon={<Clock className="size-5" />}
        title="No deliveries yet"
        description="Once an event your endpoint subscribes to fires, the delivery attempt + outcome will appear here. Test dispatches don't count."
      />
    );
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Recent deliveries
          </CardDescription>
          <span className="text-xs text-[var(--color-fg-muted)]">
            {data.length} {data.length === 1 ? "event" : "events"}
          </span>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <ol className="relative space-y-5">
          {/* Vertical rail behind the status dots */}
          <span
            aria-hidden
            className="absolute left-[7px] top-2 bottom-2 w-px bg-[var(--color-border)]"
          />
          {data.map((d) => (
            <DeliveryRow key={d.delivery_id} delivery={d} />
          ))}
        </ol>
        <ComingSoonHint apiId="FE-API-035">
          Expand-row will show the request payload + response body in monospace
          blocks for debugging. Today the list deliberately omits payload to
          keep the response small.
        </ComingSoonHint>
      </CardContent>
    </Card>
  );
}

function DeliveryRow({
  delivery,
}: {
  delivery: WebhookDelivery;
}): React.ReactElement {
  const meta = STATUS_META[delivery.status];
  const Icon = meta.icon;
  return (
    <li className="relative flex gap-4">
      <span
        aria-hidden
        className={cn(
          "relative z-10 mt-0.5 grid size-[15px] shrink-0 place-items-center rounded-full",
          meta.tone === "success" &&
            "bg-[var(--color-success)]/15 text-[var(--color-success)]",
          meta.tone === "warning" &&
            "bg-[var(--color-warning)]/15 text-[var(--color-warning)]",
          meta.tone === "danger" &&
            "bg-[var(--color-danger)]/15 text-[var(--color-danger)]",
          meta.tone === "neutral" &&
            "bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
        )}
      >
        <Icon className="size-3" />
      </span>
      <div className="min-w-0 flex-1 pb-1">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <Badge tone={meta.badge}>{delivery.status}</Badge>
          <code className="font-mono text-xs font-medium text-[var(--color-fg)]">
            {delivery.event_type}
          </code>
          <span
            className="text-xs text-[var(--color-fg-subtle)]"
            title={formatAbsoluteDate(delivery.created_at)}
          >
            {formatRelativeDate(delivery.created_at)}
          </span>
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-[var(--color-fg-muted)]">
          <span>
            Attempts{" "}
            <span className="font-mono text-[var(--color-fg)]">
              {delivery.attempts}/{delivery.max_attempts}
            </span>
          </span>
          {delivery.delivered_at ? (
            <span>
              Delivered{" "}
              <span className="text-[var(--color-fg)]">
                {formatRelativeDate(delivery.delivered_at)}
              </span>
            </span>
          ) : delivery.next_attempt_at ? (
            <span>
              Next attempt{" "}
              <span className="text-[var(--color-fg)]">
                {formatRelativeDate(delivery.next_attempt_at)}
              </span>
            </span>
          ) : null}
        </div>
        {delivery.last_error ? (
          <div className="mt-2 rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-3 py-2 font-mono text-xs text-[var(--color-fg)]">
            {delivery.last_error}
          </div>
        ) : null}
      </div>
    </li>
  );
}

function SkeletonRow(): React.ReactElement {
  return (
    <div className="flex gap-4">
      <Skeleton className="size-[15px] rounded-full" />
      <div className="flex-1 space-y-1.5">
        <Skeleton className="h-3 w-56" />
        <Skeleton className="h-2.5 w-72" />
      </div>
    </div>
  );
}
