import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Boxes, ArrowDownToLine, ShieldAlert } from "lucide-react";
import { StatCard } from "@/components/dashboard/stat-card";
import { StorageCard } from "@/components/dashboard/storage-card";
import { HealthCard } from "@/components/dashboard/health-card";
import { QuickActions } from "@/components/dashboard/quick-actions";
import { AnalyticsCard } from "@/components/dashboard/analytics-card";
import { StorageBreakdownCard } from "@/components/dashboard/storage-breakdown-card";
import { FirstSteps } from "@/components/dashboard/first-steps";
import { ErrorState } from "@/components/ui/error-state";
import { SeverityBar } from "@/components/security/severity-bar";
import { useStats } from "@/lib/api/stats";
import { useMe } from "@/lib/api/me";
import { useWorkspace } from "@/lib/api/workspace";
import { formatCompactNumber } from "@/lib/format";
import { useAuthStore } from "@/lib/auth/store";

export const Route = createFileRoute("/_authenticated/")({
  component: DashboardHome,
});

function DashboardHome(): React.ReactElement {
  const claims = useAuthStore((s) => s.claims);
  const { data: me } = useMe();
  const { data, isLoading, isError, error, refetch } = useStats();
  const { data: workspace } = useWorkspace();
  const navigate = useNavigate();

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

  // DSGN-005 — first-run guidance. When stats load and report zero
  // repos, swap the stat row for the FirstSteps walkthrough. The route
  // also owns the "first-image-arrived" transition: we latch on the
  // first poll that flips total_repos > 0 so the success state holds
  // long enough to render before navigation. Once latched, push the
  // operator into /repositories so they land on the repo they just
  // created instead of staring at a stale empty dashboard.
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
          error={error}
          onRetry={() => void refetch()}
        />
      ) : isEmptyTenant ? (
        // First-run path: drop the stat row + the per-severity row in
        // favour of the FirstSteps walkthrough. The Analytics +
        // StorageBreakdown cards below still render — they degrade
        // gracefully to their own empty states — and QuickActions
        // shows up muted below so it's clearly a secondary affordance.
        <FirstSteps workspace={workspace} firstRepoSeen={firstRepoSeen} />
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

      <section
        className={
          // DSGN-005 — when the tenant hasn't pushed anything yet, the
          // QuickActions tiles link to mostly-empty pages. They stay
          // available as a "go explore" affordance but are visually
          // demoted (muted, smaller heading) so the FirstSteps card
          // stack above remains the clear primary path.
          isEmptyTenant
            ? "space-y-2 opacity-70"
            : "space-y-3"
        }
      >
        <h2 className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          {isEmptyTenant ? "Or explore the dashboard" : "Where would you like to go"}
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
