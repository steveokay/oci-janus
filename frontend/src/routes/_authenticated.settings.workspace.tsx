// REDESIGN-001 Phase 4.2.c — Settings › Workspace tab.
//
// The workhorse tab — 90% of routine workspace config lives here:
//   - Members + Organizations  (link → /members)
//   - SSO                       (read-only info card; configured in deploy files)
//   - Retention defaults        (link → per-org settings)
//   - Scan policies             (embedded tenant-wide ScanPolicyEditor)
//   - Workspace webhooks        (link → /webhooks)
//
// In Phase 4.2.d the single-mode variant of this tab also absorbs
// Scanner adapters, GC schedule, and Deployment info (single mode has no
// separate Platform tab — workspace == deployment).
//
// Mode + role rules:
//   - Parent route already gates the TAB itself on "caller has admin
//     somewhere", so reaching this URL means SOMETHING here is editable.
//   - Sections are individually rendered to all admin callers and any
//     section that requires extra escalation (e.g. tenant admin) does its
//     own per-action gate inline. The ScanPolicyEditor's PUT is itself
//     admin-gated server-side; the FE is a read-most surface.
//
// Sections deliberately don't re-implement existing pages — Members/Orgs
// stays at /members and webhooks at /webhooks. The Workspace tab is the
// hub, not a re-host of every editor. Embedding ScanPolicyEditor is the
// exception because it's tenant-scoped (no per-org/repo route) and is
// quick enough to render inline.
import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  Building2,
  Webhook as WebhookIcon,
  ArrowRight,
  Archive,
} from "lucide-react";
import { SSOReadOnlyCard } from "@/components/admin/sso-readonly-card";
import { ScanPolicyEditor } from "@/components/security/scan-policy-editor";
import { ScannerAdaptersSection } from "@/components/admin/scanner/scanner-adapters-section";
import { GCCard } from "@/components/admin/gc-card";
import { RetentionCard } from "@/components/admin/retention-card";
import { DeploymentInfoCard } from "@/components/admin/deployment-info-card";
import { SectionAnchorNav } from "@/components/ui/section-anchor-nav";
import { useDeploymentInfo } from "@/lib/api/deployment-info";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_authenticated/settings/workspace")({
  component: WorkspaceTab,
});

