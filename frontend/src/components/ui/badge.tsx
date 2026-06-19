import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

// Beacon — Badge. Status pills and severity chips share this primitive.
// Severities map to the dedicated severity tokens (see index.css).
const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium",
  {
    variants: {
      tone: {
        neutral:
          "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
        accent:
          "border-[var(--color-accent-border)] bg-[var(--color-accent-subtle)] text-[var(--color-accent)]",
        success:
          "border-[var(--color-success)]/30 bg-[var(--color-success)]/10 text-[var(--color-success)]",
        warning:
          "border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 text-[var(--color-warning)]",
        danger:
          "border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 text-[var(--color-danger)]",
        critical:
          "border-[var(--color-sev-critical)]/30 bg-[var(--color-sev-critical)]/10 text-[var(--color-sev-critical)]",
        high:
          "border-[var(--color-sev-high)]/30 bg-[var(--color-sev-high)]/10 text-[var(--color-sev-high)]",
        medium:
          "border-[var(--color-sev-medium)]/30 bg-[var(--color-sev-medium)]/10 text-[var(--color-sev-medium)]",
        low:
          "border-[var(--color-sev-low)]/30 bg-[var(--color-sev-low)]/10 text-[var(--color-sev-low)]",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {
  dot?: boolean;
  pulse?: boolean;
}

export function Badge({
  className,
  tone,
  dot,
  pulse,
  children,
  ...props
}: BadgeProps): React.ReactElement {
  return (
    <span className={cn(badgeVariants({ tone, className }))} {...props}>
      {dot ? (
        <span
          className={cn(
            "size-1.5 rounded-full bg-current",
            pulse && "beacon-pulse",
          )}
          aria-hidden
        />
      ) : null}
      {children}
    </span>
  );
}
