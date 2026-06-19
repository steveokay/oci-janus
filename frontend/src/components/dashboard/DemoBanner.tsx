/**
 * DemoBanner — slim note above the dashboard explaining which tiles
 * are wired to the real backend and which still use placeholders.
 *
 * As more backend endpoints land, the items in this banner shrink.
 * Sprint 1b: Repositories + Storage + the hero pill + repo/vuln counts
 * are live; Tags, Scans today, Activity, and Top Repos still need
 * dedicated endpoints.
 */
import { Sparkles } from 'lucide-react'

export function DemoBanner() {
  return (
    <div className="flex items-center gap-sm px-md py-sm rounded-sm border border-warning-500/30 bg-warning-100 text-warning-500">
      <Sparkles className="w-4 h-4 shrink-0" aria-hidden="true" />
      <p className="text-label-md font-medium">
        <strong className="font-semibold">Partial demo data.</strong>{' '}
        Repositories, Storage, and the hero are live (
        <code className="font-mono">/api/v1/stats</code>). Tags, Scans
        today, Activity, and Top Repositories still use placeholders —
        Sprint 2 wires the audit + tag-count endpoints.
      </p>
    </div>
  )
}
