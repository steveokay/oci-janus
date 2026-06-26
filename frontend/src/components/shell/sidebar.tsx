// REDESIGN-001 Phase 4.2.a — sidebar IA matches the operator mental
// model (Registry / Security / Governance / Integrations / Access),
// NOT microservice boundaries (per memory/feedback_sidebar_nav_grouping.md).
//
// Items moved/removed in this PR:
//   - /admin/tenants  — route still works via URL; folds under
//                       Settings › Platform in Phase 4.2.d
//   - /admin/scanner  — same
//   - /admin/gc       — same (was never in the sidebar)
//
// The Platform sidebar group was deleted entirely. Until Phase 4.2.d
// migrates the /admin/* surfaces into Settings › Platform tab, platform
// admins must bookmark or type the URLs directly.
import * as React from "react";
import { Link, useRouterState } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import { useWorkspace } from "@/lib/api/workspace";
import { useCacheStats } from "@/lib/api/proxy-cache";
import { useDeploymentInfo } from "@/lib/api/deployment-info";
import {
  LayoutDashboard,
  Boxes,
  ShieldCheck,
  Webhook,
  Building,
  Activity,
  KeyRound,
  Ship,
  Radio,
  Repeat,
  Settings as SettingsIcon,
  UsersRound,
} from "lucide-react";
import { cn } from "@/lib/utils";

interface NavItem {
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
  // probeKey opts the item into a probe-then-show contract: the
  // sidebar runs the probe hook and hides the item when the probe
  // returns undefined (403 or 404). Keeps the GROUPS table declarative
  // while still allowing dynamic visibility for routes that depend on
  // optional backend wiring.
  probeKey?: "proxy-cache";
}

// GROUPS define the sidebar's operator mental model:
//   Registry    — things you push/pull
//   Security    — vulnerability and trust posture
//   Governance  — compliance + audit (same audience: security ops / compliance)
//   Integrations — repo-event webhooks (audience: devs / CI pipelines)
//   Access      — identity and org management
//
// Audit streaming lives under Governance rather than Integrations because its
// audience is compliance / security ops — the same people who use Activity.
// Webhooks live separately under Integrations because their audience is devs
// and CI pipelines who care about repo events, not audit compliance.
const GROUPS: Array<{ title: string; items: NavItem[] }> = [
  {
    title: "Registry",
    items: [
      // Repositories is the primary landing for operators — everything
      // they push/pull lives here.
      { to: "/repositories", label: "Repositories", icon: Boxes },
      // S-MAINT-1 Batch 5 F4 follow-up — dedicated landing for Helm chart
      // users (platform engineers running `helm install`). MVP renders the
      // same repos table as /repositories with chart-focused copy.
      { to: "/helm", label: "Helm charts", icon: Ship },
      // FUT-013: pull-through cache visibility. probeKey gates visibility on a
      // successful /proxy/cache/stats probe so deployments without the proxy
      // (403/404) don't show a dead link.
      {
        to: "/workspace/proxy-cache",
        label: "Pull-through cache",
        icon: Repeat,
        probeKey: "proxy-cache",
      },
    ],
  },
  {
    title: "Security",
    items: [
      // Single Security page for now. Phase 4.2.e splits this into
      // Overview / Vulnerabilities / Scans / Signing / Policies / Reports.
      { to: "/security", label: "Security", icon: ShieldCheck },
    ],
  },
  {
    title: "Governance",
    items: [
      { to: "/activity", label: "Activity", icon: Activity },
      // Audit-log streaming to SIEM (futures.md Tier 1 #4). Lives in
      // Governance — same audience as Activity (compliance, security ops).
      // Repo-event webhooks live separately under Integrations because their
      // audience is devs/CI, not compliance.
      { to: "/workspace/audit-export", label: "Audit streaming", icon: Radio },
    ],
  },
  {
    title: "Integrations",
    items: [
      // Webhooks are repo-event delivery channels for devs and CI pipelines.
      // Kept in a standalone Integrations group rather than merged into
      // Governance because the audiences differ: devs/CI (webhooks) vs
      // compliance/security ops (audit streaming + activity).
      { to: "/webhooks", label: "Webhooks", icon: Webhook },
    ],
  },
  {
    title: "Access",
    items: [
      // /members is the org-list landing — each card represents one
      // organization and links to the per-org member roster. URL stays
      // /members so existing bookmarks don't break.
      { to: "/members", label: "Organizations", icon: Building },
      // FUT-012 Phase C — tenant-user lifecycle. Always rendered; the
      // route itself surfaces a 403 ErrorState for non-tenant-admin
      // callers, mirroring how /admin/scanner falls back to the BFF gate
      // rather than duplicating the role check in the sidebar.
      { to: "/tenant/users", label: "Tenant users", icon: UsersRound },
      { to: "/api-keys", label: "API keys", icon: KeyRound },
    ],
  },
];

