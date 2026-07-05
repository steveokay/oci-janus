// /settings is a parent route with tab children. This file is the layout
// shell — tabs are real child routes (one per URL):
//
//   /settings/workspace     — identity / delivery / sign-in / lifecycle +
//                             deployment posture.
//   /settings/scanning      — scan policy + scanner adapters. Single-mode
//                             only (multi mode → Platform tab).
//   /settings/housekeeping  — garbage collection + retention. Single-mode
//                             only (multi mode → Platform tab).
//   /settings/notifications — the per-category notification-preference matrix.
//   /settings/platform      — cross-tenant + infra surfaces. Multi-mode +
//                             is_global_admin only.
//
// Personal account state (identity, password, API keys, MFA, sessions) is NOT
// here — it moved to the top-level /profile page in the 2026-07-05 UI cleanup.
//
// Tab visibility is mode + role gated:
//   - Workspace is shown when the caller holds ≥ admin on any scope.
//   - Scanning + Housekeeping are shown to those admins in single mode only
//     (multi mode keeps the maintenance surfaces on Platform).
//   - Notifications is always shown (personal preference).
//   - Platform is shown only when DEPLOYMENT_MODE=multi AND
//     users.is_global_admin=true.
//
// Each tab renders inside the persistent header/tab-rail via <Outlet/>, so
// switching tabs only swaps the right pane without re-mounting the chrome.
import * as React from "react";
import {
  createFileRoute,
  Link,
  Outlet,
  useRouterState,
} from "@tanstack/react-router";
import { Settings as SettingsIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { useDeploymentInfo } from "@/lib/api/deployment-info";
import { useAbilities, useIsGlobalAdmin } from "@/lib/api/abilities";

export const Route = createFileRoute("/_authenticated/settings")({
  component: SettingsLayout,
});

// SettingsTab is the union of tab keys; each maps 1:1 to a child route.
type SettingsTab =
  | "workspace"
  | "scanning"
  | "housekeeping"
  | "notifications"
  | "platform";

interface TabDef {
  key: SettingsTab;
  to:
    | "/settings/workspace"
    | "/settings/scanning"
    | "/settings/housekeeping"
    | "/settings/notifications"
    | "/settings/platform";
  label: string;
}

function SettingsLayout(): React.ReactElement {
  const { location } = useRouterState();
  const { data: deploymentInfo } = useDeploymentInfo();
  const { data: abilities } = useAbilities();
  const isGlobalAdmin = useIsGlobalAdmin();

  // Mode + role gate per the Phase 4.2 IA design.
  //
  // Workspace tab: shown when the caller is global admin OR holds ≥ admin
  // on at least one scope. Loose check is intentional for the stub — the
  // per-section gate inside the workspace tab content (4.2.c) is what
  // actually decides what renders.
  const hasAnyAdminScope = React.useMemo(() => {
    if (!abilities) return false;
    if (abilities.is_global_admin) return true;
    // hasAbility() walks role_assignments and treats admin/owner as ≥ admin.
    // Mirroring the loose "are you admin of anything?" check that the
    // Workspace tab is gated on.
    return abilities.role_assignments.some(
      (a) => a.role === "admin" || a.role === "owner",
    );
  }, [abilities]);

  // Platform tab is multi-mode + is_global_admin only. Single-mode hides
  // it entirely so the bootstrap admin doesn't see an empty tab —
  // single-mode operators get all the same controls inside Housekeeping.
  const isSingleMode = deploymentInfo?.deployment_mode === "single";
  const showPlatformTab =
    deploymentInfo?.deployment_mode === "multi" && isGlobalAdmin;

  const tabs: TabDef[] = React.useMemo(() => {
    const out: TabDef[] = [];
    if (hasAnyAdminScope) {
      out.push({ key: "workspace", to: "/settings/workspace", label: "Workspace" });
    }
    // Scanning (scan policy + scanner adapters) and Housekeeping (GC +
    // retention) are the single-mode home for the maintenance surfaces; in
    // multi mode they stay on the Platform tab, so both are gated on single
    // mode + admin.
    if (hasAnyAdminScope && isSingleMode) {
      out.push({
        key: "scanning",
        to: "/settings/scanning",
        label: "Scanning",
      });
      out.push({
        key: "housekeeping",
        to: "/settings/housekeeping",
        label: "Housekeeping",
      });
    }
    // Notifications is a personal preference — always available to everyone.
    out.push({
      key: "notifications",
      to: "/settings/notifications",
      label: "Notifications",
    });
    if (showPlatformTab) {
      out.push({ key: "platform", to: "/settings/platform", label: "Platform" });
    }
    return out;
  }, [hasAnyAdminScope, isSingleMode, showPlatformTab]);

  // Eyebrow above the H1 tracks the active tab — it was hardcoded "Account",
  // which read wrong on /settings/workspace and /settings/platform. Derived
  // from the pathname (same source of truth the tab rail uses for its
  // active state) rather than tab state so deep links land correct.
  const eyebrow = location.pathname.startsWith("/settings/scanning")
    ? "Scanning"
    : location.pathname.startsWith("/settings/housekeeping")
      ? "Housekeeping"
      : location.pathname.startsWith("/settings/notifications")
        ? "Notifications"
        : location.pathname.startsWith("/settings/platform")
          ? "Platform"
          : "Workspace";

  return (
    <div className="space-y-6 p-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          {eyebrow}
        </p>
        <h1 className="flex items-center gap-2 font-display text-3xl font-medium tracking-tight">
          <SettingsIcon className="size-6" /> Settings
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Workspace, housekeeping, and notification configuration live here.
          Personal account settings moved to Profile. Each tab is its own URL —
          bookmark or share the one you live in.
        </p>
      </header>

      {/* Link-based tab rail. Style mirrors components/ui/tabs.tsx TabsTrigger
          so the visual language is consistent with everywhere else that uses
          Radix Tabs, but each link is a real navigation that updates the URL.
          We don't use Radix Tabs here because TanStack Router owns the
          active-state truth via location.pathname. */}
      <nav
        aria-label="Settings tabs"
        className="inline-flex h-10 items-center gap-1 border-b border-[var(--color-border)]"
      >
        {tabs.map((t) => {
          const active = location.pathname.startsWith(t.to);
          return (
            <Link
              key={t.key}
              to={t.to}
              className={cn(
                "relative inline-flex h-10 items-center gap-2 rounded-sm px-3 text-sm font-medium",
                "transition-colors focus-visible:outline-none focus-visible:ring-2",
                "focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-2",
                "focus-visible:ring-offset-[var(--color-bg)]",
                active
                  ? "text-[var(--color-fg)] after:absolute after:inset-x-2 after:-bottom-px after:h-[2px] after:rounded-full after:bg-[var(--color-accent)]"
                  : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
              )}
              aria-current={active ? "page" : undefined}
            >
              {t.label}
            </Link>
          );
        })}
      </nav>

      {/* Tab content. Each child route (account/workspace/platform) renders
          its own section tree below. We do NOT wrap in <TabsContent> because
          we're not using Radix Tabs — Outlet is just whichever child matched
          the URL. */}
      <Outlet />
    </div>
  );
}
