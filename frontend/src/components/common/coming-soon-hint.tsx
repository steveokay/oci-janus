import * as React from "react";
import { Sparkles } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// Beacon — ComingSoonHint.
//
// Smaller sibling of <ComingSoon>. Used for *inline* "this affordance is
// coming" hints next to live controls — e.g. a disabled button row, an
// existing card footer, a row action. The full <ComingSoon> panel is the
// right call when the surface itself is the only thing being communicated.
// This component is the right call when the surface has live content and
// we're calling out one missing piece.
//
// Looks like:  ✨ Coming soon · FE-API-025 · short rationale string
interface ComingSoonHintProps {
  apiId: string;
  children: React.ReactNode;
  className?: string;
}

export function ComingSoonHint({
  apiId,
  children,
  className,
}: ComingSoonHintProps): React.ReactElement {
  return (
    <div
      className={cn(
        "inline-flex flex-wrap items-center gap-2 rounded-md border border-dashed",
        "border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)]",
        "px-2.5 py-1.5 text-xs text-[var(--color-fg-muted)]",
        className,
      )}
    >
      <Sparkles
        className="size-3 shrink-0 text-[var(--color-highlight)]"
        aria-hidden
      />
      <span className="font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Coming soon
      </span>
      <Badge tone="accent" className="font-mono">
        {apiId}
      </Badge>
      <span className="text-[var(--color-fg-muted)]">{children}</span>
    </div>
  );
}
