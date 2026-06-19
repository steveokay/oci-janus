/**
 * Avatar — deterministic gradient tile from any string.
 *
 * Hash the input → pick one of 12 OKLCH gradient pairs → render as a
 * rounded square with the first letter on top. Used wherever we'd
 * otherwise render "single letter on a grey square" — repo list,
 * detail hero, pinned panel, command palette, etc. Same name always
 * gets the same gradient, so `dev/webapp` looks the same everywhere
 * it appears.
 *
 * Twelve pairs is enough variety that a typical 20-repo workspace
 * doesn't immediately produce dupes, while staying within a tasteful
 * palette range (no clashing reds-and-greens).
 */
import { cn } from '@/lib/utils/cn'

/**
 * djb2 hash — small, deterministic, stable across reloads. Avoids the
 * need for a crypto hash for what's a pure UI decision.
 */
function hash(s: string): number {
  let h = 5381
  for (let i = 0; i < s.length; i++) {
    h = (h * 33) ^ s.charCodeAt(i)
  }
  return Math.abs(h)
}

/**
 * 12 OKLCH gradient pairs — tasteful, low-saturation, similar luminance
 * so any pair plays well next to any other in a list. Sorted by hue
 * so palette feels designed rather than random.
 */
const PALETTE: [string, string][] = [
  ['oklch(0.78 0.16 25)',  'oklch(0.66 0.20 40)'],   // peach → coral
  ['oklch(0.80 0.14 55)',  'oklch(0.70 0.17 75)'],   // amber → honey
  ['oklch(0.78 0.14 95)',  'oklch(0.68 0.16 115)'],  // olive → moss
  ['oklch(0.77 0.16 145)', 'oklch(0.66 0.18 160)'],  // sage → jade
  ['oklch(0.76 0.14 195)', 'oklch(0.66 0.16 215)'],  // teal → ocean
  ['oklch(0.75 0.14 235)', 'oklch(0.65 0.17 255)'],  // sky → cobalt
  ['oklch(0.73 0.16 275)', 'oklch(0.62 0.19 290)'],  // indigo → violet
  ['oklch(0.74 0.17 310)', 'oklch(0.65 0.19 325)'],  // orchid → magenta
  ['oklch(0.78 0.15 345)', 'oklch(0.68 0.17 360)'],  // rose → crimson
  ['oklch(0.76 0.10 245)', 'oklch(0.66 0.13 275)'],  // slate → indigo
  ['oklch(0.78 0.10 165)', 'oklch(0.68 0.12 200)'],  // mint → teal
  ['oklch(0.78 0.13 75)',  'oklch(0.68 0.16 115)'],  // amber → olive
]

const SIZE_CLASSES = {
  xs: 'w-5 h-5 rounded-xs text-[10px]',
  sm: 'w-6 h-6 rounded-xs text-label-sm',
  md: 'w-8 h-8 rounded-sm text-label-md',
  lg: 'w-10 h-10 rounded-sm text-body-sm',
  xl: 'w-12 h-12 rounded-md text-body-md',
} as const

export interface AvatarProps {
  /** String to hash + display the first letter of. Usually `org/repo`. */
  seed: string
  /** Override label — by default we use the first character of `seed`. */
  label?: string
  size?: keyof typeof SIZE_CLASSES
  className?: string
}

export function Avatar({ seed, label, size = 'md', className }: AvatarProps) {
  const [from, to] = PALETTE[hash(seed) % PALETTE.length]
  const text = (label ?? seed.split('/').pop() ?? seed).charAt(0).toUpperCase()

  return (
    <span
      aria-hidden="true"
      className={cn(
        'inline-flex items-center justify-center shrink-0 font-semibold text-white shadow-xs',
        SIZE_CLASSES[size],
        className,
      )}
      style={{
        backgroundImage: `linear-gradient(135deg, ${from} 0%, ${to} 100%)`,
      }}
    >
      {text}
    </span>
  )
}
