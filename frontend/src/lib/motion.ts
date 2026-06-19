/**
 * motion — shared animation tokens for framer-motion across the app.
 *
 * Keep the vocabulary small so the app feels coherent — three durations
 * (fast / base / slow) + two easings (out for incoming, spring for
 * dialogs). Add new entries here when you want consistency; reach for
 * raw values only when a specific spec demands it.
 */
import type { Transition, Variants } from 'framer-motion'

export const FAST = 0.15
export const BASE = 0.22
export const SLOW = 0.35

export const easeOut: Transition = { duration: BASE, ease: 'easeOut' }
export const easeOutFast: Transition = { duration: FAST, ease: 'easeOut' }
export const spring: Transition = { type: 'spring', stiffness: 320, damping: 28 }

/** Page-level fade-in on route mount. */
export const fadeIn: Variants = {
  initial: { opacity: 0 },
  animate: { opacity: 1, transition: easeOut },
}

/** Container + child variants for a stagger reveal. Items use `listItem`. */
export const listContainer: Variants = {
  initial: { opacity: 1 },
  animate: {
    opacity: 1,
    transition: { staggerChildren: 0.03, delayChildren: 0.04 },
  },
}

export const listItem: Variants = {
  initial: { opacity: 0, y: 6 },
  animate: { opacity: 1, y: 0, transition: easeOutFast },
}

/** Modal/dialog content variants — scale-in with a soft spring. */
export const dialogContent: Variants = {
  initial: { opacity: 0, scale: 0.96 },
  animate: { opacity: 1, scale: 1, transition: spring },
  exit: { opacity: 0, scale: 0.97, transition: easeOutFast },
}
