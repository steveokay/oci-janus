import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  Activity as ActivityIcon,
  ArrowUpCircle,
  Webhook as WebhookIcon,
  ShieldCheck,
  ShieldAlert,
  Trash2,
  FileSignature,
  Inbox,
  CheckCircle2,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import {
  NOTIFICATION_EVENT_LABELS,
  NOTIFICATION_EVENT_TYPES,
  loadLastSeen,
  useMarkAllSeen,
  useNotifications,
  type Notification,
  type NotificationEventType,
} from "@/lib/api/notifications";
import { useAuthStore } from "@/lib/auth/store";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_authenticated/activity")({
  component: ActivityPage,
});

// /activity — FE-API-008 live feed.
//
// Replaces the Sprint 3 sketched preview with the real /notifications
// endpoint. Filter chips drive the `event_types` query param; "Mark all
// seen" advances the localStorage cursor (same write the topbar bell
// uses, so the bell badge resets in lockstep).
function ActivityPage(): React.ReactElement {
  const tenantID = useAuthStore((s) => s.claims?.tenant_id);
  const [selected, setSelected] = React.useState<Set<NotificationEventType>>(
    new Set(),
  );
  const eventTypes = selected.size > 0 ? Array.from(selected) : undefined;
  const { data, isLoading, isError, refetch, isFetching } = useNotifications({
    limit: 100,
    event_types: eventTypes,
  });
  const lastSeenAt = React.useMemo(
    () => loadLastSeen(tenantID),
    // re-read when data refreshes so a new "Mark all seen" registers
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [tenantID, data],
  );
  const markAllSeen = useMarkAllSeen(tenantID);

  function toggleType(t: NotificationEventType): void {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(t)) next.delete(t);
      else next.add(t);
      return next;
    });
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Audit
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Activity
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          A live feed of what's happening across the workspace — pushes,
          deletes, scans, webhook deliveries.
        </p>
      </header>

      {/* Filter chip row + Mark-all action */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex flex-wrap gap-1.5">
          {NOTIFICATION_EVENT_TYPES.map((t) => {
            const active = selected.has(t);
            return (
              <button
                key={t}
                type="button"
                onClick={() => toggleType(t)}
                aria-pressed={active}
                className={cn(
                  "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                  active
                    ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
                    : "border-[var(--color-border-strong)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
                )}
              >
                {NOTIFICATION_EVENT_LABELS[t]}
              </button>
            );
          })}
          {selected.size > 0 ? (
            <button
              type="button"
              onClick={() => setSelected(new Set())}
              className="rounded-full border border-transparent px-2 py-1 text-xs text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
            >
              Clear
            </button>
          ) : null}
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => markAllSeen.mutate()}
          disabled={markAllSeen.isPending}
        >
          <CheckCircle2 className="size-3.5" />
          Mark all seen
        </Button>
      </div>

      {isError ? (
        <ErrorState
          title="Couldn't load notifications"
          description="The audit service didn't answer. Retry, or check the BFF logs."
          onRetry={() => void refetch()}
        />
      ) : (
        <Card>
          <CardHeader className="pb-2">
            <div className="flex items-center justify-between">
              <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                Recent events
              </CardDescription>
              {data ? (
                <span className="text-xs text-[var(--color-fg-muted)]">
                  {data.notifications.length}{" "}
                  {data.notifications.length === 1 ? "event" : "events"}
                </span>
              ) : null}
            </div>
          </CardHeader>
          <CardContent>
            {isLoading || (isFetching && !data) ? (
              <SkeletonRows />
            ) : !data || data.notifications.length === 0 ? (
              <EmptyState
                icon={<Inbox className="size-5" />}
                title={
                  selected.size > 0
                    ? "No events match this filter"
                    : "No recent activity"
                }
                description={
                  selected.size > 0
                    ? "Try a different event type — or clear the filter to see everything."
                    : "Push an image, run a scan, or wire a webhook to start populating this feed."
                }
              />
            ) : (
              <ol className="space-y-3">
                {data.notifications.map((n) => (
                  <ActivityRow
                    key={n.event_id}
                    n={n}
                    isUnread={
                      !lastSeenAt ||
                      Date.parse(n.occurred_at) > Date.parse(lastSeenAt)
                    }
                  />
                ))}
              </ol>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}

interface ActivityRowProps {
  n: Notification;
  isUnread: boolean;
}

function ActivityRow({ n, isUnread }: ActivityRowProps): React.ReactElement {
  const Icon = iconForEvent(n.event_type);
  const tone = toneForEvent(n.event_type);

  const inner = (
    <div className="flex items-start gap-3">
      <span
        aria-hidden
        className={cn(
          "mt-0.5 grid size-8 shrink-0 place-items-center rounded-md",
          tone === "success" &&
            "bg-[var(--color-success)]/10 text-[var(--color-success)]",
          tone === "warning" &&
            "bg-[var(--color-warning)]/10 text-[var(--color-warning)]",
          tone === "danger" &&
            "bg-[var(--color-danger)]/10 text-[var(--color-danger)]",
          tone === "accent" &&
            "bg-[var(--color-accent-subtle)] text-[var(--color-accent)]",
          tone === "neutral" &&
            "bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
        )}
      >
        <Icon className="size-4" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="text-sm font-medium text-[var(--color-fg)]">
            {n.title}
          </span>
          {isUnread ? (
            <span className="rounded-full bg-[var(--color-highlight)] px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider text-white">
              new
            </span>
          ) : null}
        </div>
        <div className="mt-0.5 text-xs text-[var(--color-fg-muted)]">
          {n.summary}
        </div>
        <div className="mt-1 text-[11px] text-[var(--color-fg-subtle)]">
          {n.actor_username || n.actor_id || "system"} ·{" "}
          <span title={formatAbsoluteDate(n.occurred_at)}>
            {formatRelativeDate(n.occurred_at)}
          </span>{" "}
          · <code className="font-mono">{n.event_type}</code>
        </div>
      </div>
    </div>
  );

  if (n.link) {
    return (
      <li>
        <Link
          to={n.link}
          className="block rounded-md p-2 hover:bg-[var(--color-surface-sunken)]"
        >
          {inner}
        </Link>
      </li>
    );
  }
  return <li className="p-2">{inner}</li>;
}

// iconForEvent picks the lucide glyph that matches each routing key. Keep
// in sync with the backend's NOTIFICATION_EVENT_TYPES allowlist.
function iconForEvent(eventType: string) {
  switch (eventType) {
    case "push.image":
      return ArrowUpCircle;
    case "push.failed":
      return ShieldAlert;
    case "delete.manifest":
    case "delete.tag":
      return Trash2;
    case "scan.completed":
      return ShieldCheck;
    case "scan.policy_blocked":
      return ShieldAlert;
    case "image.signed":
      return FileSignature;
    case "webhook.delivery_failed":
      return WebhookIcon;
    default:
      return ActivityIcon;
  }
}

function toneForEvent(eventType: string): "success" | "warning" | "danger" | "accent" | "neutral" {
  switch (eventType) {
    case "push.image":
    case "scan.completed":
      return "success";
    case "image.signed":
      return "accent";
    case "push.failed":
    case "scan.policy_blocked":
    case "webhook.delivery_failed":
      return "danger";
    case "delete.manifest":
    case "delete.tag":
      return "warning";
    default:
      return "neutral";
  }
}

function SkeletonRows(): React.ReactElement {
  return (
    <ol className="space-y-3">
      {Array.from({ length: 5 }).map((_, i) => (
        <li key={i} className="flex items-start gap-3 p-2">
          <Skeleton className="size-8 shrink-0 rounded-md" />
          <div className="flex-1 space-y-1.5">
            <Skeleton className="h-3 w-1/3" />
            <Skeleton className="h-3 w-2/3" />
          </div>
        </li>
      ))}
    </ol>
  );
}
