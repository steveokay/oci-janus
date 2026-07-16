import * as React from "react";
import { cn } from "@/lib/utils";

// Beacon — SigningCoverageBar. Single-value progress bar for a repo's
// signed-tag coverage, color-coded so weak repos are scannable at a glance.
// Mirrors SeverityBar's token + 2px-floor conventions.

// Coverage thresholds: >=90% is healthy (success), >=50% is a warning,
// anything lower is danger. Picked to match the design's "green/amber/red"
// scannability at a glance.
const GOOD = 0.9;
const WARN = 0.5;

// toneFor maps a clamped 0..1 coverage ratio to the bar-fill and text
// token classes for its severity band.
function toneFor(pct: number): { bar: string; label: string } {
  if (pct >= GOOD)
    return {
      bar: "bg-[var(--color-success)]",
      label: "text-[var(--color-success)]",
    };
  if (pct >= WARN)
    return {
      bar: "bg-[var(--color-warning)]",
      label: "text-[var(--color-warning)]",
    };
  return {
    bar: "bg-[var(--color-danger)]",
    label: "text-[var(--color-danger)]",
  };
}

interface SigningCoverageBarProps {
  // Fractional coverage in the 0..1 range (e.g. 0.95 for 95%). Clamped
  // defensively so out-of-range backend values can't break the layout.
  pct: number;
  // Absolute signed-tag count, shown in the trailing "signed/total" label.
  signed: number;
  // Absolute total-tag count.
  total: number;
  className?: string;
}

export function SigningCoverageBar({
  pct,
  signed,
  total,
  className,
}: SigningCoverageBarProps): React.ReactElement {
  // Clamp defensively — a stray >1 or <0 ratio would otherwise overflow the
  // track or invert the fill width.
  const clamped = Math.max(0, Math.min(1, pct));
  const tone = toneFor(clamped);
  const pctText = `${Math.round(clamped * 100)}%`;
  return (
    <div className={cn("flex items-center gap-2", className)}>
      <div
        role="img"
        aria-label={`${pctText} signed (${signed} of ${total} tags)`}
        className="h-2 w-24 overflow-hidden rounded-full bg-[var(--color-surface-sunken)]"
      >
        <span
          className={cn("block h-full transition-all", tone.bar)}
          // 2px floor so a non-zero-but-tiny coverage never vanishes.
          style={{ width: `max(${clamped * 100}%, 2px)` }}
        />
      </div>
      <span className={cn("tabular-nums text-xs font-medium", tone.label)}>
        {signed}/{total}
      </span>
    </div>
  );
}
