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
import { ComingSoon } from "@/components/common/coming-soon";
import { Badge } from "@/components/ui/badge";
import { Link } from "@tanstack/react-router";
import { useStats } from "@/lib/api/stats";
import {
  SeverityBar,
  SeverityLegend,
} from "@/components/security/severity-bar";
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
                {[
                  {
                    label: "Inspect every open CVE across the workspace",
                    api: "FE-API-014",
                  },
                  {
                    label: "Audit recent scan runs and triggers",
                    api: "FE-API-015",
                  },
                  {
                    label: "Find remediation paths grouped by base image",
                    api: "FE-API-017",
                  },
                  {
                    label: "Configure block-on-severity scan policies",
                    api: "FE-API-018",
                  },
                ].map((row) => (
                  <li
                    key={row.api}
                    className="flex items-center gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2"
                  >
                    <ArrowRight className="size-3.5 text-[var(--color-accent)]" />
                    <span className="flex-1 text-sm text-[var(--color-fg)]">
                      {row.label}
                    </span>
                    <Badge tone="accent" className="font-mono">
                      {row.api}
                    </Badge>
                  </li>
                ))}
              </ul>
              <p className="mt-4 text-xs text-[var(--color-fg-muted)]">
                Today: explore per-tag scan results from{" "}
                <Link
                  to="/repositories"
                  className="text-[var(--color-accent)] hover:underline"
                >
                  any repository
                </Link>
                .
              </p>
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

            <ComingSoon
              apiId="FE-API-020"
              title="Scan coverage + freshness"
              description="The remaining overview tiles need the snapshot endpoint — they're more than what the stats call returns today."
              highlights={[
                "Scan coverage % — tags scanned ÷ tags total",
                "Days since last scan, with worst-offender repos called out",
                "Recent scan run count over the last 24h / 7d",
              ]}
            />
          </div>
        </TabsContent>

        <TabsContent value="vulnerabilities">
          <ComingSoon
            apiId="FE-API-014"
            title="Every open CVE, every affected image"
            description="One row per CVE with severity, primary URL, and the list of (repo, tag, digest) triples it affects. Searchable + filterable by severity, with `affected images` expandable per row."
            highlights={[
              "Severity filter chip row (CRITICAL / HIGH / MEDIUM / LOW)",
              "Expandable affected-images list per CVE",
              "Direct deep-links into each tag's detail page",
            ]}
          />
        </TabsContent>

        <TabsContent value="scans">
          <ComingSoon
            apiId="FE-API-015"
            title="Scan run timeline"
            description="Every scan run across the workspace, newest first. Group by repo or by trigger (push / manual / scheduled), filter by status, drill into each run."
            highlights={[
              "Trigger source — push, manual rescan, or scheduled",
              "Status filter (complete, running, failed)",
              "Per-run severity_counts at a glance",
            ]}
          />
        </TabsContent>

        <TabsContent value="remediation">
          <ComingSoon
            apiId="FE-API-017"
            title="Actionable remediation queue"
            description="Findings rolled up by base image upgrade path. `Bump alpine:3.18 → 3.20 to close 4 CVEs across 7 images` — the kind of action a platform team can actually take in a sprint."
            highlights={[
              "Grouped by upgrade path, not by raw CVE list",
              "Affected (repo, tag) pairs per recommendation",
              "Estimated CVE count closed per upgrade",
            ]}
          />
        </TabsContent>

        <TabsContent value="policies">
          <ComingSoon
            apiId="FE-API-018"
            title="Scan policy editor"
            description="Auto-scan on push toggles, fail-on-severity gates that reject pushes with criticals, exempt-CVE list, and scanner version pin. The same backing table the scanner consults on every push."
            highlights={[
              "Block-on-severity gate (CRITICAL / HIGH / MEDIUM)",
              "Exempt-CVE allowlist with reason field",
              "Scanner plugin + version pin",
            ]}
          />
        </TabsContent>
      </Tabs>
    </div>
  );
}
