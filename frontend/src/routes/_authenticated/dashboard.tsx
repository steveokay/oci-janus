/**
 * Dashboard — Sprint 1b: live data wiring on top of the Sprint 1a bento.
 *
 * Live tiles (from `useStats` → /api/v1/stats):
 *   * Repositories  — total_repos
 *   * Storage       — storage_used_bytes (formatted to the best unit)
 *
 * Demo tiles (no backend endpoint yet):
 *   * Tags          — needs metadata RPC for tag counts across repos
 *   * Scans today   — needs an audit query for today's scan events
 *
 * Live hero data:
 *   * repoCount     — from /api/v1/stats
 *   * vulnCount     — from /api/v1/stats (total open, not just critical)
 *   * health pill   — derived from vulnerabilityCount + system_health_pct
 *
 * Activity feed and Top Repositories still use demo data — Sprint 2 will
 * wire those once the audit-events query API + per-repo activity stats
 * land in registry-audit and registry-metadata respectively.
 *
 * Error handling: a backend failure on /api/v1/stats degrades to skeletons
 * on the live tiles. We surface a single subtle inline error message
 * rather than a full-page error so demo content remains usable.
 */
import { createFileRoute } from '@tanstack/react-router'
import { TriangleAlert } from 'lucide-react'
import { ActivityFeed } from '@/components/dashboard/ActivityFeed'
import { DemoBanner } from '@/components/dashboard/DemoBanner'
import { HeroCard } from '@/components/dashboard/HeroCard'
import { Quickstart } from '@/components/dashboard/Quickstart'
import { StatCard } from '@/components/dashboard/StatCard'
import { TopRepos } from '@/components/dashboard/TopRepos'
import {
  DEMO_ACTIVITY,
  DEMO_STATS,
  DEMO_TOP_REPOS,
} from '@/lib/demo/dashboardData'
import { useStats } from '@/lib/api/hooks/useStats'
import { formatBytes } from '@/lib/format/bytes'

export const Route = createFileRoute('/_authenticated/dashboard')({
  staticData: { crumb: 'Dashboard' },
  component: DashboardPage,
})

function DashboardPage() {
  const { data: stats, isLoading, isError, error } = useStats()

  // Format storage into a sensible unit only when we actually have a
  // number — during loading we render the skeleton tile, not "0 B".
  const storage = stats ? formatBytes(stats.storage_used_bytes) : undefined

  // The two demo tiles that still need real backend endpoints. Kept as a
  // tight reference rather than slicing the array so it's obvious which
  // tiles are placeholders.
  const demoTagsTile   = DEMO_STATS.find((s) => s.label === 'Tags')!
  const demoScansTile  = DEMO_STATS.find((s) => s.label === 'Scans today')!

  return (
    <div className="p-xl space-y-lg">
      <DemoBanner />

      {isError && <StatsErrorNote error={error} />}

      <HeroCard
        loading={isLoading}
        repoCount={stats?.total_repos}
        tagCount={undefined /* no endpoint yet — renders "—" */}
        vulnerabilityCount={stats?.vulnerability_count}
        systemHealthPct={stats?.system_health_pct}
      />

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-lg">
        {/* Row 1 — 4 stat tiles. Repositories + Storage are live; Tags +
            Scans today still use demo data with trend + delta intact. */}
        <StatCard
          loading={isLoading}
          label="Repositories"
          value={stats?.total_repos ?? 0}
          iconKey="package"
          tone="primary"
        />
        <StatCard
          label={demoTagsTile.label}
          value={demoTagsTile.value}
          iconKey={demoTagsTile.iconKey}
          tone={demoTagsTile.tone}
          trend={demoTagsTile.trend}
          deltaPct={demoTagsTile.deltaPct}
          unit={demoTagsTile.unit}
        />
        <StatCard
          label={demoScansTile.label}
          value={demoScansTile.value}
          iconKey={demoScansTile.iconKey}
          tone={demoScansTile.tone}
          trend={demoScansTile.trend}
          deltaPct={demoScansTile.deltaPct}
          unit={demoScansTile.unit}
        />
        <StatCard
          loading={isLoading}
          label="Storage"
          value={storage?.value ?? 0}
          unit={storage?.unit}
          iconKey="storage"
          tone="warning"
        />

        {/* Row 2-3 — demo activity, top repos, quickstart. */}
        <div className="sm:col-span-2 lg:col-span-2 lg:row-span-2">
          <ActivityFeed items={DEMO_ACTIVITY} />
        </div>
        <div className="sm:col-span-2 lg:col-span-2">
          <TopRepos repos={DEMO_TOP_REPOS} />
        </div>
        <div className="sm:col-span-2 lg:col-span-2">
          <Quickstart />
        </div>
      </div>
    </div>
  )
}

/**
 * Inline error note shown when `/api/v1/stats` fails. We deliberately
 * don't take over the page — demo content is still useful, the user can
 * keep working, and the live tiles fall back to skeletons until the
 * automatic refetch succeeds.
 */
function StatsErrorNote({ error }: { error: unknown }) {
  // We don't surface the raw error message — could include sensitive
  // server output. The user gets a generic message + a hint to retry.
  void error
  return (
    <div className="flex items-center gap-sm px-md py-sm rounded-sm border border-danger-500/30 bg-danger-100 text-danger-500">
      <TriangleAlert className="w-4 h-4 shrink-0" aria-hidden="true" />
      <p className="text-label-md font-medium">
        Couldn't reach the stats endpoint. Live tiles will retry shortly.
      </p>
    </div>
  )
}
