import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { EmptyState } from "@/components/ui/empty-state";
import { KeyRound } from "lucide-react";

// Placeholder — full Profile + API keys lands in Sprint 7.
// Exists in Sprint 0 so the Topbar user-menu link has a target.
export const Route = createFileRoute("/_authenticated/profile")({
  component: ProfilePlaceholder,
});

function ProfilePlaceholder(): React.ReactElement {
  return (
    <div className="space-y-6">
      <div>
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Account
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Profile
        </h1>
      </div>
      <EmptyState
        icon={<KeyRound className="size-5" />}
        title="Profile and API keys arrive in Sprint 7"
        description="Identity card, password change, and API key management land in the final user-facing sprint."
      />
    </div>
  );
}
