import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Bell, Settings as SettingsIcon, Shield } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

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

// ── Notifications tab (Phase 2 placeholder) ──────────────────────────

function NotificationsTab(): React.ReactElement {
  return (
    <ComingSoon
      icon={<Bell className="size-5" />}
      title="Scheduled notifications"
      futureID="FUT-019 Phase 2 + 3"
      body="A per-category opt-in matrix for policy- and calendar-driven nudges:
        scanner adapter freshness, invite token expiry warnings, mTLS / TLS
        certificate expiry, password rotation reminders, retention dry-run
        summaries, failed-login bursts, plan quota thresholds. Bell + email +
        webhook delivery channels per category."
    />
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
