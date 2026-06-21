import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ApiKeysSection } from "@/components/profile/api-keys-section";

// /api-keys — workspace credential surface deep-linked from the sidebar
// "Access" group. The actual list + create + delete UI is reused from
// the /profile page (`ApiKeysSection`), so a key issued or revoked here
// is the same row visible there. They're literally the same data.
//
// Why both surfaces exist:
//
//   - /profile is the place an operator goes for their own account
//     hygiene (identity, password, MFA when we ship it).
//   - /api-keys is the place an operator goes when wiring a CI pipeline
//     or terraform module — deep-linkable, bookmarkable, and the URL
//     the sidebar's "Access" group has promised since Sprint 0.
//
// The header text on this route leans into the workspace-credential
// framing ("CI / Terraform / scripts"), where /profile leans into
// personal account. Same component, different surrounding copy.
//
// Future scope (tracked in `futures.md`): per-key scopes (pull-only /
// pull-push / admin), service-account keys owned by a workspace rather
// than a human, per-key last-used telemetry, IP allowlists.
export const Route = createFileRoute("/_authenticated/api-keys")({
  component: ApiKeysPage,
});

function ApiKeysPage(): React.ReactElement {
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
