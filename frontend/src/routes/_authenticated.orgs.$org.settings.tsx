import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ChevronRight, Settings as SettingsIcon } from "lucide-react";
import { OrgRetentionPanel } from "@/components/orgs/org-retention-panel";
import { OrgScanPolicySection } from "@/components/orgs/org-scan-policy-section";
import { OrgBulkScanSection } from "@/components/orgs/org-bulk-scan-section";

// Beacon — Org settings route (S11 Slice 4, FE-API-039).
//
// Currently hosts one section: Default retention. Designed as a future
// home for org-level controls (e.g. default scan policy, default
// visibility) so the route layout already accommodates multiple cards;
// for now the page is single-section.
//
// Reachable via:
//   - The "Default retention" link on the org members page header
//   - Direct URL /orgs/{org}/settings (deep-linkable, RBAC-gated by the
//     BFF's role check on the underlying GET; non-members get a 404
//     surfaced as the panel's ErrorState)
//
// Tabs vs. separate routes — we opted for a dedicated route rather than
// adding a Retention tab to /orgs/$org/members so each surface has its
// own URL. Mirrors the per-repo Retention tab in spirit (same panel
// shape, same editor), just URL-honest at the org tier.

export const Route = createFileRoute("/_authenticated/orgs/$org/settings")({
  component: OrgSettings,
});

function OrgSettings(): React.ReactElement {
  const { org } = Route.useParams();

  return (
    <div className="space-y-6">
      <nav
        aria-label="Breadcrumb"
        className="flex items-center gap-1 text-xs text-[var(--color-fg-muted)]"
      >
        <Link to="/members" className="hover:text-[var(--color-fg)]">
          Members
        </Link>
        <ChevronRight className="size-3 text-[var(--color-fg-subtle)]" />
        <Link
          to="/orgs/$org/members"
          params={{ org }}
          className="font-mono hover:text-[var(--color-fg)]"
        >
          {org}
        </Link>
        <ChevronRight className="size-3 text-[var(--color-fg-subtle)]" />
        <span className="text-[var(--color-fg)]">Settings</span>
      </nav>

      <header className="space-y-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Organization settings
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          <span className="text-[var(--color-fg-muted)]">org/</span>
          {org}
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Org-wide defaults that apply to every repository under this org.
          Per-repo overrides always win.
        </p>
      </header>

      <section className="space-y-3">
        <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          <SettingsIcon className="size-3.5" aria-hidden />
          Default retention
        </div>
        <OrgRetentionPanel org={org} />
      </section>

      {/* FE-API-049 — org-default scan policy. Sits next to retention so */}
      {/* operators tune the two policy families in one place. Per-repo   */}
      {/* overrides live on the repo Settings tab once that lands.        */}
      <section className="space-y-3">
        <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          <SettingsIcon className="size-3.5" aria-hidden />
          Default scan policy
        </div>
        <OrgScanPolicySection org={org} />
      </section>

      {/* FUT-088 #5 — org-wide bulk scan. The hook + BFF route existed but */}
      {/* no UI imported it; sits under the policy editors as an action.    */}
      <section className="space-y-3">
        <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          <SettingsIcon className="size-3.5" aria-hidden />
          Actions
        </div>
        <OrgBulkScanSection org={org} />
      </section>
    </div>
  );
}
