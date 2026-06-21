import * as React from "react";
import { Gauge } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useSecurityOverview } from "@/lib/api/security-overview";
import { cn } from "@/lib/utils";

// Beacon — CoverageCard (FE-API-020).
//
// Three stacked stat lines on a single Card: scan coverage (percent +
// scanned-of-total + a thin progress bar), recent 24h scan count, and the
// "days since last scan" gauge. The last one flips into a danger color
// when the gap is more than a week — that's the threshold the design
// brief calls out.
//
// We deliberately don't render the severity counts here: the existing
// SeverityBar card on the same tab already owns that visual real estate.
const STALE_DAYS_THRESHOLD = 7;

export function CoverageCard(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useSecurityOverview();

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load coverage snapshot"
        description="The /security/overview endpoint didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Scan coverage + freshness
          </CardDescription>
          <Gauge className="size-4 text-[var(--color-fg-muted)]" aria-hidden />
        </div>
      </CardHeader>
      <CardContent className="space-y-5 pt-0">
        {isLoading || !data ? (
          <SkeletonBody />
        ) : (
          <>
            <Coverage
              percent={data.scan_coverage.percent}
              scanned={data.scan_coverage.tags_scanned}
              total={data.scan_coverage.tags_total}
            />
            <Recent24h count={data.recent_scans_24h} />
            <DaysSince days={data.days_since_last_scan} />
          </>
        )}
      </CardContent>
    </Card>
  );
}

// Coverage — headline percent + scanned/total + thin progress bar. Bar
// pattern mirrors storage-breakdown-card.tsx so the two surfaces feel
// visually related.
function Coverage({
  percent,
  scanned,
  total,
}: {
  percent: number;
  scanned: number;
  total: number;
}): React.ReactElement {
  // Pin the bar to a 1% minimum so a workspace with even a single scanned
  // tag doesn't render as an invisible sliver. Tags-total == 0 special
  // cases to a fully greyed-out bar.
  const display = total === 0 ? 0 : Math.max(percent, 1);
  return (
    <div>
      <div className="flex items-baseline justify-between gap-2">
        <div className="font-display text-3xl font-medium leading-none tracking-tight tabular-nums">
          {total === 0 ? "—" : `${percent.toFixed(percent < 10 ? 1 : 0)}%`}
        </div>
        <div className="text-xs text-[var(--color-fg-muted)] tabular-nums">
          {scanned.toLocaleString()} of {total.toLocaleString()}{" "}
          {total === 1 ? "tag" : "tags"}
        </div>
      </div>
      <div className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-surface-sunken)]">
        <div
          className="h-full rounded-full bg-[var(--color-accent)] transition-all"
          style={{ width: `${display}%` }}
          aria-hidden
        />
      </div>
      <p className="mt-1.5 text-[11px] text-[var(--color-fg-subtle)]">
        Share of tags with at least one recorded scan.
      </p>
    </div>
  );
}

function Recent24h({ count }: { count: number }): React.ReactElement {
  return (
    <div className="flex items-baseline justify-between gap-3 border-t border-[var(--color-border)] pt-4">
      <div>
        <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Recent scans · 24h
        </div>
        <p className="mt-0.5 text-[11px] text-[var(--color-fg-muted)]">
          New scan runs across the workspace in the last day.
        </p>
      </div>
      <div className="font-display text-2xl font-medium leading-none tabular-nums">
        {count.toLocaleString()}
      </div>
    </div>
  );
}

function DaysSince({ days }: { days: number }): React.ReactElement {
  // -1 sentinel = the tenant has never been scanned. Render "Never" rather
  // than a misleading -1 days.
  const never = days < 0;
  const stale = !never && days > STALE_DAYS_THRESHOLD;
  return (
    <div className="flex items-baseline justify-between gap-3 border-t border-[var(--color-border)] pt-4">
      <div>
        <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Days since last scan
        </div>
        <p className="mt-0.5 text-[11px] text-[var(--color-fg-muted)]">
          {never
            ? "No scans have ever completed in this workspace."
            : stale
              ? "More than a week — schedule a refresh."
              : "Within the past week."}
        </p>
      </div>
      <div
        className={cn(
          "font-display text-2xl font-medium leading-none tabular-nums",
          stale ? "text-[var(--color-danger)]" : "text-[var(--color-fg)]",
        )}
      >
        {never ? "Never" : days.toLocaleString()}
      </div>
    </div>
  );
}

function SkeletonBody(): React.ReactElement {
  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <Skeleton className="h-7 w-24" />
        <Skeleton className="h-1.5 w-full" />
      </div>
      <Skeleton className="h-8 w-full" />
      <Skeleton className="h-8 w-full" />
    </div>
  );
}
