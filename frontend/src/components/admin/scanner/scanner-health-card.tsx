import * as React from "react";
import { Activity, AlertTriangle, Clock, Hourglass } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { ErrorState } from "@/components/ui/error-state";
import {
  useScannerHealth,
  type AdminScannerHealth,
} from "@/lib/api/admin-scanners";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// Beacon — ScannerHealthCard (FE-API-047).
//
// One-row "worker pool" card pinned above the adapter grid. Polls every 10s
// (default in useScannerHealth). The card is intentionally short — same
// vertical rhythm as a single StatCard — so it doesn't dominate the page
// when the scanner is healthy and idle.
export function ScannerHealthCard(): React.ReactElement {
  const q = useScannerHealth();

  if (q.isError) {
    return (
      <ErrorState
        title="Couldn't load scanner health"
        description="The /admin/scanners/health endpoint didn't answer. Confirm SCANNER_GRPC_ADDR is set on the management BFF."
        onRetry={() => void q.refetch()}
      />
    );
  }

  if (q.isLoading || !q.data) {
    return (
      <div className="grid grid-cols-1 gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4 shadow-[var(--shadow-card)] sm:grid-cols-4">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
      </div>
    );
  }

  return <Body health={q.data} />;
}

function Body({ health }: { health: AdminScannerHealth }): React.ReactElement {
  const healthy = health.healthy;
  return (
    <div
      className={cn(
        "relative grid grid-cols-1 gap-4 overflow-hidden rounded-lg border bg-[var(--color-surface)] p-4 shadow-[var(--shadow-card)] sm:grid-cols-4",
        healthy
          ? "border-[var(--color-border)]"
          : "border-[var(--color-danger)]/40",
      )}
      role="status"
      aria-live="polite"
    >
      {/* Status dot + active adapter — primary signal on the left */}
      <div className="flex items-center gap-3">
        <span
          aria-hidden
          className={cn(
            "size-2 shrink-0 rounded-full",
            healthy
              ? "bg-[var(--color-success)]"
              : "bg-[var(--color-danger)]",
          )}
        />
        <div className="min-w-0">
          <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Worker pool
          </div>
          <div className="mt-0.5 truncate text-sm font-medium text-[var(--color-fg)]">
            {health.active_adapter_name || "—"}
            {health.active_adapter_version ? (
              <span className="ml-1.5 font-mono text-xs text-[var(--color-fg-muted)]">
                @{health.active_adapter_version}
              </span>
            ) : null}
          </div>
        </div>
      </div>

      {/* Queue depth tile */}
      <Tile
        icon={<Hourglass className="size-3.5" />}
        label="Queue depth"
        value={health.queue_depth.toLocaleString()}
      />

      {/* In-flight count tile */}
      <Tile
        icon={<Activity className="size-3.5" />}
        label="In flight"
        value={health.in_flight_count.toLocaleString()}
      />

      {/* Last successful scan timestamp on the right */}
      <div className="flex items-start gap-2 sm:justify-end">
        <div className="text-left sm:text-right">
          <div className="flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)] sm:justify-end">
            <Clock className="size-3" />
            Last successful scan
          </div>
          <div className="mt-0.5 text-sm text-[var(--color-fg)]">
            {health.last_successful_scan_at ? (
              <span
                title={formatAbsoluteDate(health.last_successful_scan_at)}
              >
                {formatRelativeDate(health.last_successful_scan_at)}
              </span>
            ) : (
              <span className="text-[var(--color-fg-subtle)]">Never</span>
            )}
          </div>
        </div>
      </div>

      {/* Unhealthy banner. Stays inline so a sick scanner is visible
          without forcing a separate modal — the colored border + this
          chip give the operator the full picture at a glance. */}
      {!healthy ? (
        <div className="sm:col-span-4">
          <Badge tone="danger" className="gap-1.5">
            <AlertTriangle className="size-3" />
            Scanner isn't producing results
          </Badge>
        </div>
      ) : null}
    </div>
  );
}

function Tile({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
}): React.ReactElement {
  return (
    <div>
      <div className="flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        {icon}
        {label}
      </div>
      <div className="mt-0.5 font-display text-2xl font-medium leading-none tracking-tight tabular-nums">
        {value}
      </div>
    </div>
  );
}
