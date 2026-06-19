/**
 * Dashboard — honest version.
 *
 * What's on the page:
 *   1. Hero — warm gradient + time-of-day photo (theme-aware: dark
 *      mode hides the photo and the white veil).
 *   2. Four stat tiles:
 *        - Repositories (live, no fake sparkline)
 *        - Tags        (DEMO — small badge says so)
 *        - Scans today (DEMO — small badge says so)
 *        - Storage     (live, no fake sparkline)
 *   3. PinnedRepos — user-controlled, persisted to localStorage.
 *      Replaces the previous demo TopRepos panel.
 *   4. Quickstart — copy-pasteable docker push for first-image flow.
 *
 * Removed since the last iteration:
 *   * Demo banner (per-tile badges replace it).
 *   * Activity feed (was fully demo and clicking links toasted lies).
 *   * Sparklines on live tiles (no time series available from /stats).
 *
 * Sprint 1f honesty pass — keeps live data visually distinct from demo
 * data, and stops the page from inventing activity events.
 */
import { createFileRoute } from '@tanstack/react-router'
import { TriangleAlert } from 'lucide-react'
import { HeroCard } from '@/components/dashboard/HeroCard'
import { PinnedRepos } from '@/components/dashboard/PinnedRepos'
import { Quickstart } from '@/components/dashboard/Quickstart'
import { StatCard } from '@/components/dashboard/StatCard'
import { DEMO_STATS } from '@/lib/demo/dashboardData'
import { useStats } from '@/lib/api/hooks/useStats'
import { formatBytes } from '@/lib/format/bytes'

export const Route = createFileRoute('/_authenticated/dashboard')({
  staticData: { crumb: 'Dashboard' },
  component: DashboardPage,
})

function DashboardPage() {
  const { data: stats, isLoading, isError, error } = useStats()

  const storage = stats ? formatBytes(stats.storage_used_bytes) : undefined
  const demoTagsTile = DEMO_STATS.find((s) => s.label === 'Tags')!
  const demoScansTile = DEMO_STATS.find((s) => s.label === 'Scans today')!

  return (
    <div className="p-xl space-y-lg">
      {isError && <StatsErrorNote error={error} />}

      <HeroCard
        loading={isLoading}
        repoCount={stats?.total_repos}
        tagCount={undefined /* no endpoint yet */}
        vulnerabilityCount={stats?.vulnerability_count}
        systemHealthPct={stats?.system_health_pct}
      />

      {/* Row 1 — stat tiles. Live tiles intentionally render no sparkline
          / delta; demo tiles keep theirs + carry a small "Demo" chip. */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-lg">
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
          demo
        />
        <StatCard
          label={demoScansTile.label}
          value={demoScansTile.value}
          iconKey={demoScansTile.iconKey}
          tone={demoScansTile.tone}
          trend={demoScansTile.trend}
          deltaPct={demoScansTile.deltaPct}
          unit={demoScansTile.unit}
          demo
        />
        <StatCard
          loading={isLoading}
          label="Storage"
          value={storage?.value ?? 0}
          unit={storage?.unit}
          iconKey="storage"
          tone="warning"
        />
      </div>

      {/* Row 2 — pinned repos (full width). When the user has pins this
          fills with their selections; when they don't, the empty state
          carries enough onboarding to be a useful surface on its own. */}
      <PinnedRepos />

      {/* Row 3 — quickstart, full width. */}
      <Quickstart />
    </div>
  )
}

function StatsErrorNote({ error }: { error: unknown }) {
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
