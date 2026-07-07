import * as React from "react";
import { Link } from "@tanstack/react-router";
import { Mail, MailX, ArrowRight } from "lucide-react";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  useEmailDeliveries,
  type EmailDelivery,
} from "@/lib/api/email-deliveries";
import { useEmailTransport } from "@/lib/api/email-transport";
import { formatRelativeDate } from "@/lib/format";

// EmailActivityMenu — topbar widget surfacing the current user's recent
// email delivery log (what got emailed to them + delivery status). Mirrors
// NotificationsBell's Radix Popover structure so the two topbar affordances
// feel identical: a ghost icon trigger + a ~360px scrollable panel with a
// header, a list, and a footer link.
//
// It sits immediately BEFORE the bell so the topbar reads: mail → bell →
// theme. Deliveries come from useEmailDeliveries() (per-user, server-scoped);
// the panel never exposes anyone else's mail.
//
// Popover (not DropdownMenu) is the right primitive for the same reason the
// bell uses it: the panel is a scrollable feed of rows + a footer link, not
// a list of single-action menu items. Menu semantics (role="menu", roving
// tabindex, typeahead) fight rich content and break normal Tab navigation.

// STATUS_TONE maps a delivery status to a Badge tone: Sent = success,
// Failed = danger, Pending = muted/neutral. Labels are Title-cased for the
// pill copy.
const STATUS_TONE: Record<
  EmailDelivery["status"],
  { tone: "success" | "danger" | "neutral"; label: string }
> = {
  sent: { tone: "success", label: "Sent" },
  failed: { tone: "danger", label: "Failed" },
  pending: { tone: "neutral", label: "Pending" },
};

// Prefer sent_at for delivered mail (the moment it actually left), otherwise
// fall back to created_at (enqueue time) for pending/failed rows.
function deliveryTimestamp(d: EmailDelivery): string | undefined {
  return d.sent_at ?? d.created_at;
}

export function EmailActivityMenu(): React.ReactElement {
  const [open, setOpen] = React.useState(false);
  // isError + refetch drive the panel's cold-cache error branch — mirrors
  // the bell so a failed fetch surfaces a retry instead of an eternal
  // "Loading…".
  const { data, isError, refetch } = useEmailDeliveries();
  // Transport-enabled hint (optional nicety). This endpoint is admin-only
  // server-side, so it 403s for non-admins — we treat ANY error as "unknown"
  // and simply fall back to the plain "No emails yet" empty state. Only an
  // explicit `enabled === false` from a successful (admin) read upgrades the
  // empty copy to "Email isn't set up yet".
  const transport = useEmailTransport();
  const transportOff =
    !transport.isError && transport.data?.enabled === false;

  // Close the panel — passed to the footer link so a click dismisses the
  // popover the same way selecting a DropdownMenu.Item used to.
  const close = React.useCallback(() => setOpen(false), []);

  const deliveries = data?.deliveries ?? [];

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Email activity">
          <Mail className="size-4" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        sideOffset={6}
        aria-label="Email activity"
        className="w-[360px] overflow-hidden"
      >
        {/* Header */}
        <div className="flex items-center justify-between border-b border-[var(--color-border)] px-3 py-2">
          <span className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Email activity
          </span>
        </div>

        {/* List */}
        <div className="max-h-[400px] overflow-y-auto">
          {isError && !data ? (
            // Cold-cache fetch failure — surface a retry rather than an
            // eternal "Loading…". Stale data keeps showing (staleTime lets
            // the query self-heal), so this only fires with nothing cached.
            <div className="flex flex-col items-center justify-center gap-2 px-3 py-8 text-center">
              <div className="text-xs text-[var(--color-fg-muted)]">
                Couldn&apos;t load email activity
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
          ) : deliveries.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 px-3 py-8 text-center">
              <MailX className="size-5 text-[var(--color-fg-subtle)]" />
              <div className="text-sm font-medium text-[var(--color-fg)]">
                {transportOff ? "Email isn't set up yet" : "No emails yet"}
              </div>
              <div className="text-xs text-[var(--color-fg-muted)]">
                {transportOff
                  ? "Configure a transport to start delivering email."
                  : "Emails sent to you will show up here."}
              </div>
            </div>
          ) : (
            <ul>
              {deliveries.map((d) => (
                <EmailDeliveryRow key={d.id} d={d} />
              ))}
            </ul>
          )}
        </div>

        {/* Footer — single affordance jumping to the notification settings
            page where the email transport + per-channel toggles live. Closes
            the popover on click (Popover has no menu-item auto-dismiss). */}
        <div className="border-t border-[var(--color-border)]">
          <Link
            to="/settings/notifications"
            onClick={close}
            className="flex items-center justify-center gap-1.5 px-3 py-2 text-xs text-[var(--color-fg-muted)] outline-none hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)] focus-visible:bg-[var(--color-surface-sunken)] focus-visible:text-[var(--color-fg)]"
          >
            Manage email settings
            <ArrowRight className="size-3" />
          </Link>
        </div>
      </PopoverContent>
    </Popover>
  );
}

function EmailDeliveryRow({ d }: { d: EmailDelivery }): React.ReactElement {
  const status = STATUS_TONE[d.status];
  return (
    <li>
      <div className="flex items-start gap-3 px-3 py-2.5">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            {/* Category label — the notification class that triggered the
                mail (e.g. "scan.policy_blocked"). Rendered verbatim; the
                backend already emits human-readable category strings. */}
            <span className="truncate text-[11px] font-medium uppercase tracking-[0.12em] text-[var(--color-fg-subtle)]">
              {d.category}
            </span>
            <Badge tone={status.tone} className="!py-0 shrink-0">
              {status.label}
            </Badge>
          </div>
          <div className="truncate text-sm font-medium text-[var(--color-fg)]">
            {d.subject}
          </div>
          <div className="truncate text-xs text-[var(--color-fg-muted)]">
            {d.to_address}
          </div>
          {/* Surface the failure reason on failed rows so the operator can
              see WHY without hunting through logs. last_error is a redacted
              summary (no secrets) per the backend contract. */}
          {d.status === "failed" && d.last_error ? (
            <div className="mt-0.5 truncate text-[11px] text-[var(--color-danger)]">
              {d.last_error}
            </div>
          ) : null}
          <div className="mt-0.5 text-[11px] text-[var(--color-fg-subtle)]">
            {formatRelativeDate(deliveryTimestamp(d))}
          </div>
        </div>
      </div>
    </li>
  );
}
