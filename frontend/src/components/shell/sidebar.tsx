import * as React from "react";
import { Link, useRouterState } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import { useWorkspace } from "@/lib/api/workspace";
import {
  LayoutDashboard,
  Boxes,
  ShieldCheck,
  Users,
  Webhook,
  Building2,
  Activity,
  KeyRound,
  Globe,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useAuthStore } from "@/lib/auth/store";
import { isPlatformAdmin } from "@/lib/auth/jwt";

interface NavItem {
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
  adminOnly?: boolean;
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
      { to: "/security", label: "Security", icon: ShieldCheck },
      { to: "/activity", label: "Activity", icon: Activity },
    ],
  },
  {
    title: "Access",
    items: [
      { to: "/members", label: "Members", icon: Users },
      { to: "/api-keys", label: "API keys", icon: KeyRound },
      { to: "/workspace/domains", label: "Custom domains", icon: Globe },
    ],
  },
  {
    title: "Integrations",
    items: [{ to: "/webhooks", label: "Webhooks", icon: Webhook }],
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
    ],
  },
];

export function Sidebar(): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const showAdmin = isPlatformAdmin(claims);
  const { location } = useRouterState();
  const { data: workspace } = useWorkspace();

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
          const visibleItems = section.items.filter(
            (i) => !i.adminOnly || showAdmin,
          );
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

      {/* Footer — small build hint, not load-bearing */}
      <div className="border-t border-[var(--color-border)] px-5 py-3 text-[11px] text-[var(--color-fg-subtle)]">
        Beacon UI · v0.1
      </div>
    </aside>
  );
}
