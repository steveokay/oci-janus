import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  Bell,
  CheckCircle2,
  Inbox,
  ArrowRight,
} from "lucide-react";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  countUnread,
  loadLastSeen,
  useMarkAllSeen,
  useNotifications,
  type Notification,
} from "@/lib/api/notifications";
import { UserCell } from "@/components/users/user-cell";
import { useAuthStore } from "@/lib/auth/store";
import { formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// NotificationsBell — topbar widget surfacing recent tenant events.
//
// Polls /notifications every 60s (see hook). Unread count is derived from a
// localStorage `last_seen_at` cursor, so the bell instantly reflects "marked
// as read" without a server write. "Mark all seen" bumps the cursor + fires
// a cache invalidation so the badge resets.
//
// Panel lists the most recent 10 events; "View all" links to /activity
// for the full filterable feed.
//
// The floating panel is a Radix Popover, NOT a DropdownMenu: the panel is a
// scrollable feed of links + buttons, not a list of single-action menu
// items. Menu semantics (role="menu", roving tabindex, typeahead) fight the
// rich content and break normal Tab navigation, so Popover is the correct
// primitive. Outside-click + ESC dismissal come from Radix; we control
// `open` so notification / footer links can close the panel on navigation.
export function NotificationsBell(): React.ReactElement {
  const tenantID = useAuthStore((s) => s.claims?.tenant_id);
  const [open, setOpen] = React.useState(false);
  // isError + refetch drive the panel's error branch — the old `!data`-only
  // check left a failed fetch stuck on "Loading…" forever.
  const { data, isError, refetch } = useNotifications({ limit: 10 });
  // The "last seen" cursor is a free localStorage read. We intentionally
  // re-read whenever the notifications page updates so a "Mark all seen"
  // anywhere in the app refreshes the badge here. `data` looks "unused"
  // to the linter but is the signal that triggers the re-read.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const lastSeenAt = React.useMemo(() => loadLastSeen(tenantID), [tenantID, data]);
  const markAllSeen = useMarkAllSeen(tenantID);
  const unread = countUnread(data, lastSeenAt);
  // Close the panel — passed to every navigation link so a click dismisses
  // the popover the same way selecting a DropdownMenu.Item used to.
  const close = React.useCallback(() => setOpen(false), []);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          aria-label={
            unread > 0
              ? `Notifications — ${unread} unread`
              : "Notifications"
          }
          className="relative"
        >
          <Bell className="size-4" />
          {unread > 0 ? (
            <span
              aria-hidden
              className={cn(
                "absolute -right-0.5 -top-0.5 grid min-w-[16px] place-items-center rounded-full",
                "bg-[var(--color-highlight)] px-1 text-[10px] font-semibold leading-[16px] text-[var(--color-highlight-fg)]",
              )}
            >
              {unread > 99 ? "99+" : unread}
            </span>
          ) : null}
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        sideOffset={6}
        aria-label="Notifications"
        className="w-[360px] overflow-hidden"
      >
          {/* Header */}
          <div className="flex items-center justify-between border-b border-[var(--color-border)] px-3 py-2">
            <div className="flex items-center gap-2">
              <span className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                Notifications
              </span>
              {unread > 0 ? (
                <Badge tone="warning" className="!py-0">
                  {unread} unread
                </Badge>
              ) : null}
            </div>
            <button
              type="button"
              onClick={() => markAllSeen.mutate()}
              disabled={unread === 0 || markAllSeen.isPending}
              className={cn(
                "inline-flex items-center gap-1 rounded-sm px-1.5 py-0.5 text-[11px]",
                unread === 0
                  ? "cursor-not-allowed text-[var(--color-fg-subtle)]"
                  : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)]",
              )}
            >
              <CheckCircle2 className="size-3" />
              Mark all seen
            </button>
          </div>

          {/* List */}
          <div className="max-h-[400px] overflow-y-auto">
            {isError && !data ? (
              // Fetch failed with nothing cached — surface it instead of an
              // eternal "Loading…". If stale data exists we keep showing it
              // (the 60s poll will self-heal), so this branch only fires on
              // a cold-cache failure.
              <div className="flex flex-col items-center justify-center gap-2 px-3 py-8 text-center">
                <div className="text-xs text-[var(--color-fg-muted)]">
                  Couldn&apos;t load notifications
                </div>
                <button
                  type="button"
                  onClick={() => void refetch()}
                  className="rounded-sm px-1.5 py-0.5 text-[11px] text-[var(--color-fg-muted)] underline hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)]"
                >
                  Retry
                </button>
              </div>
            ) : !data ? (
              <div className="grid place-items-center px-3 py-8 text-xs text-[var(--color-fg-muted)]">
                Loading…
              </div>
            ) : data.notifications.length === 0 ? (
              <div className="flex flex-col items-center justify-center gap-2 px-3 py-8 text-center">
                <Inbox className="size-5 text-[var(--color-fg-subtle)]" />
                <div className="text-sm font-medium text-[var(--color-fg)]">
                  All caught up
                </div>
                <div className="text-xs text-[var(--color-fg-muted)]">
                  Recent tenant events will surface here.
                </div>
              </div>
            ) : (
              <ul>
                {data.notifications.map((n) => (
                  <NotificationRow
                    key={n.event_id}
                    n={n}
                    isUnread={
                      !lastSeenAt || Date.parse(n.occurred_at) > Date.parse(lastSeenAt)
                    }
                    onNavigate={close}
                  />
                ))}
              </ul>
            )}
          </div>

          {/* Footer — two affordances side by side. "See all activity" is
              the no-filter jump; "Failures only" pre-filters /activity to
              the three failure-class event types. The /activity route
              hydrates its chip state from the `event_types` search param
              (comma-separated routing keys) so the page lands with those
              chips pressed. Both links close the popover on click via the
              `close` callback (Popover has no menu-item auto-dismiss). */}
          <div className="grid grid-cols-2 border-t border-[var(--color-border)]">
            <Link
              to="/activity"
              onClick={close}
              className="flex items-center justify-center gap-1.5 px-3 py-2 text-xs text-[var(--color-fg-muted)] outline-none hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)] focus-visible:bg-[var(--color-surface-sunken)] focus-visible:text-[var(--color-fg)]"
            >
              See all activity
              <ArrowRight className="size-3" />
            </Link>
            <Link
              to="/activity"
              onClick={close}
              search={
                {
                  event_types:
                    "push.failed,scan.policy_blocked,webhook.delivery_failed",
                } as Record<string, string>
              }
              className="flex items-center justify-center gap-1.5 border-l border-[var(--color-border)] px-3 py-2 text-xs text-[var(--color-fg-muted)] outline-none hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)] focus-visible:bg-[var(--color-surface-sunken)] focus-visible:text-[var(--color-fg)]"
            >
              Failures only
              <ArrowRight className="size-3" />
            </Link>
          </div>
        </PopoverContent>
    </Popover>
  );
}

