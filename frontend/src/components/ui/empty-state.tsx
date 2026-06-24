import * as React from "react";
import { cn } from "@/lib/utils";

interface EmptyStateProps {
  icon?: React.ReactNode;
  title: string;
  // description accepts a string (the common case) or a ReactNode so
  // callers can inline secondary affordances inside the body without
  // duplicating the EmptyState surface (DSGN-019). When a ReactNode is
  // passed we still wrap it in the same <p> element so the surrounding
  // type ramp + colour stays consistent.
  description?: React.ReactNode;
  action?: React.ReactNode;
  // secondaryAction renders next to the primary action — typically a
  // "Read the docs" link for the surface so a first-time visitor can
  // learn what this is for without leaving the page (DSGN-007).
  secondaryAction?: React.ReactNode;
  className?: string;
}

// Beacon — EmptyState. Used by every list/table when there's no data.
// The dotted-grid background hints at "blank canvas" without being noisy.
export function EmptyState({
  icon,
  title,
  description,
  action,
  secondaryAction,
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
          // Wrapper is a <div> rather than a <p> so callers passing a
          // ReactNode with nested block elements (DSGN-019 sibling-tab
          // links sit inside a flex/block child) don't produce invalid
          // HTML. Strings still render with the same type ramp.
          <div className="text-sm text-[var(--color-fg-muted)]">
            {description}
          </div>
        ) : null}
      </div>
      {action || secondaryAction ? (
        <div className="flex flex-wrap items-center justify-center gap-3 pt-2">
          {action}
          {secondaryAction}
        </div>
      ) : null}
    </div>
  );
}
