import * as React from "react";
import { Card, CardContent, CardDescription, CardHeader } from "@/components/ui/card";
import { AnimatedNumber } from "@/components/ui/animated-number";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

interface StatCardProps {
  label: string;
  value?: number;
  format?: (n: number) => string;
  icon: React.ReactNode;
  // Optional contextual line under the hero number (e.g. delta, target, unit).
  caption?: React.ReactNode;
  loading?: boolean;
  accentBar?: React.ComponentProps<typeof Card>["accentBar"];
  className?: string;
}

// Beacon — StatCard. The dashboard hero tile. Big serif hero number,
// uppercase small-caps label above, contextual line below. The accent
// top-border keys to status so a wall of cards reads "calm / attention /
// problem" before the eye has parsed the numbers.
export function StatCard({
  label,
  value,
  format,
  icon,
  caption,
  loading,
  accentBar,
  className,
}: StatCardProps): React.ReactElement {
  return (
    <Card accentBar={accentBar} className={cn("flex flex-col", className)}>
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            {label}
          </CardDescription>
          <span className="text-[var(--color-fg-subtle)]" aria-hidden>
            {icon}
          </span>
        </div>
      </CardHeader>
      <CardContent className="pt-0 pb-5">
        {/* Skeleton keys on `loading` only — the old `|| value === undefined`
            arm rendered a permanent skeleton when the API legitimately omits
            a stat. A settled-but-absent value renders an em dash instead so
            the card reads "no data", not "still loading". */}
        {loading ? (
          <Skeleton className="h-10 w-32" />
        ) : value === undefined ? (
          <div className="font-display text-4xl font-medium leading-none tracking-tight text-[var(--color-fg)]">
            —
          </div>
        ) : (
          <div className="font-display text-4xl font-medium leading-none tracking-tight text-[var(--color-fg)]">
            <AnimatedNumber value={value} format={format} />
          </div>
        )}
        {caption ? (
          <div className="mt-3 text-xs text-[var(--color-fg-muted)]">
            {caption}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
