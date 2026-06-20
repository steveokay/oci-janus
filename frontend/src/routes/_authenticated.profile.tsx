import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { IdentityCard } from "@/components/profile/identity-card";
import { ChangePasswordDialog } from "@/components/profile/change-password-dialog";
import { ApiKeysSection } from "@/components/profile/api-keys-section";

// Sprint 7A — real wiring of the profile surface against FE-API-011/012/013
// (`/users/me` GET + PATCH + password change) plus the existing /apikeys
// CRUD. Replaces the Sprint 0 placeholder that just shipped a ComingSoon.
export const Route = createFileRoute("/_authenticated/profile")({
  component: ProfilePage,
});

function ProfilePage(): React.ReactElement {
  const [passwordOpen, setPasswordOpen] = React.useState(false);

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Account
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Profile
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Manage your identity, password, and API keys. Changes apply to your
          own session — never to other users.
        </p>
      </header>

      <IdentityCard onChangePassword={() => setPasswordOpen(true)} />
      <ApiKeysSection />

      <ChangePasswordDialog
        open={passwordOpen}
        onOpenChange={setPasswordOpen}
      />
    </div>
  );
}
