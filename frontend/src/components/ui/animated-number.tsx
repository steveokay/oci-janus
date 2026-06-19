import * as React from "react";
import { animate, useMotionValue, useTransform, motion } from "framer-motion";
import { cn } from "@/lib/utils";

interface AnimatedNumberProps {
  value: number;
  // Optional formatter; default is locale string. Passes the in-flight
  // numeric value, so callers can render units, bytes, etc.
  format?: (n: number) => string;
  durationMs?: number;
  className?: string;
}

// Beacon — AnimatedNumber. Counts up from 0 (or from the previous value)
// to `value` over `durationMs`. Used for hero KPI cards.
//
// The animation uses framer-motion's `animate` so we get a real spring/tween
// instead of a `setInterval` race. We track the previous value to allow
// re-animating when stats refetch.
export function AnimatedNumber({
  value,
  format = (n) => Math.round(n).toLocaleString(),
  durationMs = 600,
  className,
}: AnimatedNumberProps): React.ReactElement {
  const motionVal = useMotionValue(0);
  const rendered = useTransform(motionVal, (n) => format(n));

  React.useEffect(() => {
    const controls = animate(motionVal, value, {
      duration: durationMs / 1000,
      ease: [0.22, 1, 0.36, 1],
    });
    return () => controls.stop();
    // We deliberately exclude motionVal — framer-motion returns a stable ref.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, durationMs]);

  return (
    <motion.span
      className={cn("tabular-nums", className)}
      aria-label={format(value)}
    >
      {rendered}
    </motion.span>
  );
}
