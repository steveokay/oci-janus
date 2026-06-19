/**
 * relativeTime — turn a Date or ISO string into a "2 days ago" string
 * using `Intl.RelativeTimeFormat`.
 *
 * Tables and feeds use this everywhere we'd otherwise show a raw
 * timestamp. The exact second is rarely useful at a glance — a coarse
 * "5 min ago" / "yesterday" / "2 weeks ago" carries more meaning.
 *
 * Negative diffs (future dates) are handled correctly because we pass
 * the signed diff to `RelativeTimeFormat`, which renders "in 2 days"
 * for forward times.
 */
const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })

export function relativeTime(input: Date | string): string {
  const d = typeof input === 'string' ? new Date(input) : input
  if (Number.isNaN(d.getTime())) return ''

  const diffSec = Math.round((Date.now() - d.getTime()) / 1000)
  // Each branch picks the largest unit whose absolute value < the
  // next-larger unit's threshold — keeps the output short and idiomatic.
  if (Math.abs(diffSec) < 60)            return rtf.format(-diffSec, 'second')
  if (Math.abs(diffSec) < 60 * 60)       return rtf.format(-Math.round(diffSec / 60), 'minute')
  if (Math.abs(diffSec) < 60 * 60 * 24)  return rtf.format(-Math.round(diffSec / 3600), 'hour')
  if (Math.abs(diffSec) < 60 * 60 * 24 * 30) return rtf.format(-Math.round(diffSec / 86400), 'day')
  if (Math.abs(diffSec) < 60 * 60 * 24 * 365) return rtf.format(-Math.round(diffSec / (86400 * 30)), 'month')
  return rtf.format(-Math.round(diffSec / (86400 * 365)), 'year')
}
