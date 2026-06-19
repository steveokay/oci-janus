import * as React from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "./button";
import { cn } from "@/lib/utils";

interface ErrorStateProps {
  title?: string;
  description?: string;
  onRetry?: () => void;
  className?: string;
}

// Beacon — ErrorState. Inline (never modal) per the design direction.
// The "you can retry" CTA is the load-bearing piece; the title is for context.
export function ErrorState({
  title = "Something went wrong",
  description = "We couldn't load this. Try again — if it keeps failing, the service may be unreachable.",
  onRetry,
  className,
}: ErrorStateProps): React.ReactElement {
  return (
    <div
      role="alert"
      className={cn(
        "flex flex-col items-start gap-3 rounded-lg border border-[var(--color-danger)]/30",
        "bg-[var(--color-danger)]/5 px-5 py-4",
        className,
      )}
    >
      <div className="flex items-center gap-2 text-[var(--color-danger)]">
        <AlertTriangle className="size-4" aria-hidden />
        <span className="text-sm font-semibold">{title}</span>
      </div>
      <p className="text-sm text-[var(--color-fg-muted)]">{description}</p>
      {onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry}>
          Retry
        </Button>
      ) : null}
    </div>
  );
}