function NotificationRow({
  n,
  isUnread,
  onNavigate,
}: {
  n: Notification;
  isUnread: boolean;
  // Closes the popover when a linked row is clicked — parity with the old
  // DropdownMenu.Item auto-dismiss.
  onNavigate: () => void;
}): React.ReactElement {
  // Row renders as an anchor when there's a link, plain div when not.
  // Each click navigates to the synthesized deep-link from the backend
  // (e.g. /repositories/dev/alpine/tags/3.20).
  const inner = (
    <div className="flex items-start gap-3 px-3 py-2.5">
      <span
        aria-hidden
        className={cn(
          "mt-1.5 size-1.5 shrink-0 rounded-full",
          isUnread ? "bg-[var(--color-highlight)]" : "bg-[var(--color-border-strong)]",
        )}
      />
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium text-[var(--color-fg)]">
          {n.title}
        </div>
        <div className="truncate text-xs text-[var(--color-fg-muted)]">
          {n.summary}
        </div>
        <div className="mt-0.5 flex items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
          {/* REM-018-followup: actor cell renders display_name + @username
              when the BFF was able to join auth; otherwise UserCell falls
              back to @username or the System placeholder. */}
          <UserCell
            userId={n.actor_id}
            username={n.actor_username}
            displayName={n.actor_display_name}
            variant="inline"
          />
          <span>·</span>
          <span>{formatRelativeDate(n.occurred_at)}</span>
        </div>
      </div>
    </div>
  );
  if (n.link) {
    return (
      <li>
        <Link
          to={n.link}
          onClick={onNavigate}
          className="block hover:bg-[var(--color-surface-sunken)]"
        >
          {inner}
        </Link>
      </li>
    );
  }
  return <li>{inner}</li>;
}
