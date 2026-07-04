// REDESIGN-001 Phase 4.2.b — /settings becomes a parent route with tab children.
//
// Previously /settings was a single page with two URL-search-param tabs
// (Notifications + Security FUT-019 placeholders). This file is now the
// layout shell — tabs are real child routes (one per URL):
//
//   /settings/account    — profile, password, notification prefs, my API keys,
//                          MFA/sessions placeholder.        (this PR)
//   /settings/workspace  — workspace-level config.          (Phase 4.2.c)
//   /settings/platform   — cross-tenant + infra surfaces.   (Phase 4.2.d,
//                          multi-mode + is_global_admin only)
//
// Tab visibility is mode + role gated:
//   - Account is always shown.
//   - Workspace is shown when the caller holds ≥ admin on any scope
//     (workspace admin in single mode == effective platform admin per the
//     Phase 5.2 helper). Stub content lands in 4.2.c.
//   - Platform is shown only when DEPLOYMENT_MODE=multi AND
//     users.is_global_admin=true. Hidden in single mode entirely because
//     "workspace = deployment = platform" — all surfaces fold into Workspace.
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
type SettingsTab = "account" | "workspace" | "platform";

interface TabDef {
  key: SettingsTab;
  to: "/settings/account" | "/settings/workspace" | "/settings/platform";
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
  // it entirely so the bootstrap admin doesn't see an empty third tab —
  // single-mode operators get all the same controls inside Workspace.
  const showPlatformTab =
    deploymentInfo?.deployment_mode === "multi" && isGlobalAdmin;

  const tabs: TabDef[] = React.useMemo(() => {
    const out: TabDef[] = [
      { key: "account", to: "/settings/account", label: "Account" },
    ];
    if (hasAnyAdminScope) {
      out.push({ key: "workspace", to: "/settings/workspace", label: "Workspace" });
    }
    if (showPlatformTab) {
      out.push({ key: "platform", to: "/settings/platform", label: "Platform" });
    }
    return out;
  }, [hasAnyAdminScope, showPlatformTab]);

  // Eyebrow above the H1 tracks the active tab — it was hardcoded "Account",
  // which read wrong on /settings/workspace and /settings/platform. Derived
  // from the pathname (same source of truth the tab rail uses for its
  // active state) rather than tab state so deep links land correct.
  const eyebrow = location.pathname.startsWith("/settings/workspace")
    ? "Workspace"
    : location.pathname.startsWith("/settings/platform")
      ? "Platform"
      : "Account";

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
          Personal preferences and{" "}
          {tabs.length > 1 ? "workspace configuration" : "preferences"} live
          here. Each tab is its own URL — bookmark or share the one you live in.
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
