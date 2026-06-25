import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { Bell, Settings as SettingsIcon, Shield } from "lucide-react";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  useNotificationPreferences,
  useUpdateNotificationPreferences,
  type NotificationPreferenceRow,
} from "@/lib/api/notification-preferences";

// FUT-019 Phase 1 — /settings hub (skeleton).
//
// Scope deliberately narrow: this page hosts ONLY things that don't have
// a home elsewhere in the dashboard. Profile already lives at /profile;
// theme toggling is one click away in the topbar. Putting them on tabs
// here would just create two paths to the same surface and confuse
// "which one is the source of truth?".
//
// Two tabs:
//   Notifications  — Phase 2 placeholder. Scheduled notifications + per-
//                    category opt-in matrix lands in FUT-019 Phase 2 + 3.
//   Security       — placeholder. MFA enrolment + active sessions land
//                    when Tier-1 #1 (MFA + session management) ships.
//
// Active tab is driven by the ?tab= search param so deep-links from
// notifications ("Update your preferences →") survive a page refresh.

type SettingsTab = "notifications" | "security";

const TABS: ReadonlyArray<SettingsTab> = ["notifications", "security"];

interface SettingsSearch {
  tab?: SettingsTab;
}

export const Route = createFileRoute("/_authenticated/settings")({
  validateSearch: (search: Record<string, unknown>): SettingsSearch => {
    const t = search.tab;
    return typeof t === "string" && (TABS as ReadonlyArray<string>).includes(t)
      ? { tab: t as SettingsTab }
      : {};
  },
  component: SettingsPage,
});

function SettingsPage(): React.ReactElement {
  const { tab } = Route.useSearch();
  const navigate = useNavigate();
  const activeTab: SettingsTab = tab ?? "notifications";

  // Sync state with the URL — clicking a tab writes ?tab=, and a direct
  // visit to /settings?tab=security lands on the right tab. We use
  // replace so a chain of tab clicks doesn't pollute browser history
  // (mirrors the proxy-cache + tags filter pattern). Default tab
  // (notifications) is the absent search param so the URL stays clean.
  function setTab(next: string): void {
    void navigate({
      to: "/settings",
      search: next === "notifications" ? {} : { tab: next as SettingsTab },
      replace: true,
    });
  }

  return (
    <div className="space-y-6 p-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Account
        </p>
        <h1 className="flex items-center gap-2 font-display text-3xl font-medium tracking-tight">
          <SettingsIcon className="size-6" /> Settings
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Configure your notification subscriptions and account security.
          Profile and theme live in their own places — this hub is for the
          settings without a home elsewhere.
        </p>
      </header>

      <Tabs value={activeTab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="notifications">
            <Bell className="size-3.5" /> Notifications
          </TabsTrigger>
          <TabsTrigger value="security">
            <Shield className="size-3.5" /> Security
          </TabsTrigger>
        </TabsList>

        <TabsContent value="notifications">
          <NotificationsTab />
        </TabsContent>
        <TabsContent value="security">
          <SecurityTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

// ── Notifications tab ────────────────────────────────────────────────

function NotificationsTab(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useNotificationPreferences();
  const update = useUpdateNotificationPreferences();

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
    // Send the FULL matrix every time. The BFF UPSERTs each row;
    // sending only one would still work, but the full payload keeps
    // the wire shape uniform with the GET response + lets the
    // server seed defaults for rows the user has never touched.
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
    try {
      await update.mutateAsync({ preferences: patched });
      toast.success(`${row.label}: ${channel} ${next ? "enabled" : "disabled"}.`);
    } catch (_e) {
      toast.error("Couldn't save preferences. Try again, or check the BFF logs.");
    }
  }

  return (
    <div className="mt-6 space-y-4">
      <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]">
        <h2 className="font-display text-lg font-medium">Notification categories</h2>
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
                      pending={update.isPending}
                      onChange={(v) => void toggleChannel(row, "bell", v)}
                    />
                    <ChannelToggleCell
                      enabled={row.email_enabled}
                      pending={update.isPending}
                      onChange={(v) => void toggleChannel(row, "email", v)}
                      hint="Wired in Phase 3+"
                    />
                    <ChannelToggleCell
                      enabled={row.webhook_enabled}
                      pending={update.isPending}
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
    </div>
  );
}

function ChannelToggleCell({
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
  return (
    <TableCell className="text-center">
      <input
        type="checkbox"
        checked={enabled}
        onChange={(e) => onChange(e.target.checked)}
        disabled={pending}
        title={hint}
        className="size-4 cursor-pointer rounded border-[var(--color-border-strong)] accent-[var(--color-accent)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40"
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

// ── Security tab (Tier 1 #1 placeholder) ────────────────────────────

function SecurityTab(): React.ReactElement {
  return (
    <ComingSoon
      icon={<Shield className="size-5" />}
      title="MFA + active sessions"
      futureID="Tier 1 #1 (MFA + session management)"
      body="TOTP enrolment with QR code + 8 backup codes, optional WebAuthn /
        hardware key support, active-session list with per-row revoke, and a
        workspace policy toggle to require MFA for every member. Lives here so
        operators don't have to context-switch to a separate /security route
        for personal account hardening."
    />
  );
}

function ComingSoon({
  icon,
  title,
  futureID,
  body,
}: {
  icon: React.ReactNode;
  title: string;
  futureID: string;
  body: string;
}): React.ReactElement {
  return (
    <section className="mt-6 rounded-lg border border-dashed border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] p-6 text-center">
      <div className="mx-auto inline-flex size-10 items-center justify-center rounded-md bg-[var(--color-surface)] text-[var(--color-fg-muted)]">
        {icon}
      </div>
      <h2 className="mt-3 font-display text-lg font-medium">{title}</h2>
      <p className="mx-auto mt-2 max-w-prose text-sm text-[var(--color-fg-muted)]">
        {body}
      </p>
      <p className="mt-3 text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
        Tracked under {futureID}
      </p>
    </section>
  );
}
