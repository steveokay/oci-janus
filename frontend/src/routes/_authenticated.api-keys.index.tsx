import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ApiKeysSection } from "@/components/profile/api-keys-section";

// /api-keys (exact) — personal API keys page.
//
// This file holds the content that used to live directly in
// `_authenticated.api-keys.tsx` before T24 converted that file into the
// `/api-keys` hub layout shell. Moving the content here lets TanStack Router
// treat /api-keys as a layout route and render `ApiKeysSection` only when the
// path exactly matches, while other child routes (service-accounts, activity,
// preview surfaces) each render in the right pane without replacing the
// `AccessSubNav` rail.
//
// Why both /api-keys and /profile show the same `ApiKeysSection`:
//
//   - /profile is where an operator manages account hygiene (identity,
//     password, MFA when we ship it).
//   - /api-keys is where an operator goes when wiring a CI pipeline or
//     Terraform module — deep-linkable, bookmarkable, the URL the sidebar's
//     "Access" group has pointed to since Sprint 0.
//
// They're the same data; the surrounding copy frames the context differently.
export const Route = createFileRoute("/_authenticated/api-keys/")({
  component: PersonalKeysPage,
});

function PersonalKeysPage(): React.ReactElement {
  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Access
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          API keys
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Long-lived credentials for CI pipelines, Terraform modules, and
          scripts. Each key is shown in plaintext exactly once at creation
          and can be revoked from this page or your{" "}
          <Link
            to="/profile"
            className="text-[var(--color-accent)] hover:underline"
          >
            profile
          </Link>
          .
        </p>
      </header>

      <ApiKeysSection />
    </div>
  );
}
