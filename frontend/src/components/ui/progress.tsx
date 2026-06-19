import * as React from "react";
import * as ProgressPrimitive from "@radix-ui/react-progress";
import { cn } from "@/lib/utils";

interface ProgressProps
  extends React.ComponentPropsWithoutRef<typeof ProgressPrimitive.Root> {
  // 0..100. Beacon shifts the indicator tone above 75% and 90% thresholds.
  value: number;
  // Override the auto-tone. Useful when the consumer already knows the
  // semantic meaning (e.g. quota usage vs. scan progress).
  tone?: "accent" | "success" | "warning" | "danger";
}

function toneFromValue(v: number): NonNullable<ProgressProps["tone"]> {
  if (v >= 90) return "danger";
  if (v >= 75) return "warning";
  return "accent";
}

const toneClass: Record<NonNullable<ProgressProps["tone"]>, string> = {
  accent: "bg-[var(--color-accent)]",
  success: "bg-[var(--color-success)]",
  warning: "bg-[var(--color-warning)]",
  danger: "bg-[var(--color-danger)]",
};

// Beacon — Progress. Used for storage quotas, scan progress, etc.
// The indicator transitions on value change so the bar feels alive
// without being distracting.
export const Progress = React.forwardRef<
  React.ElementRef<typeof ProgressPrimitive.Root>,
  ProgressProps
>(function Progress({ className, value, tone, ...props }, ref) {
  const resolved = tone ?? toneFromValue(value);
  const clamped = Math.max(0, Math.min(100, value));
  return (
    <ProgressPrimitive.Root
      ref={ref}
      className={cn(
        "relative h-2 w-full overflow-hidden rounded-full bg-[var(--color-surface-sunken)]",
        className,
      )}
      value={clamped}
      {...props}
    >
      <ProgressPrimitive.Indicator
        className={cn(
          "h-full w-full origin-left transition-transform duration-700 ease-out",
          toneClass[resolved],
        )}
        style={{ transform: `translateX(-${100 - clamped}%)` }}
      />
    </ProgressPrimitive.Root>
  );
});
