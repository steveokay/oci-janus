// REDESIGN-001 Phase 4.2.a — sidebar IA matches the operator mental
// model (Registry / Security / Governance / Integrations / Access),
// NOT microservice boundaries (per memory/feedback_sidebar_nav_grouping.md).
//
// Items moved/removed in 4.2.a:
//   - /admin/tenants  — route still works via URL; folds under
//                       Settings › Platform in Phase 4.2.d
//   - /admin/scanner  — same
//   - /admin/gc       — same (was never in the sidebar)
//
// The Platform sidebar group was deleted entirely. Until Phase 4.2.d
// migrated the /admin/* surfaces into Settings › Platform tab, platform
// admins had to bookmark the URLs directly. 4.2.d ships the migration.
//
// REDESIGN-001 Phase 4.6 — the same nav body now also feeds the mobile
// off-canvas drawer (MobileNav). All the rendering logic lives in
// `SidebarBody`; the desktop `<Sidebar>` and `<MobileNav>` (in
// mobile-nav.tsx) wrap it in their own container chrome. The mobile
// wrapper passes `onNavigate` so each link tap closes the drawer.
import * as React from "react";
import { Link, useRouterState } from "@tanstack/react-router";
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
      // Dashboard is the index route ("/") — the operator's landing
      // overview. Kept first so the top-most nav entry maps to the app
      // home, matching the brand link's `to="/"` target.
      { to: "/", label: "Dashboard", icon: LayoutDashboard },
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

// SidebarBody renders the shared nav column (brand + groups + Settings
// footer + Beacon hint) for BOTH the desktop aside and the mobile drawer.
// The only behavioural difference is `onNavigate`: the mobile wrapper
// passes a close-drawer callback so tapping a link dismisses the
// off-canvas. Desktop omits the prop and link clicks behave as plain nav.
//
// Exported so mobile-nav.tsx can mount it inside a Radix Dialog content.
interface SidebarBodyProps {
  onNavigate?: () => void;
}

export function SidebarBody({
  onNavigate,
}: SidebarBodyProps): React.ReactElement {
  const { location } = useRouterState();
  const { data: workspace } = useWorkspace();
  // FUT-013 probe: null ⇒ caller is not workspace-admin OR the BFF has no
  // PROXY_GRPC_ADDR wired (both surface as 403/404 → null in the hook).
  // Either way the menu item stays hidden — the page would 403/404 anyway.
  // undefined means "query still loading"; treat as not-yet-available so the
  // sidebar doesn't flash the item between mount and first response.
  const { data: proxyCacheStats } = useCacheStats();
  const proxyCacheAvailable = proxyCacheStats != null;

  // useDeploymentInfo called for future-proofing: Phase 4.2.b/c/d uses
  // deploymentInfo.data?.deployment_mode === "multi" to gate platform-only
  // items. Called here (result unused) so the hook is load-bearing and its
  // cache is primed before the Settings tab renders in the same shell.
  //
  // Phase 4.6 note: the desktop <Sidebar> and the mobile <MobileNav> both
  // mount SidebarBody in parallel (CSS hides one, but both stay in the
  // DOM). That means this hook is called twice. Intentional — TanStack
  // Query dedupes by query key, so the second call is a cache hit, no
  // extra network. Don't "optimise" by lifting state to a context.
  useDeploymentInfo();

  // FE-API-009 — sidebar header reflects the live workspace name once the BFF
  // responds. Falls back to "Janus" on first paint and when the tenant gRPC
  // client isn't wired (BFF returns 404).
  const workspaceName = workspace?.name ?? "Janus";
  const workspaceSubLabel = workspace?.name ? "Workspace" : "Registry control";

  return (
    <>
      {/* Brand */}
      <Link
        to="/"
        onClick={onNavigate}
        // a11y — brand link is a keyboard focus stop; mirror the topbar
        // hamburger's visible focus ring so keyboard users can see it.
        className="flex items-center gap-2.5 px-5 py-5 focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40"
      >
        <span
          className="grid size-8 place-items-center rounded-md bg-[var(--color-accent)] text-[var(--color-accent-fg)]"
          aria-hidden
        >
          <span className="font-display text-lg font-semibold leading-none">
            {workspaceName[0]?.toUpperCase() ?? "J"}
          </span>
        </span>
        <div className="flex min-w-0 flex-col leading-tight">
          {/* REDESIGN-001 Phase 2.4 (RM-006) — the plan badge that used to
              live here was meaningful only in multi-tenant SaaS where each
              tenant's plan drove billing surfaces. In the self-hosted
              default posture there's no billing, so the badge was empty
              chrome. The `plan` field is still served by the BFF
              (HD-004 — keep the column for forward compat) and remains
              on the multi-mode admin Tenants page; it just stops rendering
              in the personal navigation chrome. */}
          <span className="truncate text-sm font-semibold tracking-tight">
            {workspaceName}
          </span>
          <span className="text-[10px] uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            {workspaceSubLabel}
          </span>
        </div>
      </Link>

      <nav aria-label="Primary" className="flex-1 overflow-y-auto px-3 pb-4">
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
                  // Active when the pathname is the item's route or a child of
                  // it — a path-boundary match (`to` OR `to/…`) so `/repo` does
                  // not falsely match `/repository`. A bare startsWith would.
                  //
                  // The Dashboard root ("/") is special-cased to an EXACT match:
                  // every route starts with "/", so the child-boundary branch
                  // ("//") would never fire but a naive startsWith("/") would
                  // light up Dashboard on every page. Exact-only keeps it lit
                  // solely on the index route.
                  const active =
                    item.to === "/"
                      ? location.pathname === "/"
                      : location.pathname === item.to ||
                        location.pathname.startsWith(item.to + "/");
                  return (
                    <li key={item.to}>
                      <Link
                        to={item.to}
                        onClick={onNavigate}
                        // aria-current="page" exposes the active route to AT,
                        // matching the app's sub-nav convention.
                        aria-current={active ? "page" : undefined}
                        className={cn(
                          "group flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm",
                          "transition-colors",
                          // a11y — visible keyboard focus ring on every nav link.
                          "focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40",
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
          onClick={onNavigate}
          // aria-current="page" when on any /settings route, matching the
          // nav-link convention above.
          aria-current={
            location.pathname === "/settings" ||
            location.pathname.startsWith("/settings/")
              ? "page"
              : undefined
          }
          className={cn(
            "group flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm",
            "transition-colors",
            // a11y — visible keyboard focus ring, matching the primary nav.
            "focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40",
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
    </>
  );
}

// Sidebar is the desktop wrapper — a fixed-width aside that's hidden below
// the `lg` breakpoint. Mobile gets the same content through MobileNav
// (see components/shell/mobile-nav.tsx) which mounts SidebarBody inside a
// Radix Dialog.
export function Sidebar(): React.ReactElement {
  return (
    <aside
      // a11y — label the desktop nav landmark so AT can distinguish it from
      // the footer nav. The inner <nav aria-label="Primary"> carries the
      // matching list semantics.
      aria-label="Primary"
      className={cn(
        "hidden h-full w-[248px] shrink-0 flex-col border-r border-[var(--color-border)]",
        "bg-[var(--color-surface-2)] lg:flex",
      )}
    >
      <SidebarBody />
    </aside>
  );
}
