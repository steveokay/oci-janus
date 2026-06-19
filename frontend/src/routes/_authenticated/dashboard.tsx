/**
 * Dashboard — Sprint 1a shell + Sprint 1a-visual upgrade.
 *
 * Bento layout, top-down:
 *   1. DemoBanner — honest "this data isn't real yet"
 *   2. HeroCard — warm gradient welcome, status pill, CTAs
 *   3. Stats row — 4 tiles with icon + value + sparkline
 *   4. ActivityFeed (tall, right rail) + TopRepos + Quickstart
 *
 * All numbers and rows come from `dashboardData.ts`. Sprint 1b swaps
 * those constants for TanStack Query hooks against the real API. The
 * component tree itself doesn't need to change when that happens —
 * each child takes its data via props.
 */
import { createFileRoute } from '@tanstack/react-router'
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

export const Route = createFileRoute('/_authenticated/dashboard')({
  staticData: { crumb: 'Dashboard' },
  component: DashboardPage,
})

function DashboardPage() {
  return (
    <div className="p-xl space-y-lg">
      <DemoBanner />

      <HeroCard
        repoCount={DEMO_TOP_REPOS.length + 19 /* keep in sync with stats */}
        tagCount={156}
        criticalCount={0}
      />

      {/* Bento grid: 4 cols on lg+. Row 1 holds 4 stat tiles (incl. Storage);
          Row 2 has Activity (col-span-2, row-span-2) on the left, TopRepos
          on the right; Row 3 puts Quickstart under TopRepos with Activity
          continuing on the left. */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-lg">
        {DEMO_STATS.slice(0, 4).map((stat) => (
          <StatCard key={stat.label} stat={stat} />
        ))}
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
