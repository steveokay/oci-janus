import * as React from "react";
import { Sparkles, ArrowRight } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

interface ComingSoonProps {
  apiId: string;          // e.g. "FE-API-014"
  title: string;          // e.g. "Workspace-wide vulnerabilities"
  description: string;    // one-paragraph explanation of what's coming
  highlights?: string[];  // optional bullet list of what the surface will show
  className?: string;
}

// Beacon — ComingSoon. Used everywhere we have a backend gap. Better than
// a generic empty state because it tells the operator exactly *which* API
// is missing, with the same tag the backend uses in status.md. That way a
// product owner reviewing the UI can map any gap straight to a tracker line.
export function ComingSoon({
  apiId,
  title,
  description,
  highlights,
  className,
}: ComingSoonProps): React.ReactElement {
  return (
    <Card className={cn("relative overflow-hidden", className)}>
      {/* Subtle dotted-grid wash so the panel doesn't read as "empty" */}
      <span
        aria-hidden
        className="bg-dot-grid pointer-events-none absolute inset-0 opacity-40"
      />
      <CardHeader className="relative">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            <Sparkles className="-mt-0.5 mr-1 inline size-3 text-[var(--color-highlight)]" />
            Coming soon
          </CardDescription>
          <Badge tone="accent" className="font-mono">
            {apiId}
          </Badge>
        </div>
        <h3 className="mt-1 font-display text-xl font-medium tracking-tight">
          {title}
        </h3>
      </CardHeader>
      <CardContent className="relative space-y-4">
        <p className="text-sm text-[var(--color-fg-muted)]">{description}</p>
        {highlights && highlights.length > 0 ? (
          <ul className="space-y-1.5">
            {highlights.map((h) => (
              <li
                key={h}
                className="flex items-start gap-2 text-sm text-[var(--color-fg)]"
              >
                <ArrowRight className="mt-0.5 size-3.5 shrink-0 text-[var(--color-accent)]" />
                <span>{h}</span>
              </li>
            ))}
          </ul>
        ) : null}
      </CardContent>
    </Card>
  );
}
