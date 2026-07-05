// UI cleanup 2026-07-05 — /profile is a first-class page again.
//
// The unified Settings IA (REDESIGN-001 Phase 4.2.b) had folded personal
// account state into a Settings › Account tab and left /profile as a bare
// redirect. This split reverses that for the *personal* surface only:
// Profile is now its own top-level sidebar item holding everything a user
// tweaks about themselves — identity, password, personal API keys,
// two-factor auth, and active sessions. Workspace/housekeeping/notification
// config stays under Settings.
//
// Notification preferences deliberately do NOT live here — they moved to
// Settings › Notifications, because "which alerts reach me" is a
// preferences-hub concern, not identity/hardening.
import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Compass, UserRound } from "lucide-react";
import { IdentityCard } from "@/components/profile/identity-card";
import { ChangePasswordDialog } from "@/components/profile/change-password-dialog";
import { MfaCard } from "@/components/profile/mfa-card";
import { MfaEnrollDialog } from "@/components/profile/mfa-enroll-dialog";
import { MfaDisableDialog } from "@/components/profile/mfa-disable-dialog";
import { MfaRegenerateDialog } from "@/components/profile/mfa-regenerate-dialog";
import { ApiKeysSection } from "@/components/profile/api-keys-section";
import { SessionsCard } from "@/components/profile/sessions-card";

export const Route = createFileRoute("/_authenticated/profile")({
  component: ProfilePage,
});

function ProfilePage(): React.ReactElement {
  // Dialogs are owned by the page so the cards stay dumb emitters (matches
  // the pattern the old Account tab used, so user behaviour is unchanged).
  const [passwordOpen, setPasswordOpen] = React.useState(false);
  const [mfaEnrollOpen, setMfaEnrollOpen] = React.useState(false);
  const [mfaDisableOpen, setMfaDisableOpen] = React.useState(false);
  const [mfaRegenerateOpen, setMfaRegenerateOpen] = React.useState(false);

  return (
    <div className="space-y-6 p-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Account
        </p>
        <h1 className="flex items-center gap-2 font-display text-3xl font-medium tracking-tight">
          <UserRound className="size-6" /> Profile
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Everything about you — identity, sign-in security, and the devices
          and keys that can act as you.
        </p>
      </header>

      {/* Identity + password. IdentityCard inline-edits display_name + email
          and emits onChangePassword to open the dialog below. */}
      <IdentityCard onChangePassword={() => setPasswordOpen(true)} />

      {/* Personal API keys — same component the /api-keys hub uses (single
          source of truth). Workspace-shared keys (service accounts) live
          under /api-keys/service-accounts. */}
      <ApiKeysSection />

      {/* Two-factor authentication. Disable + regenerate-backup-codes each
          open their own re-auth-gated dialog. */}
      <MfaCard
        onEnroll={() => setMfaEnrollOpen(true)}
        onDisable={() => setMfaDisableOpen(true)}
        onRegenerate={() => setMfaRegenerateOpen(true)}
      />

      {/* Active sessions — signed-in devices with per-row revoke + "sign out
          all others". Part of the personal-hardening cluster below MFA. */}
      <SessionsCard />

      {/* Replay-onboarding footer stays at the bottom (its long-standing spot)
          — a low-emphasis way back into the first-run wizard for returning
          users, not a primary Profile concern. */}
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
  // Navigate to the wizard route; the wizard owns its own dismiss/complete
  // behaviour, so there's nothing else to do here. Re-running while the
  // onboarding_complete flag is true is harmless (idempotent BE endpoint).
  return (
    <section className="flex items-center justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-5 py-3 text-sm text-[var(--color-fg-muted)]">
      <div className="flex items-center gap-2">
        <Compass className="size-4" />
        <span>New here, or just want a refresher?</span>
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
