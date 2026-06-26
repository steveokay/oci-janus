import * as React from "react";
import { Link, useRouterState } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import { useWorkspace } from "@/lib/api/workspace";
import { useCacheStats } from "@/lib/api/proxy-cache";
import {
  LayoutDashboard,
  Boxes,
  ShieldCheck,
  Webhook,
  Building,
  Building2,
  Activity,
  KeyRound,
  ScanLine,
  Ship,
  Radio,
  Repeat,
  Settings as SettingsIcon,
  UsersRound,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useAuthStore } from "@/lib/auth/store";
import { isPlatformAdmin } from "@/lib/auth/jwt";

interface NavItem {
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
  adminOnly?: boolean;
  // probeKey opts the item into a probe-then-show contract: the
  // sidebar runs the probe hook and hides the item when the probe
  // returns undefined (403 or 404). Keeps the SECTIONS table
  // declarative while still allowing dynamic visibility for routes
  // that depend on optional backend wiring.
  probeKey?: "proxy-cache";
}

// Section groupings mirror the persona: ops first, RBAC second, infra
// integrations third. Each group's heading is the only hierarchy needed —
// we deliberately avoid nested expanding menus.
const SECTIONS: Array<{ title: string; items: NavItem[] }> = [
  {
    title: "Operate",
    items: [
      { to: "/", label: "Dashboard", icon: LayoutDashboard },
      { to: "/repositories", label: "Repositories", icon: Boxes },
      // FUT-013: pull-through cache visibility. Operator-facing
      // signal alongside Repositories — operators think of the
      // cache as "another set of images we serve," not as an
      // integration. probeKey gates visibility on a successful
      // /proxy/cache/stats probe so deployments without the proxy
      // (403/404) don't show a dead link.
      {
        to: "/workspace/proxy-cache",
        label: "Pull-through cache",
        icon: Repeat,
        probeKey: "proxy-cache",
      },
      // S-MAINT-1 Batch 5 F4 follow-up — dedicated landing for Helm chart
      // users (platform engineers running `helm install`). MVP renders
      // the same repos table as /repositories with chart-focused copy;
      // a workspace-wide chart browser ships in a follow-up sprint.
      { to: "/helm", label: "Helm charts", icon: Ship },
      { to: "/security", label: "Security", icon: ShieldCheck },
      { to: "/activity", label: "Activity", icon: Activity },
    ],
  },
  {
    title: "Access",
    items: [
      // /members is the org-list landing — each card represents one
      // organization and links to the per-org member roster. "Members"
      // didn't carry that meaning at a glance; "Organizations" matches
      // what the page actually surfaces. URL stays /members so existing
      // links + bookmarks don't break. Icon is `Building` (singular)
      // to stay visually distinct from `Building2` already used by
      // the Platform → Tenants entry.
      { to: "/members", label: "Organizations", icon: Building },
      // FUT-012 Phase C — tenant-user lifecycle. Always rendered; the
      // route itself surfaces a 403 ErrorState for non-tenant-admin
      // callers, mirroring how /admin/scanner falls back to the
      // BFF gate rather than duplicating the role check in the
      // sidebar (which would require loading role assignments here).
      { to: "/tenant/users", label: "Tenant users", icon: UsersRound },
      { to: "/api-keys", label: "API keys", icon: KeyRound },
    ],
  },
  {
    title: "Integrations",
    items: [
      { to: "/webhooks", label: "Webhooks", icon: Webhook },
      // Audit-log streaming to SIEM (futures.md Tier 1 #4). Lives in
      // Integrations alongside Webhooks because both are outbound
      // delivery channels — same admin posture, similar mental model.
      { to: "/workspace/audit-export", label: "Audit streaming", icon: Radio },
    ],
  },
  {
    title: "Platform",
    items: [
      {
        to: "/admin/tenants",
        label: "Tenants",
        icon: Building2,
        adminOnly: true,
      },
      // REM-011 Phase 2 FE — platform-wide scanner adapter picker. Gated
      // behind the platform-admin marker (same JWT check as Tenants).
      {
        to: "/admin/scanner",
        label: "Scanner",
        icon: ScanLine,
        adminOnly: true,
      },
    ],
  },
];

export function Sidebar(): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const showAdmin = isPlatformAdmin(claims);
  const { location } = useRouterState();
  const { data: workspace } = useWorkspace();
  // FUT-013 probe: null ⇒ caller is not workspace-admin OR the BFF
  // has no PROXY_GRPC_ADDR wired (both surface as 403/404 → null in
  // the hook). Either way the menu item stays hidden — the page
  // itself would 403/404, no point advertising it. Undefined means
  // "query still loading"; we treat that as not-yet-available so the
  // sidebar doesn't flash the item between mount and first response.
  const { data: proxyCacheStats } = useCacheStats();
  const proxyCacheAvailable = proxyCacheStats != null;

  // FE-API-009 — sidebar header reflects the live workspace name once the
  // BFF responds. Falls back to "Janus / Registry control" on first paint
  // and when the tenant gRPC client isn't wired (BFF returns 404). The
  // tenant id chip in the user menu remains the source of truth for the
  // exact tenant uuid.
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
        {SECTIONS.map((section) => {
          const visibleItems = section.items.filter((i) => {
            if (i.adminOnly && !showAdmin) return false;
            if (i.probeKey === "proxy-cache" && !proxyCacheAvailable) return false;
            return true;
          });
          if (visibleItems.length === 0) return null;
          return (
            <div key={section.title} className="mt-6 first:mt-0">
              <div className="px-2 pb-1 text-[10px] font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
                {section.title}
              </div>
              <ul className="space-y-0.5">
                {visibleItems.map((item) => {
                  const Icon = item.icon;
                  // Mark exact-match for the root path; everything else is prefix.
                  const active =
                    item.to === "/"
                      ? location.pathname === "/"
                      : location.pathname.startsWith(item.to);
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
          / Linear / Notion pattern where personal preferences live at
          the bottom of the chrome and stay reachable regardless of
          scroll. Always rendered — tab-level gates inside /settings
          handle visibility of Notifications / Security per the
          per-category opt-in matrix that lands in FUT-019 Phase 2+. */}
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
