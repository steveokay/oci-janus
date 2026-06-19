import * as React from "react";
import { SEVERITY_ORDER, type SeverityKey } from "@/lib/api/scan";
import type { ScanResult } from "@/lib/api/types";
import { cn } from "@/lib/utils";

// Beacon — SeverityBar. A stacked horizontal bar showing the distribution
// of vulnerability severities for a scan. Each segment width is proportional
// to its share of the total; tiny segments still render a 2px minimum so
// "1 CRITICAL" never disappears into a giant LOW pool.
//
// The design direction explicitly calls for this over a pie chart — easier
// to compare across rows, doesn't lie about exact ratios.

const SEGMENT_CLASS: Record<SeverityKey, string> = {
  CRITICAL: "bg-[var(--color-sev-critical)]",
  HIGH: "bg-[var(--color-sev-high)]",
  MEDIUM: "bg-[var(--color-sev-medium)]",
  LOW: "bg-[var(--color-sev-low)]",
};

interface SeverityBarProps {
  counts: ScanResult["severity_counts"];
  className?: string;
}

export function SeverityBar({
  counts,
  className,
}: SeverityBarProps): React.ReactElement {
  const total = SEVERITY_ORDER.reduce(
    (sum, k) => sum + (counts[k] ?? 0),
    0,
  );

  if (total === 0) {
    return (
      <div
        className={cn(
          "h-2 w-full rounded-full bg-[var(--color-success)]/20",
          className,
        )}
        aria-label="No vulnerabilities"
      />
    );
  }

  return (
    <div
      role="img"
      aria-label={`${total} findings: ${SEVERITY_ORDER.map(
        (k) => `${counts[k] ?? 0} ${k.toLowerCase()}`,
      ).join(", ")}`}
      className={cn(
        "flex h-2 w-full overflow-hidden rounded-full bg-[var(--color-surface-sunken)]",
        className,
      )}
    >
      {SEVERITY_ORDER.map((k) => {
        const count = counts[k] ?? 0;
        if (count === 0) return null;
        const pct = (count / total) * 100;
        return (
          <span
            key={k}
            className={cn("h-full transition-all", SEGMENT_CLASS[k])}
            style={{
              // 2px floor so single-count segments don't vanish.
              width: `max(${pct}%, 2px)`,
            }}
          />
        );
      })}
    </div>
  );
}

interface SeverityLegendProps {
  counts: ScanResult["severity_counts"];
  className?: string;
}

// Compact legend showing each severity with its count. Pairs with SeverityBar.
export function SeverityLegend({
  counts,
  className,
}: SeverityLegendProps): React.ReactElement {
  return (
    <div className={cn("flex flex-wrap gap-x-4 gap-y-1.5", className)}>
      {SEVERITY_ORDER.map((k) => {
        const count = counts[k] ?? 0;
        return (
          <div key={k} className="flex items-center gap-1.5 text-xs">
            <span
              className={cn("size-2 rounded-sm", SEGMENT_CLASS[k])}
              aria-hidden
            />
            <span className="font-medium text-[var(--color-fg)]">
              {count.toLocaleString()}
            </span>
            <span className="text-[var(--color-fg-muted)]">{k.toLowerCase()}</span>
          </div>
        );
      })}
    </div>
  );
}
