import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Bell, Monitor, Settings as SettingsIcon, Shield, User } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { IdentityCard } from "@/components/profile/identity-card";
import { ApiKeysSection } from "@/components/profile/api-keys-section";
import { ChangePasswordDialog } from "@/components/profile/change-password-dialog";
import { useTheme, type Theme } from "@/lib/theme";

// FUT-019 Phase 1 — /settings hub (skeleton).
//
// Four tabs:
//   Profile        — identity + password + API keys (re-renders the existing
//                    /profile content so deep-links keep working AND the
//                    settings hub is the canonical surface).
//   Display        — theme picker (light / dark / system). Hooks into the
//                    existing useTheme + localStorage round-trip.
//   Notifications  — Phase 2 placeholder. Scheduled notifications + per-
//                    category opt-in matrix lands in FUT-019 Phase 2 + 3.
//   Security       — placeholder. MFA enrolment + active sessions land
//                    when Tier-1 #1 (MFA + session management) ships.
//
// Active tab is driven by the ?tab= search param so deep-links from
// notifications ("Update your preferences →") survive a page refresh.

type SettingsTab = "profile" | "display" | "notifications" | "security";

const TABS: ReadonlyArray<SettingsTab> = [
  "profile",
  "display",
  "notifications",
  "security",
];

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
  const activeTab: SettingsTab = tab ?? "profile";

  // Sync state with the URL — clicking a tab writes ?tab=, and a
  // direct visit to /settings?tab=display lands on the right tab. We
  // use replace so a chain of tab clicks doesn't pollute browser
  // history (mirrors the proxy-cache + tags filter pattern).
  function setTab(next: string): void {
    void navigate({
      to: "/settings",
      search: next === "profile" ? {} : { tab: next as SettingsTab },
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
          Manage your profile, display preferences, notification subscriptions,
          and security. Changes apply to your own account.
        </p>
      </header>

      <Tabs value={activeTab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="profile">
            <User className="size-3.5" /> Profile
          </TabsTrigger>
          <TabsTrigger value="display">
            <Monitor className="size-3.5" /> Display
          </TabsTrigger>
          <TabsTrigger value="notifications">
            <Bell className="size-3.5" /> Notifications
          </TabsTrigger>
          <TabsTrigger value="security">
            <Shield className="size-3.5" /> Security
          </TabsTrigger>
        </TabsList>

        <TabsContent value="profile">
          <ProfileTab />
        </TabsContent>
        <TabsContent value="display">
          <DisplayTab />
        </TabsContent>
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

// ── Profile tab ──────────────────────────────────────────────────────

function ProfileTab(): React.ReactElement {
  const [passwordOpen, setPasswordOpen] = React.useState(false);
  return (
    <div className="mt-6 space-y-6">
      <IdentityCard onChangePassword={() => setPasswordOpen(true)} />
      <ApiKeysSection />
      <ChangePasswordDialog
        open={passwordOpen}
        onOpenChange={setPasswordOpen}
      />
    </div>
  );
}

// ── Display tab ──────────────────────────────────────────────────────

function DisplayTab(): React.ReactElement {
  const { theme, setTheme } = useTheme();
  return (
    <div className="mt-6 space-y-4">
      <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]">
        <h2 className="font-display text-lg font-medium">Theme</h2>
        <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
          Pick a fixed theme or follow your operating system's preference.
          The choice is saved in this browser and doesn't sync across devices.
        </p>
        <div className="mt-4 grid gap-3 sm:grid-cols-3">
          {(["light", "dark", "system"] as Theme[]).map((value) => (
            <ThemeOption
              key={value}
              value={value}
              active={theme === value}
              onSelect={() => setTheme(value)}
            />
          ))}
        </div>
      </section>
    </div>
  );
}

function ThemeOption({
  value,
  active,
  onSelect,
}: {
  value: Theme;
  active: boolean;
  onSelect: () => void;
}): React.ReactElement {
  const label = value === "light" ? "Light" : value === "dark" ? "Dark" : "System";
  const hint =
    value === "system" ? "Follows OS preference" : `Always ${value}`;
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={active}
      className={`flex flex-col items-start gap-1 rounded-md border px-3 py-2.5 text-left text-sm transition ${
        active
          ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
          : "border-[var(--color-border)] bg-[var(--color-surface-sunken)] text-[var(--color-fg)] hover:border-[var(--color-border-strong)]"
      }`}
    >
      <span className="font-medium">{label}</span>
      <span className="text-xs text-[var(--color-fg-muted)]">{hint}</span>
    </button>
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
