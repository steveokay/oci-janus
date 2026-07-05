// UI cleanup 2026-07-05 — Settings › Notifications tab.
//
// The per-category notification-preference matrix moved here out of the old
// Settings › Account tab. It's a preferences-hub concern ("which alerts reach
// me, on which channel"), so it belongs under Settings alongside the other
// workspace/housekeeping config — not on the personal Profile page.
//
// Backend + delivery-channel state is unchanged: the bell channel is live;
// email + webhook are shown but locked ("Wired in Phase 3+") until the
// FUT-019 Phase 3 delivery worker lands (tracked in futures.md).
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { toast } from "sonner";
import { Bell } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { ErrorState } from "@/components/ui/error-state";
import {
  useNotificationPreferences,
  useUpdateNotificationPreferences,
  type NotificationPreferenceRow,
} from "@/lib/api/notification-preferences";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_authenticated/settings/notifications")({
  component: NotificationsTab,
});

function NotificationsTab(): React.ReactElement {
  return (
    <div className="space-y-6">
      <NotificationsSection />
    </div>
  );
}

// ── Notifications section ────────────────────────────────────────────

function NotificationsSection(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useNotificationPreferences();
  const update = useUpdateNotificationPreferences();
  // UIR-4: track the single in-flight cell as "<category>:<channel>" so only
  // the toggled checkbox shows a pending/disabled state — the prior
  // `update.isPending` froze the entire 12-cell matrix on any one write.
  const [pendingCell, setPendingCell] = React.useState<string | null>(null);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load notification preferences"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  async function toggleChannel(
    row: NotificationPreferenceRow,
    channel: "bell" | "email" | "webhook",
    next: boolean,
  ): Promise<void> {
    if (!data) return;
    // Send the FULL matrix every time. The BFF UPSERTs each row; sending
    // only one would still work, but the full payload keeps the wire shape
    // uniform with the GET response + lets the server seed defaults for
    // rows the user has never touched.
    const patched = data.preferences.map((p) =>
      p.key === row.key
        ? {
            category: p.key,
            bell_enabled: channel === "bell" ? next : p.bell_enabled,
            email_enabled: channel === "email" ? next : p.email_enabled,
            webhook_enabled: channel === "webhook" ? next : p.webhook_enabled,
          }
        : {
            category: p.key,
            bell_enabled: p.bell_enabled,
            email_enabled: p.email_enabled,
            webhook_enabled: p.webhook_enabled,
          },
    );
    // Mark just this cell in-flight for the duration of the write.
    setPendingCell(`${row.key}:${channel}`);
    try {
      await update.mutateAsync({ preferences: patched });
      toast.success(`${row.label}: ${channel} ${next ? "enabled" : "disabled"}.`);
    } catch {
      toast.error("Couldn't save preferences. Try again, or check the BFF logs.");
    } finally {
      setPendingCell(null);
    }
  }

  return (
    <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]">
      <div className="flex items-center gap-2">
        <Bell className="size-4 text-[var(--color-fg-muted)]" />
        <h2 className="font-display text-lg font-medium">Notification categories</h2>
      </div>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
        Toggle which scheduled notifications you want delivered to which
        channels. Bell shows in the topbar feed; email and webhook deliver
        when those channels are wired (Phase 3+).
      </p>

      <div className="mt-4 overflow-hidden rounded-md border border-[var(--color-border)]">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-[35%]">Category</TableHead>
              <TableHead>Description</TableHead>
              <TableHead className="w-[80px] text-center">Bell</TableHead>
              <TableHead className="w-[80px] text-center">Email</TableHead>
              <TableHead className="w-[90px] text-center">Webhook</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading || !data ? (
              <SkeletonRows />
            ) : (
              data.preferences.map((row) => (
                <TableRow key={row.key}>
                  <TableCell>
                    <div className="flex flex-col gap-1">
                      <span className="font-medium text-[var(--color-fg)]">{row.label}</span>
                      <Badge tone="neutral" className="!self-start text-[10px]">
                        {row.shipped_in}
                      </Badge>
                    </div>
                  </TableCell>
                  <TableCell className="text-xs text-[var(--color-fg-muted)]">
                    {row.description}
                  </TableCell>
                  <ChannelToggleCell
                    enabled={row.bell_enabled}
                    pending={pendingCell === `${row.key}:bell`}
                    onChange={(v) => void toggleChannel(row, "bell", v)}
                  />
                  <ChannelToggleCell
                    enabled={row.email_enabled}
                    pending={pendingCell === `${row.key}:email`}
                    onChange={(v) => void toggleChannel(row, "email", v)}
                    hint="Wired in Phase 3+"
                  />
                  <ChannelToggleCell
                    enabled={row.webhook_enabled}
                    pending={pendingCell === `${row.key}:webhook`}
                    onChange={(v) => void toggleChannel(row, "webhook", v)}
                    hint="Wired in Phase 3+"
                  />
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </section>
  );
}

export function ChannelToggleCell({
  enabled,
  pending,
  onChange,
  hint,
}: {
  enabled: boolean;
  pending: boolean;
  onChange: (next: boolean) => void;
  hint?: string;
}): React.ReactElement {
  // REDESIGN-001 Phase 4.5 — channel-not-yet-shipped lockout.
  //
  // When a channel hasn't shipped (BFF silently drops Email/Webhook writes
  // until FUT-019 Phase 3 lands), `hint` is set. Previously we only surfaced
  // that via a tooltip on the live checkbox — the operator could still flip
  // it, see no error, and walk away believing alerts were wired. Now we
  // visibly disable the row and keep the hint as the explanation.
  const locked = Boolean(hint);
  const isDisabled = pending || locked;
  return (
    <TableCell className="text-center">
      <input
        type="checkbox"
        checked={enabled}
        onChange={(e) => onChange(e.target.checked)}
        disabled={isDisabled}
        aria-disabled={isDisabled}
        title={hint}
        data-locked={locked ? "true" : "false"}
        className={cn(
          "size-4 rounded border-[var(--color-border-strong)] accent-[var(--color-accent)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40",
          locked ? "cursor-not-allowed opacity-50" : "cursor-pointer",
        )}
      />
    </TableCell>
  );
}

function SkeletonRows(): React.ReactElement {
  return (
    <>
      {Array.from({ length: 4 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell><Skeleton className="h-4 w-32" /></TableCell>
          <TableCell><Skeleton className="h-3 w-72" /></TableCell>
          <TableCell className="text-center"><Skeleton className="mx-auto size-4 rounded" /></TableCell>
          <TableCell className="text-center"><Skeleton className="mx-auto size-4 rounded" /></TableCell>
          <TableCell className="text-center"><Skeleton className="mx-auto size-4 rounded" /></TableCell>
        </TableRow>
      ))}
    </>
  );
}
