import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Boxes, ArrowDownToLine, ShieldAlert } from "lucide-react";
import { StatCard } from "@/components/dashboard/stat-card";
import { StorageCard } from "@/components/dashboard/storage-card";
import { HealthCard } from "@/components/dashboard/health-card";
import { QuickActions } from "@/components/dashboard/quick-actions";
import { AnalyticsCard } from "@/components/dashboard/analytics-card";
import { StorageBreakdownCard } from "@/components/dashboard/storage-breakdown-card";
import { ErrorState } from "@/components/ui/error-state";
import { SeverityBar } from "@/components/security/severity-bar";
import { useStats } from "@/lib/api/stats";
import { useMe } from "@/lib/api/me";
import { formatCompactNumber } from "@/lib/format";
import { useAuthStore } from "@/lib/auth/store";

export const Route = createFileRoute("/_authenticated/")({
  component: DashboardHome,
});

function DashboardHome(): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const { data: me } = useMe();
  const { data, isLoading, isError, refetch } = useStats();

  const greeting = useGreeting();
  // Service-account principals fall back to a non-personal salutation
  // (DSGN-022). The time-of-day greeting + first-name pattern reads as
  // affectionate-toward-a-human; surfacing the SA name as the actor it
  // is, instead of "operator", makes the dashboard feel correct when a
  // CI bot is the one logged in.
  const isServiceAccount = (me?.type ?? "user") === "service_account";
  const saName =
    me?.service_account?.name ?? me?.display_name ?? "Service Account";
  const subjectName = claims?.username ?? "operator";

  return (
    <div className="space-y-8">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Overview
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          {isServiceAccount ? (
            <>Authenticated as {saName}.</>
          ) : (
            <>
              {greeting}, {subjectName}.
            </>
          )}
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Your registry control plane — refreshed every 30 seconds.
        </p>
      </header>

      {isError ? (
        <ErrorState
          title="Couldn't load stats"
          description="The management API is unreachable. Check that the BFF (`registry-management`) is running, then try again."
          onRetry={() => void refetch()}
        />
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
            <StatCard
              label="Repositories"
              icon={<Boxes className="size-4" />}
              value={data?.total_repos}
              loading={isLoading}
              accentBar="accent"
              caption="Across all organizations in this workspace."
            />
            <StorageCard
              used={data?.storage_used_bytes}
              quota={data?.storage_quota_bytes}
              loading={isLoading}
            />
            <StatCard
              label="Pulls / 24h"
              icon={<ArrowDownToLine className="size-4" />}
              value={data?.daily_pulls}
              format={formatCompactNumber}
              loading={isLoading}
              caption="Image pulls served by registry-core in the last 24 hours."
            />
          </div>

          <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
            <StatCard
              label="Vulnerabilities"
              icon={<ShieldAlert className="size-4" />}
              value={data?.vulnerability_count}
              loading={isLoading}
              accentBar={
                (data?.critical_count ?? 0) + (data?.high_count ?? 0) > 0
                  ? "danger"
                  : (data?.vulnerability_count ?? 0) > 0
                    ? "warning"
                    : "accent"
              }
              caption={
                isLoading ? (
                  "Total open findings across the latest scan per tag."
                ) : (
                  // FE-API-016 — the per-severity breakdown lets us show a
                  // mini SeverityBar instead of plain prose, so the tile
                  // reads "where the problem is" at a glance.
                  <div className="mt-1 space-y-1.5">
                    <SeverityBar
                      counts={{
                        CRITICAL: data?.critical_count ?? 0,
                        HIGH: data?.high_count ?? 0,
                        MEDIUM: data?.medium_count ?? 0,
                        LOW: data?.low_count ?? 0,
                      }}
                      className="h-1"
                    />
                    <span>
                      Across the latest scan per tag.
                    </span>
                  </div>
                )
              }
            />
            <HealthCard pct={data?.system_health_pct} loading={isLoading} />
            <div className="md:col-span-1" />
          </div>
        </>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <AnalyticsCard scope="tenant" />
        <StorageBreakdownCard />
      </div>

      <section className="space-y-3">
        <h2 className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Where would you like to go
        </h2>
        <QuickActions />
      </section>
    </div>
  );
}

// Tiny niceity — three-band greeting tied to local hour so the dashboard
// doesn't feel like a robot. Falls back to "Welcome" if Intl is unhappy.
function useGreeting(): string {
  return React.useMemo(() => {
    const h = new Date().getHours();
    if (h < 5) return "Working late";
    if (h < 12) return "Good morning";
    if (h < 18) return "Good afternoon";
    return "Good evening";
  }, []);
}