export function Sidebar(): React.ReactElement {
  const { location } = useRouterState();
  const { data: workspace } = useWorkspace();
  // FUT-013 probe: null ⇒ caller is not workspace-admin OR the BFF has no
  // PROXY_GRPC_ADDR wired (both surface as 403/404 → null in the hook).
  // Either way the menu item stays hidden — the page would 403/404 anyway.
  // undefined means "query still loading"; treat as not-yet-available so the
  // sidebar doesn't flash the item between mount and first response.
  const { data: proxyCacheStats } = useCacheStats();
  const proxyCacheAvailable = proxyCacheStats != null;

  // useDeploymentInfo called for future-proofing: Phase 4.2.b/c/d will use
  // deploymentInfo.data?.deployment_mode === "multi" to gate platform-only items.
  // Called here (result unused) so the hook is load-bearing and its cache is
  // primed before the Settings tab renders in the same shell.
  useDeploymentInfo();

  // FE-API-009 — sidebar header reflects the live workspace name once the BFF
  // responds. Falls back to "Janus" on first paint and when the tenant gRPC
  // client isn't wired (BFF returns 404).
  const workspaceName = workspace?.name ?? "Janus";
  const workspaceSubLabel = workspace?.name ? "Workspace" : "Registry control";

  return (
    <aside
      className={cn(
        "hidden h-full w-[248px] shrink-0 flex-col border-r border-[var(--color-border)]",
        "bg-[var(--color-surface-2)] lg:flex",
      )}
    >
      {/* Brand */}
      <Link to="/" className="flex items-center gap-2.5 px-5 py-5">
        <span
          className="grid size-8 place-items-center rounded-md bg-[var(--color-accent)] text-[var(--color-accent-fg)]"
          aria-hidden
        >
          <span className="font-display text-lg font-semibold leading-none">
            {workspaceName[0]?.toUpperCase() ?? "J"}
          </span>
        </span>
        <div className="flex min-w-0 flex-col leading-tight">
          <div className="flex items-center gap-1.5">
            <span className="truncate text-sm font-semibold tracking-tight">
              {workspaceName}
            </span>
            {workspace?.plan ? (
              <Badge
                tone={
                  workspace.plan === "enterprise"
                    ? "accent"
                    : workspace.plan === "pro"
                      ? "success"
                      : "neutral"
                }
                className="!py-0 text-[9px] uppercase tracking-wider"
              >
                {workspace.plan}
              </Badge>
            ) : null}
          </div>
          <span className="text-[10px] uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            {workspaceSubLabel}
          </span>
        </div>
      </Link>

      <nav className="flex-1 overflow-y-auto px-3 pb-4">
        {GROUPS.map((group) => {
          // Filter items by probe key. No adminOnly flag exists in the new IA —
          // the Platform group (which used adminOnly) was removed entirely.
          const visibleItems = group.items.filter((i) => {
            if (i.probeKey === "proxy-cache" && !proxyCacheAvailable)
              return false;
            return true;
          });
          // Skip rendering the entire group heading if all items are hidden.
          if (visibleItems.length === 0) return null;
          return (
            <div key={group.title} className="mt-6 first:mt-0">
              <div className="px-2 pb-1 text-[10px] font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
                {group.title}
              </div>
              <ul className="space-y-0.5">
                {visibleItems.map((item) => {
                  const Icon = item.icon;
                  // Mark exact-match for the root path; everything else is prefix.
                  const active = location.pathname.startsWith(item.to);
                  return (
                    <li key={item.to}>
                      <Link
                        to={item.to}
                        className={cn(
                          "group flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm",
                          "transition-colors",
                          active
                            ? "bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
                            : "text-[var(--color-fg)] hover:bg-[var(--color-surface-sunken)]",
                        )}
                      >
                        <Icon
                          className={cn(
                            "size-4 shrink-0",
                            active
                              ? "text-[var(--color-accent)]"
                              : "text-[var(--color-fg-muted)] group-hover:text-[var(--color-fg)]",
                          )}
                        />
                        <span className="flex-1 truncate">{item.label}</span>
                      </Link>
                    </li>
                  );
                })}
              </ul>
            </div>
          );
        })}
      </nav>

      {/* FUT-019 Phase 1 — sticky-bottom Settings cog. Mirrors the GitHub
          / Linear / Notion pattern where personal preferences live at the
          bottom of the chrome and stay reachable regardless of scroll.
          Always rendered — tab-level gates inside /settings handle
          visibility of Notifications / Security per the per-category
          opt-in matrix that lands in FUT-019 Phase 2+. */}
      <div className="border-t border-[var(--color-border)] px-3 py-2">
        <Link
          to="/settings"
          className={cn(
            "group flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm",
            "transition-colors",
            location.pathname.startsWith("/settings")
              ? "bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
              : "text-[var(--color-fg)] hover:bg-[var(--color-surface-sunken)]",
          )}
        >
          <SettingsIcon
            className={cn(
              "size-4 shrink-0",
              location.pathname.startsWith("/settings")
                ? "text-[var(--color-accent)]"
                : "text-[var(--color-fg-muted)] group-hover:text-[var(--color-fg)]",
            )}
          />
          <span className="flex-1 truncate">Settings</span>
        </Link>
      </div>

      {/* Footer — small build hint, not load-bearing */}
      <div className="border-t border-[var(--color-border)] px-5 py-3 text-[11px] text-[var(--color-fg-subtle)]">
        Beacon UI · v0.1
      </div>
    </aside>
  );
}
