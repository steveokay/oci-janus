import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { ShieldAlert, ShieldCheck, ArrowRight } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { AnimatedNumber } from "@/components/ui/animated-number";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import { ErrorState } from "@/components/ui/error-state";
import { Badge } from "@/components/ui/badge";
import { useStats } from "@/lib/api/stats";
import {
  SeverityBar,
  SeverityLegend,
} from "@/components/security/severity-bar";
import { VulnerabilitiesTable } from "@/components/security/vulnerabilities-table";
import { ScanHistoryTable } from "@/components/security/scan-history-table";
// S9.5 — six FE-API surfaces that replaced the prior ComingSoon panels.
import { RemediationTable } from "@/components/security/remediation-table";
import { ScanPolicyEditor } from "@/components/security/scan-policy-editor";
import { ReportsPanel } from "@/components/security/reports-panel";
import { CoverageCard } from "@/components/security/coverage-card";
import type { ScanResult } from "@/lib/api/types";

export const Route = createFileRoute("/_authenticated/security")({
  component: SecurityPage,
});

function SecurityPage(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useStats();
  const vulnCount = data?.vulnerability_count ?? 0;

  // FE-API-016 — pull the per-severity counts into the shape SeverityBar +
  // SeverityLegend already consume. Defensive `?? 0` so a stale BFF that
  // doesn't yet populate the new fields renders an empty bar rather than NaN.
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
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Posture
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Security
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Vulnerability findings, scan history, remediation paths — across every
          repository in this workspace.
        </p>
      </header>

      {isError ? (
        <ErrorState
          title="Couldn't load security overview"
          description="Stats endpoint didn't answer. Check that the management BFF is reachable."
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

          {/* What each tab covers — a directory rather than a chart we can't draw yet */}
          <Card className="md:col-span-2">
            <CardHeader className="pb-2">
              <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                What you can do here
              </CardDescription>
            </CardHeader>
            <CardContent className="pt-0">
              <ul className="space-y-2">
                {/* S9.5 — every surface here is now live. We retain the
                    directory so the operator sees the breadth of the page
                    without scrolling, but the "deferred" tone is gone. */}
                {[
                  {
                    label: "Inspect every open CVE across the workspace",
                    tone: "live" as const,
                    hint: "Vulnerabilities tab",
                  },
                  {
                    label: "Audit recent scan runs and triggers",
                    tone: "live" as const,
                    hint: "Scans tab",
                  },
                  {
                    label: "Find remediation paths grouped by upgrade",
                    tone: "live" as const,
                    hint: "Remediation tab",
                  },
                  {
                    label: "Configure block-on-severity scan policies",
                    tone: "live" as const,
                    hint: "Policies tab",
                  },
                  {
                    label: "Generate downloadable compliance reports",
                    tone: "live" as const,
                    hint: "Reports tab",
                  },
                ].map((row) => (
                  <li
                    key={row.label}
                    className="flex items-center gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2"
                  >
                    <ArrowRight className="size-3.5 text-[var(--color-accent)]" />
                    <span className="flex-1 text-sm text-[var(--color-fg)]">
                      {row.label}
                    </span>
                    {/* Tone is uniformly "live" now that S9.5 fulfilled
                        every formerly-deferred row — kept the field around
                        in case a future surface ships in two stages. */}
                    <Badge tone={row.tone === "live" ? "success" : "accent"}>
                      {row.hint}
                    </Badge>
                  </li>
                ))}
              </ul>
            </CardContent>
          </Card>
        </div>
      )}

      <Tabs defaultValue="overview" className="space-y-0">
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="vulnerabilities">Vulnerabilities</TabsTrigger>
          <TabsTrigger value="scans">Scans</TabsTrigger>
          <TabsTrigger value="remediation">Remediation</TabsTrigger>
          <TabsTrigger value="policies">Policies</TabsTrigger>
          {/* S9.5 — new tab. Sits after Policies so the read-only / write
              / async-job ordering reads left → right. */}
          <TabsTrigger value="reports">Reports</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            {/* Real severity legend — FE-API-016 now ships these counts. */}
            <Card accentBar={headlineTone}>
              <CardHeader>
                <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                  Severity breakdown
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-4">
                {isLoading ? (
                  <Skeleton className="h-2 w-full" />
                ) : (
                  <>
                    <SeverityBar counts={severityCounts} />
                    <SeverityLegend counts={severityCounts} />
                  </>
                )}
                <p className="text-xs text-[var(--color-fg-muted)]">
                  Aggregated across every tag's most-recent scan in the
                  workspace. Drill into any repository to see per-tag detail.
                </p>
              </CardContent>
            </Card>

            {/* FE-API-020 — coverage + freshness snapshot. */}
            <CoverageCard />
          </div>
        </TabsContent>

        <TabsContent value="vulnerabilities">
          <VulnerabilitiesTable />
        </TabsContent>

        <TabsContent value="scans">
          <ScanHistoryTable />
        </TabsContent>

        {/* FE-API-017 — remediation rollups grouped by upgrade path. */}
        <TabsContent value="remediation">
          <RemediationTable />
        </TabsContent>

        {/* FE-API-018 — block-on-severity policy editor. */}
        <TabsContent value="policies">
          <ScanPolicyEditor />
        </TabsContent>

        {/* FE-API-019 — async compliance report generation + download. */}
        <TabsContent value="reports">
          <ReportsPanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}
