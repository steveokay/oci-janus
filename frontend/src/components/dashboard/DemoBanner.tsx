/**
 * DemoBanner — slim warning at the top of any screen still using fake data.
 *
 * Honest signal that the numbers/people/repos below are stand-ins. Slim
 * amber strip with an info glyph; meant to be conspicuous-but-not-loud.
 * Comes off the page entirely the moment Sprint 1b wires real data.
 */
import { Sparkles } from 'lucide-react'

export function DemoBanner() {
  return (
    <div className="flex items-center gap-sm px-md py-sm rounded-sm border border-warning-500/30 bg-warning-100 text-warning-500">
      <Sparkles className="w-4 h-4 shrink-0" aria-hidden="true" />
      <p className="text-label-md font-medium">
        Demo data — the numbers, activity, and repos below are placeholders.
        Sprint 1b wires real values from{' '}
        <code className="font-mono">/api/v1/stats</code>.
      </p>
    </div>
  )
}
