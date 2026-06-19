/**
 * Demo dashboard data — stand-in until Sprint 1b wires real /api/v1/stats.
 *
 * Numbers are deliberately plausible (not all 0s, not all huge) so the
 * UI can be designed and reviewed without staring at em-dashes. The
 * `DemoBanner` component at the top of the dashboard tells the user
 * these aren't real.
 *
 * Everything here is a pure constant — no fetch, no random — so visual
 * reviews + screenshots are stable across reloads.
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

export type ActivityKind = 'push' | 'scan' | 'invite' | 'create' | 'delete'

export interface DemoActivity {
  id: string
  actor: string
  verb: string
  object: string
  /** Human-friendly relative time. Sprint 1b switches to a real timestamp + intl. */
  when: string
  kind: ActivityKind
  /** Optional severity tag — only set on scan results today. */
  severity?: 'low' | 'medium' | 'high' | 'critical'
}

export const DEMO_ACTIVITY: DemoActivity[] = [
  { id: '1', actor: 'admin',   verb: 'pushed',        object: 'webapp:v2.1.3',         when: '2 min ago',  kind: 'push' },
  { id: '2', actor: 'scanner', verb: 'flagged',       object: '2 CVEs in api-gw:v3',   when: '8 min ago',  kind: 'scan', severity: 'high' },
  { id: '3', actor: 'admin',   verb: 'invited',       object: 'bob@acme.io',           when: '23 min ago', kind: 'invite' },
  { id: '4', actor: 'admin',   verb: 'created repo',  object: 'api-gateway',           when: '1 hr ago',   kind: 'create' },
  { id: '5', actor: 'ci-bot',  verb: 'pushed',        object: 'backend:nightly',       when: '3 hrs ago',  kind: 'push' },
  { id: '6', actor: 'admin',   verb: 'deleted tag',   object: 'cdn:legacy',            when: '5 hrs ago',  kind: 'delete' },
]

export interface DemoRepo {
  name: string
  tags: number
  deltaPct: number
}

export const DEMO_TOP_REPOS: DemoRepo[] = [
  { name: 'webapp',      tags: 24, deltaPct: 12 },
  { name: 'api-gateway', tags: 18, deltaPct: 8 },
  { name: 'backend',     tags: 12, deltaPct: -2 },
  { name: 'cdn',         tags: 6,  deltaPct: 3 },
  { name: 'worker',      tags: 4,  deltaPct: 0 },
]
