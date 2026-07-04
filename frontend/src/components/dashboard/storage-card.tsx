import * as React from "react";
import { HardDrive } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBytes } from "@/lib/format";

interface StorageCardProps {
  used?: number;
  quota?: number;
  loading?: boolean;
}

// Beacon — Storage card. Used-of-quota bar is the primary affordance, with
// the numeric breakdown below. Color shifts to amber > 75% and rose > 90%
// (handled inside the Progress primitive).
export function StorageCard({
  used,
  quota,
  loading,
}: StorageCardProps): React.ReactElement {
  const pct =
    typeof used === "number" && typeof quota === "number" && quota > 0
      ? Math.min(100, Math.round((used / quota) * 100))
      : 0;

  // Tone the surrounding card border based on the same threshold so the eye
  // catches over-quota tenants from the dashboard hero.
  const accentBar =
    pct >= 90 ? ("danger" as const)
    : pct >= 75 ? ("warning" as const)
    : ("accent" as const);

  return (
    <Card accentBar={accentBar} className="col-span-1">
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Storage
          </CardDescription>
          <HardDrive className="size-4 text-[var(--color-fg-subtle)]" />
        </div>
        {loading ? (
          <Skeleton className="mt-1 h-7 w-48" />
        ) : (
          <CardTitle className="!text-xl font-display !font-medium">
            {/* Unified "used / total" form (matches repositories-table +
                repository-header); dropped the "of" wording. */}
            {formatBytes(used ?? 0)}{" "}
            <span className="text-base font-normal text-[var(--color-fg-muted)]">
              / {formatBytes(quota ?? 0)}
            </span>
          </CardTitle>
        )}
      </CardHeader>
      <CardContent className="space-y-3">
        {loading ? (
          <Skeleton className="h-2 w-full" />
        ) : (
          <Progress value={pct} aria-label={`${pct}% of quota used`} />
        )}
        <div className="flex items-center justify-between text-xs text-[var(--color-fg-muted)]">
          <span>{loading ? "—" : `${pct}% used`}</span>
          <span>
            {loading ? "—" : `${formatBytes(Math.max(0, (quota ?? 0) - (used ?? 0)))} remaining`}
          </span>
        </div>
      </CardContent>
    </Card>
  );
}
