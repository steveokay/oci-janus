/**
 * Demo dashboard data — stand-in for stat tiles whose backend
 * endpoints don't exist yet (Tags + Scans today). Live tiles
 * (Repositories, Storage) come from `useStats`; demo tiles are
 * tagged with a small "Demo" pill by `StatCard` so the user can
 * see at a glance which numbers aren't real.
 *
 * Everything here is a pure constant — no fetch, no random — so
 * visual reviews + screenshots are stable across reloads.
 */

export interface DemoStat {
  label: string
  value: number
  /** Percent change vs prior period. Positive is up, negative is down. */
  deltaPct: number
  /** 12 data points for the sparkline (oldest → newest). */
  trend: number[]
  /** Semantic colour key — picks one of the design-token palettes. */
  tone: 'primary' | 'info' | 'success' | 'warning'
  /** Lucide icon name; resolved in StatCard so we don't ship every icon. */
  iconKey: 'package' | 'tag' | 'scan' | 'upload' | 'storage'
  /** Optional unit shown after the value (e.g. "GB"). */
  unit?: string
}

export const DEMO_STATS: DemoStat[] = [
  {
    label: 'Repositories',
    value: 24,
    deltaPct: 12,
    trend: [3, 5, 4, 6, 8, 7, 9, 12, 14, 17, 21, 24],
    tone: 'primary',
    iconKey: 'package',
  },
  {
    label: 'Tags',
    value: 156,
    deltaPct: 8,
    trend: [80, 95, 100, 110, 115, 120, 130, 140, 145, 150, 153, 156],
    tone: 'info',
    iconKey: 'tag',
  },
  {
    label: 'Scans today',
    value: 12,
    deltaPct: 33,
    trend: [2, 4, 3, 5, 6, 8, 7, 9, 10, 11, 12, 12],
    tone: 'success',
    iconKey: 'scan',
  },
  {
    label: 'Storage',
    value: 1.2,
    unit: 'GB',
    deltaPct: 8,
    // Slow growth over the last 12 buckets — storage rarely shrinks.
    trend: [0.82, 0.88, 0.93, 0.97, 1.01, 1.04, 1.07, 1.10, 1.13, 1.15, 1.18, 1.20],
    tone: 'warning',
    iconKey: 'storage',
  },
  {
    label: 'Pushes today',
    value: 8,
    deltaPct: -10,
    trend: [12, 11, 10, 9, 11, 12, 10, 9, 8, 9, 8, 8],
    tone: 'info',
    iconKey: 'upload',
  },
]

// DEMO_ACTIVITY and DEMO_TOP_REPOS were removed in the 1f honesty pass:
// ActivityFeed and TopRepos panels are gone from the dashboard. Tag
// counts and scan counts stay as demo for now — see DEMO_STATS above.
