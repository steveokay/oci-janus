// REDESIGN-001 Phase 4.2.b — Settings › Account tab.
//
// This is where the bootstrap admin AND every regular user manages personal
// state: identity, password, notification preferences, API keys, and the
// MFA / active-sessions placeholder (futures.md Tier 1 #1). It absorbs the
// previous /profile page (now a redirect) and the FUT-019 /settings
// Notifications + Security tab content.
//
// Why one tab instead of four sub-pages: profile/password/keys/notifications
// are all "things you tweak about yourself" — a single scroll keeps the
// audience focused. Workspace/Platform tabs are where role-gated config
// lives; those split into their own tabs (4.2.c / 4.2.d).
import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { Bell, Compass } from "lucide-react";
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
import { IdentityCard } from "@/components/profile/identity-card";
import { ChangePasswordDialog } from "@/components/profile/change-password-dialog";
import { MfaCard } from "@/components/profile/mfa-card";
import { MfaEnrollDialog } from "@/components/profile/mfa-enroll-dialog";
import { MfaDisableDialog } from "@/components/profile/mfa-disable-dialog";
import { MfaRegenerateDialog } from "@/components/profile/mfa-regenerate-dialog";
import { ApiKeysSection } from "@/components/profile/api-keys-section";
import {
  useNotificationPreferences,
  useUpdateNotificationPreferences,
  type NotificationPreferenceRow,
} from "@/lib/api/notification-preferences";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_authenticated/settings/account")({
  component: AccountTab,
});

function AccountTab(): React.ReactElement {
  // Password dialog is owned by the route so IdentityCard can stay a dumb
  // component (it just emits onChangePassword). Matches the pattern used by
  // the old /profile page so the user behaviour is unchanged.
  const [passwordOpen, setPasswordOpen] = React.useState(false);
  // MFA dialog state, owned here alongside the password dialog so MfaCard
  // stays a dumb emitter. Three independent dialogs — enroll, disable,
  // regenerate-backup-codes — each toggled by its own boolean so opening one
  // never disturbs the others' state.
  const [mfaEnrollOpen, setMfaEnrollOpen] = React.useState(false);
  const [mfaDisableOpen, setMfaDisableOpen] = React.useState(false);
  const [mfaRegenerateOpen, setMfaRegenerateOpen] = React.useState(false);

  return (
    <div className="space-y-6">
      {/* Profile + password. The IdentityCard does inline-edit on
          display_name + email and exposes a "Change password" action that
          opens the dialog below. */}
      <IdentityCard onChangePassword={() => setPasswordOpen(true)} />

      {/* Personal API keys. Same component the /api-keys hub uses — single
          source of truth for the personal-key UX. Workspace-shared keys
          (service accounts) live under /api-keys/service-accounts. */}
      <ApiKeysSection />

      {/* Notification preferences moved out of the previous /settings tab.
          Same UX, just lives inside Account now so personal preferences are
          colocated with profile. */}
      <NotificationsSection />

      {/* MFA card. Personal-account hardening; was the old Security tab
          placeholder. Disable + regenerate-backup-codes both open their own
          re-auth-gated dialogs below. */}
      <MfaCard
        onEnroll={() => setMfaEnrollOpen(true)}
        onDisable={() => setMfaDisableOpen(true)}
        onRegenerate={() => setMfaRegenerateOpen(true)}
      />

      {/* REDESIGN-001 Phase 4.3 §3 — replay onboarding link. The first-run
          wizard auto-shows on the dashboard for users with
          `onboarding_complete === false`, but once dismissed (Done / Skip)
          the BE flips that flag and the wizard never re-triggers. This
          footer row gives users a way back in from Settings — matches the
          redesign plan's "Reachable from Settings > Help even after
          dismissal" requirement. Not gated on `onboarding_complete`:
          even users who finished should be allowed to replay. */}
      <ReplayOnboardingFooter />

      <ChangePasswordDialog open={passwordOpen} onOpenChange={setPasswordOpen} />
      <MfaEnrollDialog open={mfaEnrollOpen} onOpenChange={setMfaEnrollOpen} />
      <MfaDisableDialog open={mfaDisableOpen} onOpenChange={setMfaDisableOpen} />
      <MfaRegenerateDialog
        open={mfaRegenerateOpen}
        onOpenChange={setMfaRegenerateOpen}
      />
    </div>
  );
}

// ── Replay onboarding footer (REDESIGN-001 Phase 4.3 §3) ────────────

function ReplayOnboardingFooter(): React.ReactElement {
  const navigate = useNavigate();
  // Single click handler — navigate to the wizard route. The wizard owns its
  // own dismiss/complete behaviour (calls useCompleteOnboarding on Done/Skip),
  // so there's nothing else to do here. We don't pre-clear the flag because
  // re-running the wizard while the flag is `true` is harmless — the BE
  // endpoint is idempotent and the FE re-checks per session.
  return (
    <section className="flex items-center justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-5 py-3 text-sm text-[var(--color-fg-muted)]">
      <div className="flex items-center gap-2">
        <Compass className="size-4" />
        <span>
          New here, or just want a refresher?
        </span>
      </div>
      <button
        type="button"
        onClick={() => void navigate({ to: "/getting-started" })}
        className="font-medium text-[var(--color-accent)] underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40 rounded"
      >
        Replay onboarding tour
      </button>
    </section>
  );
}

// ── Notifications section ────────────────────────────────────────────

function NotificationsSection(): React.ReactElement {
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
    try {
      await update.mutateAsync({ preferences: patched });
      toast.success(`${row.label}: ${channel} ${next ? "enabled" : "disabled"}.`);
    } catch {
      toast.error("Couldn't save preferences. Try again, or check the BFF logs.");
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
          locked
            ? "cursor-not-allowed opacity-50"
            : "cursor-pointer",
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
