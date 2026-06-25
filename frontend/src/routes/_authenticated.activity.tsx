import * as React from "react";
import {
  createFileRoute,
  Link,
  useNavigate,
  useSearch,
} from "@tanstack/react-router";
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
  Clock,
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
import { UserCell } from "@/components/users/user-cell";
import { useAuthStore } from "@/lib/auth/store";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import {
  PageSizeSelector,
  usePageSize,
} from "@/components/ui/page-size-selector";
import { cn } from "@/lib/utils";

// Lookup-set version of NOTIFICATION_EVENT_TYPES for O(1) membership checks
// when hydrating the chip selection from the URL. Tuple → Set conversion
// at module load is cheap and avoids re-allocating on every render.
const NOTIFICATION_EVENT_TYPE_SET: ReadonlySet<NotificationEventType> = new Set(
  NOTIFICATION_EVENT_TYPES,
);

// parseEventTypesParam — decode the comma-separated `event_types` URL
// search param into a Set of valid event types. Unknown values are
// dropped silently so a stale link from an older deploy doesn't blow
// up the page; we trust the chip row to remain the authoritative list.
function parseEventTypesParam(
  raw: string | undefined,
): Set<NotificationEventType> {
  if (!raw) return new Set();
  const parts = raw.split(",").map((s) => s.trim()).filter(Boolean);
  const out = new Set<NotificationEventType>();
  for (const p of parts) {
    if (NOTIFICATION_EVENT_TYPE_SET.has(p as NotificationEventType)) {
      out.add(p as NotificationEventType);
    }
  }
  return out;
}

export const Route = createFileRoute("/_authenticated/activity")({
  component: ActivityPage,
});

// S-MAINT-1 F3 — date-range chips for the activity feed.
//
// Why a chip row rather than a date picker: the audit table grows unbounded
// over time and operators almost always want one of "what happened today"
// vs "what happened this week" vs "everything". A fixed-set chip is one
// click and the URL state ("?range=7d") makes deep-links shareable.
//
// Default is "7d" — a typical work-week window keeps the feed lively
// without dragging in stale events on a quiet weekend. "all" is still
// reachable for forensic / audit purposes.
type DateRange = "24h" | "7d" | "30d" | "all";
const DEFAULT_RANGE: DateRange = "7d";

const RANGE_OPTIONS: ReadonlyArray<{
  value: DateRange;
  label: string;
  // Hover tooltip — used by screen readers + power users wanting to
  // confirm the boundary semantics ("Events since 7 days ago — at the
  // millisecond the page rendered").
  title: string;
}> = [
  { value: "24h", label: "Last 24h", title: "Events from the last 24 hours" },
  { value: "7d", label: "Last 7d", title: "Events from the last 7 days" },
  { value: "30d", label: "Last 30d", title: "Events from the last 30 days" },
  { value: "all", label: "All time", title: "Every event in the audit log" },
];

// rangeToSince converts a chip value into an ISO timestamp suitable for
// the BFF's `since` query param. The boundary is computed at call time
// (rather than module-load) so a long-lived tab refreshing the feed
// doesn't gradually drift its "last 24h" window into the past.
//
// "all" maps to undefined — `useNotifications` omits the param entirely,
// matching the backend's "no since filter" path.
function rangeToSince(range: DateRange): string | undefined {
  if (range === "all") return undefined;
  const now = Date.now();
  const ms: Record<Exclude<DateRange, "all">, number> = {
    "24h": 24 * 60 * 60 * 1000,
    "7d": 7 * 24 * 60 * 60 * 1000,
    "30d": 30 * 24 * 60 * 60 * 1000,
  };
  return new Date(now - ms[range]).toISOString();
}

