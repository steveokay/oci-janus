/**
 * AnimatedNumber — tween from 0 (or previous value) to `value` on mount
 * and on subsequent updates.
 *
 * Used on the dashboard stat tiles to give the page first-paint a
 * "comes alive" moment. Tween length is short (~600ms ease-out) so the
 * number feels responsive — anything longer reads as slow.
 *
 * If the user has `prefers-reduced-motion`, we skip the animation and
 * render the value directly. Same with values < ~10 where the count-up
 * would look gimmicky.
 */
import { useEffect, useRef, useState } from 'react'
import { animate } from 'framer-motion'

export interface AnimatedNumberProps {
  value: number
  /** Custom format for the displayed string (e.g. comma separators). */
  format?: (n: number) => string
  /** Duration in seconds. Defaults to 0.6. */
  duration?: number
}

export function AnimatedNumber({
  value,
  format,
  duration = 0.6,
}: AnimatedNumberProps) {
  const [display, setDisplay] = useState(0)
  const previous = useRef(0)

  useEffect(() => {
    const reduce =
      typeof window !== 'undefined' &&
      window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    // Very small values look gimmicky tween'd; just render directly.
    if (reduce || value < 10) {
      setDisplay(value)
      previous.current = value
      return
    }
    const from = previous.current
    const controls = animate(from, value, {
      duration,
      ease: 'easeOut',
      onUpdate: (latest) => setDisplay(latest),
      onComplete: () => {
        previous.current = value
      },
    })
    return () => controls.stop()
  }, [value, duration])

  const rendered = format
    ? format(display)
    : Math.round(display).toLocaleString()
  return <>{rendered}</>
}
