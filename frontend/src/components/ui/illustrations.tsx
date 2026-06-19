/**
 * Minimal line illustrations for empty states.
 *
 * Each is a tiny SVG drawn in `currentColor` so the parent can tint
 * via Tailwind (`text-primary`, `text-on-surface-subtle`, etc). Sized
 * to ~128px square by default; consumers can override via className.
 *
 * Why hand-rolled SVGs instead of Higgsfield: empty-state art needs to
 * be consistent in line weight, palette, and motif. Iterating on six
 * generations to match each other is more expensive than drawing six
 * 30-line SVGs once.
 */
import type { SVGProps } from 'react'
import { cn } from '@/lib/utils/cn'

interface IllustrationProps extends SVGProps<SVGSVGElement> {
  className?: string
}

const BASE_CLASS = 'w-32 h-32 text-primary'

/** Stack of boxes with one open at the top — repos empty state. */
export function ReposIllustration({ className, ...rest }: IllustrationProps) {
  return (
    <svg
      viewBox="0 0 128 128"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn(BASE_CLASS, className)}
      {...rest}
    >
      <path d="M24 84l40 22 40-22" />
      <path d="M24 64l40 22 40-22" />
      <path d="M24 44l40-22 40 22-40 22-40-22z" />
      <path d="M64 44l-40 22" opacity="0.35" />
      <path d="M64 44l40 22" opacity="0.35" />
      <circle cx="64" cy="44" r="2.5" fill="currentColor" />
    </svg>
  )
}

/** Tag silhouette with a small dot. */
export function TagsIllustration({ className, ...rest }: IllustrationProps) {
  return (
    <svg
      viewBox="0 0 128 128"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn(BASE_CLASS, className)}
      {...rest}
    >
      <path d="M28 70l30-30a6 6 0 0 1 4.2-1.8H88a6 6 0 0 1 6 6v25.8a6 6 0 0 1-1.8 4.2L62 104a6 6 0 0 1-8.5 0L28 78.5a6 6 0 0 1 0-8.5z" />
      <circle cx="78" cy="50" r="4" />
      <path d="M44 84l8 8" opacity="0.4" />
      <path d="M52 76l8 8" opacity="0.4" />
    </svg>
  )
}

/** Key floating with a small spark. */
export function ApiKeyIllustration({ className, ...rest }: IllustrationProps) {
  return (
    <svg
      viewBox="0 0 128 128"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn(BASE_CLASS, className)}
      {...rest}
    >
      <circle cx="46" cy="64" r="18" />
      <circle cx="46" cy="64" r="6" />
      <path d="M64 64h44" />
      <path d="M88 64v10" />
      <path d="M100 64v8" />
      <path d="M90 28l4 8 8 4-8 4-4 8-4-8-8-4 8-4z" opacity="0.5" />
    </svg>
  )
}

/** Pin floating diagonally. */
export function PinIllustration({ className, ...rest }: IllustrationProps) {
  return (
    <svg
      viewBox="0 0 128 128"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn(BASE_CLASS, className)}
      {...rest}
    >
      <path d="M82 36l-30 30" />
      <path d="M58 32l40 40" />
      <path d="M68 22l40 40" />
      <path d="M52 66L36 96" />
      <circle cx="36" cy="96" r="2" fill="currentColor" />
      <path d="M20 20l8 8" opacity="0.4" />
      <path d="M104 100l8 8" opacity="0.4" />
    </svg>
  )
}

/** Magnifier over a grid — no-match state. */
export function NoMatchIllustration({ className, ...rest }: IllustrationProps) {
  return (
    <svg
      viewBox="0 0 128 128"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn(BASE_CLASS, className)}
      {...rest}
    >
      <path d="M28 28h32M28 44h32M28 60h22" opacity="0.35" />
      <circle cx="78" cy="74" r="22" />
      <path d="M94 90l16 16" />
      <path d="M70 74h16" opacity="0.5" />
    </svg>
  )
}

/** Friendly error illustration — broken cable. */
export function ErrorIllustration({ className, ...rest }: IllustrationProps) {
  return (
    <svg
      viewBox="0 0 128 128"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn(BASE_CLASS, className)}
      {...rest}
    >
      <rect x="20" y="48" width="34" height="32" rx="4" />
      <rect x="74" y="48" width="34" height="32" rx="4" />
      <path d="M54 64h6M68 64h6" />
      <path d="M62 58l4 12" />
      <path d="M30 80v10M44 80v10" opacity="0.4" />
      <path d="M84 80v10M98 80v10" opacity="0.4" />
    </svg>
  )
}
