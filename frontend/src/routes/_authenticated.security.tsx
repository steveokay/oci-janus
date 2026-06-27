// REDESIGN-001 Phase 4.2.e — /security becomes a parent route with tab children.
//
// Previously /security was a single page that toggled Radix Tabs in-memory:
// every visit landed on Overview and the URL never changed when the operator
// clicked through Vulnerabilities → Scans → etc. That made it impossible to
// bookmark "the scans tab" or share a link to remediation. This file is now
// the layout shell — tabs are real child routes (one per URL):
//
//   /security/overview         — severity-breakdown card.
//   /security/vulnerabilities  — VulnerabilitiesTable.
//   /security/scans            — ScanHistoryTable.
//   /security/signing          — Cosign/Notary coverage placeholder
//                                (futures.md "Signed-image admission" Phase 3).
//   /security/remediation      — RemediationTable (FE-API-017).
//   /security/policies         — ScanPolicyEditor (FE-API-018).
//   /security/reports          — ReportsPanel (FE-API-019).
//
// The "Open findings" card + CoverageCard top row stay HERE in the parent —
// they're the always-visible posture summary that contextualises whichever
// tab is open. Only the tab content swaps via <Outlet/>.
//
// Pattern mirrors Phase 4.2.b /settings: link-based tab rail (real
// navigation, URL-driven active state) rather than Radix Tabs, because
// TanStack Router owns the active-pathname truth.
import * as React from "react";
import {
  createFileRoute,
  Link,
  Outlet,
  useRouterState,
} from "@tanstack/react-router";
import { ShieldAlert, ShieldCheck } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { AnimatedNumber } from "@/components/ui/animated-number";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useStats } from "@/lib/api/stats";
import { SeverityBar } from "@/components/security/severity-bar";
import { CoverageCard } from "@/components/security/coverage-card";
import { cn } from "@/lib/utils";
import type { ScanResult } from "@/lib/api/types";

export const Route = createFileRoute("/_authenticated/security")({
  component: SecurityLayout,
});

// SecurityTab is the union of tab keys; each maps 1:1 to a child route.
// Order in the rail matches the Phase 4.2.e dashboard spec:
// Overview · Vulnerabilities · Scans · Signing · Remediation · Policies · Reports.
type SecurityTab =
  | "overview"
  | "vulnerabilities"
  | "scans"
  | "signing"
  | "remediation"
  | "policies"
  | "reports";

interface TabDef {
  key: SecurityTab;
  to:
    | "/security/overview"
    | "/security/vulnerabilities"
    | "/security/scans"
    | "/security/signing"
    | "/security/remediation"
    | "/security/policies"
    | "/security/reports";
  label: string;
}

// Static tab list — every tab is visible to every authenticated caller.
// Per-action gating (e.g. policy edits) is enforced inside each child route
// and ultimately server-side, so the rail itself doesn't need a role gate.
const TABS: TabDef[] = [
  { key: "overview", to: "/security/overview", label: "Overview" },
  { key: "vulnerabilities", to: "/security/vulnerabilities", label: "Vulnerabilities" },
  { key: "scans", to: "/security/scans", label: "Scans" },
  { key: "signing", to: "/security/signing", label: "Signing" },
  { key: "remediation", to: "/security/remediation", label: "Remediation" },
  { key: "policies", to: "/security/policies", label: "Policies" },
  { key: "reports", to: "/security/reports", label: "Reports" },
];

function SecurityLayout(): React.ReactElement {
  const { location } = useRouterState();
  const { data, isLoading, isError, error, refetch } = useStats();
  const vulnCount = data?.vulnerability_count ?? 0;

  // FE-API-016 — per-severity counts feed the SeverityBar in the headline
  // card. Defensive `?? 0` so a stale BFF without the new fields renders
  // an empty bar rather than NaN.
  const severityCounts: ScanResult["severity_counts"] = {
    CRITICAL: data?.critical_count ?? 0,
    HIGH: data?.high_count ?? 0,
    MEDIUM: data?.medium_count ?? 0,
    LOW: data?.low_count ?? 0,
  };
  const criticalOrHigh =
    (data?.critical_count ?? 0) + (data?.high_count ?? 0);
  const headlineTone =
    criticalOrHigh > 0
      ? ("danger" as const)
      : vulnCount > 0
        ? ("warning" as const)
        : ("accent" as const);

  return (
    <div className="space-y-6 p-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Posture
        </p>
        {/* Icon mirrors the sidebar nav entry — consistent with the */}
        {/* /activity (Activity) and /helm (Ship) page headers. */}
        <h1 className="font-display flex items-center gap-3 text-3xl font-medium tracking-tight">
          <ShieldCheck
            className="size-7 text-[var(--color-accent)]"
            aria-hidden
          />
          Security
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Vulnerability findings, scan history, remediation paths — across every
          repository in this workspace.
        </p>
      </header>

      {/* Always-visible posture row — kept here in the parent so the
          headline numbers don't disappear as the operator tabs around. */}
      {isError ? (
        <ErrorState
          title="Couldn't load security overview"
          description="Stats endpoint didn't answer. Check that the management BFF is reachable."
          error={error}
          onRetry={() => void refetch()}
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
          {/* Open findings — the one real number we have today */}
          <Card accentBar={headlineTone} className="md:col-span-1">
            <CardHeader className="pb-2">
              <div className="flex items-center justify-between">
                <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                  Open findings
                </CardDescription>
                {vulnCount > 0 ? (
                  <ShieldAlert className="size-4 text-[var(--color-warning)]" />
                ) : (
                  <ShieldCheck className="size-4 text-[var(--color-success)]" />
                )}
              </div>
            </CardHeader>
            <CardContent className="pt-0 pb-5">
              {isLoading ? (
                <Skeleton className="h-10 w-32" />
              ) : (
                <div className="font-display text-4xl font-medium leading-none tracking-tight">
                  <AnimatedNumber value={vulnCount} />
                </div>
              )}
              <div className="mt-4 space-y-2">
                {isLoading ? (
                  <Skeleton className="h-2 w-full" />
                ) : (
                  <SeverityBar counts={severityCounts} />
                )}
                {isLoading ? null : (
                  <p className="text-xs text-[var(--color-fg-muted)]">
                    {criticalOrHigh > 0
                      ? `${criticalOrHigh.toLocaleString()} CRITICAL or HIGH need attention.`
                      : vulnCount > 0
                        ? "No CRITICAL or HIGH findings — only MEDIUM and below."
                        : "Workspace is clean across the latest scan per tag."}
                  </p>
                )}
              </div>
            </CardContent>
          </Card>

          {/* DSGN-015 — CoverageCard handles its own query
              (useSecurityOverview); we don't add a second consumer of
              useStats here. */}
          <div className="md:col-span-2">
            <CoverageCard />
          </div>
        </div>
      )}

      {/* Link-based tab rail. Style mirrors components/ui/tabs.tsx
          TabsTrigger so the visual language is consistent with the rest of
          the app, but each link is a real navigation that updates the URL.
          Radix Tabs is intentionally NOT used here because TanStack Router
          owns the active-state truth via location.pathname. */}
      <nav
        aria-label="Security tabs"
        className="inline-flex h-10 items-center gap-1 border-b border-[var(--color-border)]"
      >
        {TABS.map((t) => {
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

      {/* Tab content. Each child route renders its own section tree below.
          We don't wrap in <TabsContent> — Outlet is just whichever child
          matched the URL. */}
      <Outlet />
    </div>
  );
}
