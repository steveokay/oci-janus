// REDESIGN-001 Phase 4.2.b — Platform tab stub.
//
// Real content lands in Phase 4.2.d:
//   - Tenants (CRUD)
//   - SSO (editable)
//   - Scanner adapters
//   - GC schedule + run history
//   - Deployment info
// Plus 301 redirects from the legacy /admin/scanner, /admin/gc, /admin/tenants
// URLs into this tab's anchored sections.
//
// This tab is rendered ONLY in multi-mode deployments with an is_global_admin
// caller. The parent route already enforces both gates before adding this
// tab to the rail — single-mode visitors never see it. A direct /settings/platform
// URL hit in single mode still mounts this stub; that's intentional for now
// (no info disclosure — the page is empty) and the per-section route guard
// in 4.2.d will harden it.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Server } from "lucide-react";

export const Route = createFileRoute("/_authenticated/settings/platform")({
  component: PlatformTabStub,
});

function PlatformTabStub(): React.ReactElement {
  return (
    <section className="rounded-lg border border-dashed border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] p-6 text-center">
      <div className="mx-auto inline-flex size-10 items-center justify-center rounded-md bg-[var(--color-surface)] text-[var(--color-fg-muted)]">
        <Server className="size-5" />
      </div>
      <h2 className="mt-3 font-display text-lg font-medium">
        Platform configuration
      </h2>
      <p className="mx-auto mt-2 max-w-prose text-sm text-[var(--color-fg-muted)]">
        Cross-tenant + infrastructure surfaces — Tenants, SSO, scanner adapters,
        GC schedule, and deployment info — land here in Phase 4.2.d. The legacy
        /admin/scanner, /admin/gc, and /admin/tenants routes will 301 into the
        matching anchored section.
      </p>
      <p className="mt-3 text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
        Tracked under REDESIGN-001 Phase 4.2.d (multi-mode only)
      </p>
    </section>
  );
}
