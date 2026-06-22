import * as React from "react";
import { ShieldCheck, Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { useGCRuns, type GCRun } from "@/lib/api/admin-gc";
import { formatAbsoluteDate, formatBytes, formatRelativeDate } from "@/lib/format";

// Beacon — Admin Retention tile (S11 Slice 5).
//
// Sits below the GCCard on /admin/tenants. Same data shape (`gc_runs` rows
// streamed by FE-API-032) filtered client-side for the retention modes —
// "retention" (soft-delete) and "retention_grace" (finaliser).
//
// Why client-side filter: the gc service's ListRuns gRPC doesn't accept a
// `mode` filter today. Tracking the proper fix in status.md → REM-013 gap
// 2. For dev / small-deployment use the client filter is fine — page 1 of
// /admin/gc/runs returns the latest 50 ordered by completed_at DESC, which
// is comfortably above the typical retention cadence.
//
// What we render:
//
//   - Counts strip — runs in last 24h + last 7d, totalled across both
//     retention modes
//   - Recent runs table — last 10 retention rows with mode pill +
//     status pill + manifests + bytes + triggered_by
//
// The card is empty-state-friendly: a deployment with no retention runs
// shows a hint rather than a blank rectangle.

const RETENTION_MODES = new Set(["retention", "retention_grace"]);
const RECENT_LIMIT = 10;

export function RetentionCard(): React.ReactElement {
  // Fetch a larger page than the GCCard so the filter has more rows to
  // pick from. Backend caps at 200 — see admin_gc.go.
  const runs = useGCRuns({ limit: 100 });

  // Flatten the infinite-query pages and keep only retention modes.
  const retentionRuns = React.useMemo(() => {
    const flat = runs.data?.pages.flatMap((p) => p.runs) ?? [];
    return flat.filter((r) => RETENTION_MODES.has(r.mode));
  }, [runs.data]);

  // Bucketed counts. 24h is "last day"; 7d is "last week". Backend rows
  // use `completed_at` (when terminal) and `requested_at` (otherwise);
  // prefer requested_at so an in-flight queued run still counts.
  const { count24h, count7d } = React.useMemo(() => {
    const now = Date.now();
    const day = 24 * 60 * 60 * 1000;
    let c24 = 0;
    let c7 = 0;
    for (const r of retentionRuns) {
      const stampIso = r.requested_at ?? r.completed_at ?? r.started_at;
      if (!stampIso) continue;
      const t = new Date(stampIso).getTime();
      if (Number.isNaN(t)) continue;
      const age = now - t;
      if (age <= day) c24++;
      if (age <= 7 * day) c7++;
    }
    return { count24h: c24, count7d: c7 };
  }, [retentionRuns]);

  if (runs.isError) {
    return (
      <ErrorState
        title="Couldn't load retention runs"
        description="The GC runs endpoint didn't answer. Try again, or check the BFF logs."
        onRetry={() => void runs.refetch()}
      />
    );
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Housekeeping · Retention
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Per-repo and per-org retention policy executor runs. Counts
              include both the soft-delete pass and the 7-day grace finaliser.
            </p>
          </div>
          <ShieldCheck
            className="size-4 text-[var(--color-fg-muted)]"
            aria-hidden
          />
        </div>
      </CardHeader>
      <CardContent className="space-y-5">
        <Counts loading={runs.isLoading} count24h={count24h} count7d={count7d} />
        <RecentRuns
          loading={runs.isLoading}
          runs={retentionRuns.slice(0, RECENT_LIMIT)}
        />
      </CardContent>
    </Card>
  );
}

// Counts — two stat lines for last 24h / last 7d. Matches the visual
// rhythm of CoverageCard so the dashboard reads as one family.
function Counts({
  loading,
  count24h,
  count7d,
}: {
  loading: boolean;
  count24h: number;
  count7d: number;
}): React.ReactElement {
  return (
    <div className="grid grid-cols-2 gap-3">
      <Stat label="Last 24h" value={count24h} loading={loading} />
      <Stat label="Last 7d" value={count7d} loading={loading} />
    </div>
  );
}

function Stat({
  label,
  value,
  loading,
}: {
  label: string;
  value: number;
  loading: boolean;
}): React.ReactElement {
  return (
    <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3">
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-12" />
      ) : (
        <div className="mt-0.5 font-display text-2xl font-medium leading-none tabular-nums">
          {value.toLocaleString()}
        </div>
      )}
      <p className="mt-1 text-[11px] text-[var(--color-fg-subtle)]">
        Retention + grace finaliser runs
      </p>
    </div>
  );
}

function RecentRuns({
  loading,
  runs,
}: {
  loading: boolean;
  runs: GCRun[];
}): React.ReactElement {
  if (loading) {
    return (
      <div className="space-y-2">
        <Skeleton className="h-3 w-32" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }
  if (runs.length === 0) {
    return (
      <EmptyState
        icon={<Trash2 className="size-5" />}
        title="No retention runs yet"
        description="Once an operator triggers retention from a repo Retention tab — or the cross-tenant grace ticker fires — the runs land here."
      />
    );
  }
  return (
    <div className="space-y-2">
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        Recent runs (last {runs.length})
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Mode</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>When</TableHead>
            <TableHead className="text-right">Manifests</TableHead>
            <TableHead className="text-right">Bytes</TableHead>
            <TableHead>Triggered by</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {runs.map((r) => (
            <TableRow key={r.run_id}>
              <TableCell>
                <Badge tone={r.mode === "retention" ? "accent" : "neutral"}>
                  {r.mode}
                </Badge>
              </TableCell>
              <TableCell>
                <Badge tone={statusTone(r.status)}>{r.status}</Badge>
              </TableCell>
              <TableCell
                className="text-xs text-[var(--color-fg-muted)]"
                title={formatAbsoluteDate(
                  r.completed_at ?? r.started_at ?? r.requested_at,
                )}
              >
                {formatRelativeDate(
                  r.completed_at ?? r.started_at ?? r.requested_at,
                )}
              </TableCell>
              <TableCell className="text-right tabular-nums text-xs text-[var(--color-fg)]">
                {r.manifests_deleted.toLocaleString()}
              </TableCell>
              <TableCell className="text-right tabular-nums text-xs text-[var(--color-fg-muted)]">
                {formatBytes(r.bytes_freed)}
              </TableCell>
              <TableCell className="text-xs text-[var(--color-fg-muted)]">
                {r.triggered_by ? (
                  <code className="font-mono text-[11px]">
                    {r.triggered_by.slice(0, 8)}
                  </code>
                ) : (
                  <span className="italic text-[var(--color-fg-subtle)]">
                    cron
                  </span>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

// statusTone — keyed to the GCRun status values the gc service emits.
// Same mapping as GCCard so the two surfaces use one vocabulary.
function statusTone(s: string): React.ComponentProps<typeof Badge>["tone"] {
  switch (s) {
    case "succeeded":
    case "completed":
      return "success";
    case "running":
      return "accent";
    case "queued":
      return "neutral";
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
}