function WorkspaceTab(): React.ReactElement {
  // In single mode the Workspace tab absorbs everything Platform-tab in
  // multi mode would carry (Scanner / GC / Retention / Deployment info)
  // because there IS no Platform tab in single mode — the deployment is
  // the workspace is the platform.
  //
  // We gate on the live deployment_mode rather than a build flag so an
  // operator who flips DEPLOYMENT_MODE without restarting the dashboard
  // sees the right surface on next page load.
  const { data: deploymentInfo } = useDeploymentInfo();
  const isSingleMode = deploymentInfo?.deployment_mode === "single";

  return (
    <div className="space-y-6">
      {/* Top row: Members/Orgs + Webhooks. Both are quick "go here" cards
          because their real surfaces are already polished elsewhere — the
          Workspace tab is a router, not a re-host. */}
      <div className="grid gap-4 md:grid-cols-2">
        <LinkCard
          to="/members"
          icon={<Building2 className="size-4" />}
          eyebrow="Identity"
          title="Members & organizations"
          body="Organizations are the unit of access control — every repository belongs to one, and each org has its own member roster + roles. Manage them from the Organizations page."
          cta="Open organizations"
        />
        <LinkCard
          to="/webhooks"
          icon={<WebhookIcon className="size-4" />}
          eyebrow="Delivery"
          title="Workspace webhooks"
          body="Repository event webhooks (push, pull, scan completed) and HMAC delivery configuration. Audit / SIEM streaming lives under Governance, not here."
          cta="Open webhooks"
        />
      </div>

      {/* SSO read-only info card (shared SSOReadOnlyCard primitive). Per
          RM-003/004 (Phase 2.2), SSO is now a global deployment-config
          concern — there is no FE editor in single mode. Multi-mode operators
          get the editable surface inside the Platform tab (4.2.d). For
          everyone else this card explains the posture without misleading them
          into thinking there's a toggle. */}
      <SSOReadOnlyCard
        note={
          <>
            Edits require a deployment restart. To rotate a client secret or add
            a provider, update the deployment config and redeploy — the login
            screen picks up the change on the next page load. Multi-tenant
            deployments expose an editable surface under Settings › Platform.
          </>
        }
      />

      {/* Retention defaults. Per-org / per-repo today (no tenant-wide row),
          so this card routes operators to /members where each org has its
          own settings page hosting the retention editor. A future
          enhancement could embed an "org default" rollup here. */}
      <LinkCard
        to="/members"
        icon={<Archive className="size-4" />}
        eyebrow="Lifecycle"
        title="Retention defaults"
        body="Retention policies (max age, max count, max storage, dangling grace, max idle) are configured per organization and inherited per repository. Open an organization to set or edit its default policy. Per-repository overrides live on each repository's settings tab."
        cta="Pick an organization"
      />

      {/* Embedded ScanPolicyEditor — the only section that doesn't link out
          because the tenant-wide scan policy has no other home. The editor
          is RBAC-aware: GET is reader-grade, PUT requires admin on at least
          one org in the tenant. Non-admins see a disabled "Save" button
          rather than a 401 toast. */}
      <ScanPolicyEditor />

      {/* Single-mode-only sections (Phase 4.2.d).
          These also live on the Platform tab in multi mode; in single mode
          there IS no Platform tab so they collapse here. Hidden in multi
          mode to avoid duplicating the editable surface across two tabs —
          the multi-mode Workspace tab stays scoped to per-workspace
          concerns. */}
      {isSingleMode ? (
        <>
          {/* Anchor chips for the single-mode-only section stack. Only these
              four blocks carry #id anchors on this tab, and they only exist
              in single mode — so the chip row is rendered inside the same
              conditional and lists exactly the sections below it. */}
          <SectionAnchorNav
            ariaLabel="Workspace platform sections"
            items={[
              { id: "scanner", label: "Scanner" },
              { id: "gc", label: "Garbage collection" },
              { id: "retention", label: "Retention" },
              { id: "deployment", label: "Deployment" },
            ]}
          />
          <ScannerAdaptersSection />
          <section id="gc" className="space-y-4 scroll-mt-24">
            <GCCard />
          </section>
          <section id="retention" className="space-y-4 scroll-mt-24">
            <RetentionCard />
          </section>
          <DeploymentInfoCard />
        </>
      ) : null}
    </div>
  );
}

// LinkCard — visual primitive shared by every "this lives elsewhere" entry.
// Kept inline because (a) it's only used here, (b) the existing Card +
// CardHeader composition leaves the visual language we want intact, and
// (c) extracting it to a /components dir would create a dependency on a
// route file's design intent that's better kept colocated.
function LinkCard({
  to,
  icon,
  eyebrow,
  title,
  body,
  cta,
}: {
  to: "/members" | "/webhooks";
  icon: React.ReactNode;
  eyebrow: string;
  title: string;
  body: string;
  cta: string;
}): React.ReactElement {
  return (
    <Link
      to={to}
      className={cn(
        "group block rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)]",
        "p-5 shadow-[var(--shadow-card)] transition-colors",
        "hover:border-[var(--color-border-strong)] hover:bg-[var(--color-surface-sunken)]",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]",
      )}
    >
      <div className="flex items-start gap-3">
        <div className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)] group-hover:text-[var(--color-fg)]">
          {icon}
        </div>
        <div className="flex-1 space-y-1">
          <p className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            {eyebrow}
          </p>
          <h3 className="font-display text-base font-medium">{title}</h3>
          <p className="text-sm text-[var(--color-fg-muted)]">{body}</p>
          <p className="inline-flex items-center gap-1 pt-1 text-sm font-medium text-[var(--color-accent)]">
            {cta}
            <ArrowRight className="size-3.5 transition-transform group-hover:translate-x-0.5" />
          </p>
        </div>
      </div>
    </Link>
  );
}
