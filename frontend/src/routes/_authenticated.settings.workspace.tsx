// REDESIGN-001 Phase 4.2.b — Workspace tab stub.
//
// Real content lands in Phase 4.2.c:
//   - Members + Organizations
//   - SSO (read-only in single mode; editable in multi mode here)
//   - Retention defaults
//   - Scan policies
//   - Workspace webhooks
// And in single mode, the Workspace tab also picks up:
//   - Scanner adapters
//   - GC schedule + run history
//   - Deployment info
// because "workspace = deployment = platform" — no separate Platform tab
// renders in single mode.
//
// The parent route (_authenticated.settings.tsx) already gates this tab on
// the caller having ≥ admin somewhere, so reaching this URL means the
// caller is allowed to see workspace config — not just to edit every section.
// Per-section gates land with the real content in 4.2.c.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Building2 } from "lucide-react";

export const Route = createFileRoute("/_authenticated/settings/workspace")({
  component: WorkspaceTabStub,
});

function WorkspaceTabStub(): React.ReactElement {
  return (
    <section className="rounded-lg border border-dashed border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] p-6 text-center">
      <div className="mx-auto inline-flex size-10 items-center justify-center rounded-md bg-[var(--color-surface)] text-[var(--color-fg-muted)]">
        <Building2 className="size-5" />
      </div>
      <h2 className="mt-3 font-display text-lg font-medium">
        Workspace configuration
      </h2>
      <p className="mx-auto mt-2 max-w-prose text-sm text-[var(--color-fg-muted)]">
        Members, organizations, SSO (read-only display in single mode),
        retention defaults, scan policies, and workspace webhooks land here in
        Phase 4.2.c. In single-mode deployments this tab also absorbs scanner
        adapters, GC schedule, and deployment info — there's no separate
        Platform tab in single mode because workspace == deployment.
      </p>
      <p className="mt-3 text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
        Tracked under REDESIGN-001 Phase 4.2.c
      </p>
    </section>
  );
}
