// REDESIGN-001 Phase 4.2.e — Security › Overview tab.
//
// This is the default landing tab for /security. The "Open findings" card
// + CoverageCard top row live in the parent layout (they're always
// visible), so this tab is the secondary read — the severity-breakdown
// card with bar + legend. Lightest possible content so the operator gets
// useful info immediately without waiting on a table fetch.
//
// Why a card here at all (instead of an empty Outlet): the parent's top
// row is dense numerics; the Overview tab gives the same data a second
// presentation (full legend with counts per severity) and reserves grid
// space for additional summary widgets in later phases.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  SeverityBar,
  SeverityLegend,
} from "@/components/security/severity-bar";
import { useStats } from "@/lib/api/stats";
import type { ScanResult } from "@/lib/api/types";

export const Route = createFileRoute("/_authenticated/security/overview")({
  component: OverviewTab,
});

function OverviewTab(): React.ReactElement {
  // useStats is a TanStack Query hook; the second consumer here (parent
  // layout is the first) hits the cache so there's no extra request.
  const { data, isLoading } = useStats();

  // Defensive `?? 0` so a stale BFF without the new fields renders an
  // empty bar rather than NaN.
  const severityCounts: ScanResult["severity_counts"] = {
    CRITICAL: data?.critical_count ?? 0,
    HIGH: data?.high_count ?? 0,
    MEDIUM: data?.medium_count ?? 0,
    LOW: data?.low_count ?? 0,
  };
  const vulnCount = data?.vulnerability_count ?? 0;
  const criticalOrHigh =
    (data?.critical_count ?? 0) + (data?.high_count ?? 0);
  const headlineTone =
    criticalOrHigh > 0
      ? ("danger" as const)
      : vulnCount > 0
        ? ("warning" as const)
        : ("accent" as const);

  return (
    // Grid wrapper retained so a future overview surface (e.g. a "top 5
    // offending repos" widget) drops in without re-jigging the layout.
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
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
            Aggregated across every tag's most-recent scan in the workspace.
            Drill into any repository to see per-tag detail.
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
