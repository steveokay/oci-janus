import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Boxes, ArrowDownToLine, ShieldAlert, RefreshCw } from "lucide-react";
import { StatCard } from "@/components/dashboard/stat-card";
import { StorageCard } from "@/components/dashboard/storage-card";
import { HealthCard } from "@/components/dashboard/health-card";
import { QuickActions } from "@/components/dashboard/quick-actions";
import { AnalyticsCard } from "@/components/dashboard/analytics-card";
import { StorageBreakdownCard } from "@/components/dashboard/storage-breakdown-card";
import { FirstStepsStrip } from "@/components/dashboard/first-steps-strip";
import { ErrorState } from "@/components/ui/error-state";
import { SeverityBar } from "@/components/security/severity-bar";
import { Button } from "@/components/ui/button";
import { useStats } from "@/lib/api/stats";
import { useMe } from "@/lib/api/me";
import { useWorkspace } from "@/lib/api/workspace";
import { formatCompactNumber, formatRelativeDate } from "@/lib/format";
import { useAuthStore } from "@/lib/auth/store";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_authenticated/")({
  component: DashboardHome,
});

function DashboardHome(): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const { data: me, isLoading: meLoading } = useMe();
  const { data, isLoading, isError, error, refetch, isFetching, dataUpdatedAt } =
    useStats();
  const { data: workspace } = useWorkspace();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  // Manual refresh — the dashboard auto-polls every 30s, but operators often
  // want an immediate re-pull after an action (push, scan, GC). Invalidate the
  // dashboard query-key prefixes so every panel refetches together, not just
  // the stats tiles. The `["stats"]` prefix also covers the storage-breakdown
  // query (keyed `["stats","storage"]`); `["analytics"]` covers the two
  // analytics charts.
  const handleRefresh = React.useCallback((): void => {
    void queryClient.invalidateQueries({ queryKey: ["stats"] });
    void queryClient.invalidateQueries({ queryKey: ["analytics"] });
  }, [queryClient]);

  // REDESIGN-001 Phase 4.3 — first-run onboarding auto-redirect.
  //
  // WHAT: When the authenticated caller has `onboarding_complete === false`,
  // bounce them to `/getting-started` instead of rendering the dashboard.
  //
  // WHY (render-time vs beforeLoad): a `beforeLoad` redirect would depend on
  // the React-Query cache being warm at navigation time. For a freshly logged-
  // in user (the exact audience for this redirect), the `me` cache is still
  // cold on first dashboard hit, so `beforeLoad` would either miss the
  // redirect or have to do its own fetch. Reading `useMe()` at render time
  // and redirecting via `useNavigate` reliably waits for the value to arrive.
  //
  // WHY (skip rules):
  //   - `type === "service_account"`: SA principals don't onboard (the BE
  //     returns 403 from POST /users/me/onboarding/complete), and they're
  //     CI bots that should land on the dashboard.
  //   - `onboarding_complete === undefined`: defensive — a pre-rollout
  //     backend without the column would omit the field. Treat undefined
  //     as "done" so legacy users aren't trapped in a loop.
  //
  // WHY (replace: true): prevents the browser back button from bouncing the
  // user back into the dashboard mid-onboarding, which would either re-trigger
  // the redirect (infinite loop) or — worse — flash the dashboard briefly.
  const isServiceAccountForOnboarding =
    (me?.type ?? "user") === "service_account";
  const shouldOnboard =
    me !== undefined &&
    !isServiceAccountForOnboarding &&
    me.onboarding_complete === false;
  React.useEffect(() => {
    if (shouldOnboard) {
      void navigate({ to: "/getting-started", replace: true });
    }
  }, [shouldOnboard, navigate]);

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

  // DSGN-005 v2 — hybrid first-run guidance. The original DSGN-005
  // swapped the stat row for a 4-card vertical FirstSteps stack at
  // zero repos, but operators reported losing useful signals at zero
  // (workspace health, quota allocated, "0/100 GB used"). The refined
  // behaviour keeps the full stat row visible always and surfaces a
  // single compact horizontal "Get started" strip below it when the
  // tenant has no repos yet. The route still owns the
  // "first-image-arrived" transition: we latch on the first poll that
  // flips total_repos > 0 so the success state holds long enough to
  // render before navigation, then push the operator into
  // /repositories so they land on the repo they just created instead
  // of staring at a stale empty dashboard.
  const totalRepos = data?.total_repos;
  const isEmptyTenant = !isLoading && !isError && totalRepos === 0;
  const sawEmpty = React.useRef(false);
  const [firstRepoSeen, setFirstRepoSeen] = React.useState(false);
  React.useEffect(() => {
    if (totalRepos === 0) {
      sawEmpty.current = true;
      return;
    }
    if (sawEmpty.current && totalRepos !== undefined && totalRepos > 0) {
      // Latch the success state first so the green check + "opening…"
      // message has a beat to render, then navigate. 800ms is the same
      // budget we use elsewhere for transient success affordances.
      setFirstRepoSeen(true);
      const t = window.setTimeout(() => {
        void navigate({ to: "/repositories" });
      }, 800);
      return () => window.clearTimeout(t);
    }
    return;
  }, [totalRepos, navigate]);

  // REDESIGN-001 Phase 4.3 — gate the render on the onboarding redirect.
  //
  // WHAT: While `useMe()` is still loading, or while a first-run redirect is
  // in flight, return an empty container instead of the dashboard.
  //
  // WHY (placement after all hooks): the rules of hooks require every hook
  // call to happen in the same order each render. This early return therefore
  // sits AFTER every `useState` / `useRef` / `useEffect` above it, so we never
  // skip a hook on the redirecting render. The cost is a single extra render
  // pass for first-run users; the benefit is no dashboard flash before the
  // wizard appears.
  if (meLoading || shouldOnboard) {
    return <div className="space-y-8" aria-busy="true" />;
  }

  return (
    <div className="space-y-8">
      <header className="flex flex-col gap-1">
        <div className="flex items-start justify-between gap-4">
          <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
            Overview
          </p>
          {/* Manual refresh control — spins while any dashboard query is in
              flight and surfaces when the data was last pulled. */}
          <div className="flex items-center gap-2">
            {dataUpdatedAt > 0 ? (
              <span className="hidden text-xs text-[var(--color-fg-subtle)] sm:inline">
                Updated{" "}
                {formatRelativeDate(new Date(dataUpdatedAt).toISOString())}
              </span>
            ) : null}
            <Button
              variant="ghost"
              size="icon"
              onClick={handleRefresh}
              disabled={isFetching}
              aria-label="Refresh dashboard"
              title="Refresh dashboard"
            >
              <RefreshCw
                className={cn("size-4", isFetching && "animate-spin")}
              />
            </Button>
          </div>
        </div>
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
          error={error}
          onRetry={() => void refetch()}
        />
      ) : (
        <>
          {/* All five KPI tiles in one row at lg+ so the dashboard
              reads at a glance. At md the grid wraps to 2-col (3+2);
              at sm everything stacks. */}
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-5">
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
                    <span>Across the latest scan per tag.</span>
                  </div>
                )
              }
            />
            <HealthCard pct={data?.system_health_pct} loading={isLoading} />
          </div>

          {/* DSGN-005 v2 — compact "Get started" strip slots below the
              stat row when the tenant has no repos yet. Once
              total_repos > 0, the strip vanishes; the rest of the
              dashboard is unchanged. */}
          {isEmptyTenant ? (
            <FirstStepsStrip
              workspace={workspace}
              firstRepoSeen={firstRepoSeen}
            />
          ) : null}
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