// /activity — FE-API-008 live feed.
//
// Replaces the Sprint 3 sketched preview with the real /notifications
// endpoint. Filter chips drive the `event_types` query param; "Mark all
// seen" advances the localStorage cursor (same write the topbar bell
// uses, so the bell badge resets in lockstep).
function ActivityPage(): React.ReactElement {
  const tenantID = useAuthStore((s) => s.claims?.tenant_id);
  const navigate = useNavigate();
  // S-MAINT-1 F3: date-range chip selection lives in the URL search
  // params so a "?range=24h" deep-link reproduces the operator's view
  // exactly. The route doesn't declare a validateSearch schema for the
  // same reason as /api-keys/service-accounts — we set the param
  // imperatively via navigate({ search }) and read it via cast.
  const search = useSearch({ strict: false }) as Record<
    string,
    string | undefined
  >;
  const range = ((): DateRange => {
    const raw = search.range;
    if (raw === "24h" || raw === "7d" || raw === "30d" || raw === "all") {
      return raw;
    }
    return DEFAULT_RANGE;
  })();
  // Memoise `since` keyed on `range` so the ISO timestamp stays stable
  // across renders — without this, `Date.now()` ticks forward every render,
  // producing a fresh ISO string, which propagates into useNotifications'
  // queryKey. A constantly-changing key means React Query can't return
  // cached data — only the "all" path (since=undefined) is a stable key,
  // which is exactly why every other chip rendered empty before this
  // memo landed.
  const since = React.useMemo(() => rangeToSince(range), [range]);

  // DSGN-016 — chip selection hydrates from the URL `event_types` search
  // param so deep-links from the notifications-bell footer ("Failures
  // only") land with the right chips already pressed. We only consume
  // the param at mount time; subsequent chip toggles drive local state
  // only (we keep the URL clean to match the existing range-chip pattern
  // which similarly omits the default state from the URL).
  const initialEventTypes = React.useMemo(
    () => parseEventTypesParam(search.event_types),
    // Hydrate once on first render; later URL changes from the bell
    // shouldn't clobber an in-progress filter edit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );
  const [selected, setSelected] = React.useState<Set<NotificationEventType>>(
    initialEventTypes,
  );
  const eventTypes = selected.size > 0 ? Array.from(selected) : undefined;
  // S-MAINT-1 P5: persisted page size, "notifications" key.
  const [pageSize, setPageSize] = usePageSize("notifications");
  const { data, isLoading, isError, error, refetch, isFetching } = useNotifications({
    limit: pageSize,
    event_types: eventTypes,
    since,
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

  // setRange updates the URL search param so a refresh / share preserves
  // the choice. Default selection clears the param so the URL stays tidy
  // when the operator picks the implicit default ("7d").
  function setRange(next: DateRange): void {
    void navigate({
      to: "/activity",
      search: (prev) => ({
        ...prev,
        range: next === DEFAULT_RANGE ? undefined : next,
      }),
      replace: true,
    });
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Audit
        </p>
        {/* Icon mirrors the sidebar nav entry so the page identity reads */}
        {/* the same in the topbar and in the page header (consistent with */}
        {/* /helm's Ship icon + /security's ShieldCheck icon usage). */}
        <h1 className="font-display flex items-center gap-3 text-3xl font-medium tracking-tight">
          <ActivityIcon
            className="size-7 text-[var(--color-accent)]"
            aria-hidden
          />
          Activity
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          A live feed of what's happening across the workspace — pushes,
          deletes, scans, webhook deliveries.
        </p>
      </header>

      {/* S-MAINT-1 F3 — date-range chip row. Mounted above the event-
          type chips so the time window reads as the primary filter
          ("show me last 24h, then narrow by type") rather than the
          other way around. */}
      <div className="flex items-center gap-1.5">
        <Clock
          className="size-3.5 text-[var(--color-fg-subtle)]"
          aria-hidden
        />
        <span className="text-[10px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
          Range
        </span>
        <div
          role="group"
          aria-label="Filter activity by date range"
          className="flex flex-wrap gap-1.5"
        >
          {RANGE_OPTIONS.map((opt) => {
            const active = range === opt.value;
            return (
              <button
                key={opt.value}
                type="button"
                onClick={() => setRange(opt.value)}
                aria-pressed={active}
                title={opt.title}
                className={cn(
                  "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
                  active
                    ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
                    : "border-[var(--color-border-strong)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
                )}
              >
                {opt.label}
              </button>
            );
          })}
        </div>
      </div>

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
        <div className="flex items-center gap-3">
          <PageSizeSelector value={pageSize} onChange={setPageSize} />
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
      </div>

      {isError ? (
        <ErrorState
          title="Couldn't load notifications"
          description="The audit service didn't answer. Retry, or check the BFF logs."
          error={error}
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
                    : range !== "all"
                      ? `No activity in the ${
                          RANGE_OPTIONS.find((o) => o.value === range)?.label
                        }`
                      : "No recent activity"
                }
                description={
                  selected.size > 0
                    ? "Try a different event type — or clear the filter to see everything."
                    : range !== "all"
                      ? "Widen the range chip above to see older events, or push an image / run a scan / wire a webhook to populate the feed."
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
        <div className="mt-1 flex flex-wrap items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
          {/* REM-018-followup: prefer auth-side display_name + @username
              when present; UserCell falls back to @username only when the
              BFF couldn't join, and to the System shield when actor_id is
              the system/anonymous sentinel. */}
          <UserCell
            userId={n.actor_id}
            username={n.actor_username}
            displayName={n.actor_display_name}
            variant="inline"
          />
          <span>·</span>
          <span title={formatAbsoluteDate(n.occurred_at)}>
            {formatRelativeDate(n.occurred_at)}
          </span>
          <span>·</span>
          <code className="font-mono">{n.event_type}</code>
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
