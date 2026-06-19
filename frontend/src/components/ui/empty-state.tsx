import * as React from "react";
import { cn } from "@/lib/utils";

interface EmptyStateProps {
  icon?: React.ReactNode;
  title: string;
  description?: string;
  action?: React.ReactNode;
  className?: string;
}

// Beacon — EmptyState. Used by every list/table when there's no data.
// The dotted-grid background hints at "blank canvas" without being noisy.
export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
}: EmptyStateProps): React.ReactElement {
  return (
    <div
      className={cn(
        "bg-dot-grid flex flex-col items-center justify-center gap-3 rounded-lg",
        "border border-dashed border-[var(--color-border)]",
        "px-6 py-16 text-center",
        className,
      )}
    >
      {icon ? (
        <div
          className="grid size-12 place-items-center rounded-full bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
          aria-hidden
        >
          {icon}
        </div>
      ) : null}
      <div className="max-w-md space-y-1">
        <h3 className="text-base font-semibold text-[var(--color-fg)]">
          {title}
        </h3>
        {description ? (
          <p className="text-sm text-[var(--color-fg-muted)]">{description}</p>
        ) : null}
      </div>
      {action ? <div className="pt-2">{action}</div> : null}
    </div>
  );
}
