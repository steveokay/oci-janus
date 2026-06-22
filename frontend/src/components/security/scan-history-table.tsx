import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  CheckCircle2,
  Clock,
  History,
  Loader2,
  Pickaxe,
  Timer,
  TimerOff,
  Upload,
  Wrench,
  XCircle,
} from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SeverityBar } from "@/components/security/severity-bar";
import {
  toSeverityBarCounts,
  useScanHistory,
  type ScanHistoryEntry,
} from "@/lib/api/security";
import {
  PageSizeSelector,
  usePageSize,
} from "@/components/ui/page-size-selector";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

// ScanHistoryTable — FE-API-015.
//
// One row per scan run, newest first. Trigger + status badges, mini
// severity bar so the operator can scan the list for the runs that
// actually found something. Default window is server-side (30d).
//
// `since` filter and explicit status filter intentionally omitted on
// this first pass — the data is already keyset-ordered and 200-cap
// paginated; we can add filter chips on top later if operators ask.
export function ScanHistoryTable(): React.ReactElement {
  // S-MAINT-1 P5: persisted page size, "scans" key separate from the
  // vulnerabilities table so each surface remembers its own setting.
  const [pageSize, setPageSize] = usePageSize("scans");
  const q = useScanHistory({ limit: pageSize });
  const flat = React.useMemo(
    () => q.data?.pages.flatMap((p) => p.scans) ?? [],
    [q.data],
  );

  if (q.isError) {
    return (
      <ErrorState
        title="Couldn't load scan history"
        description="The metadata service didn't answer. Try again, or check the BFF logs."
        onRetry={() => void q.refetch()}
      />
    );
  }
  if (q.isLoading) {
    return <SkeletonRows />;
  }
  if (flat.length === 0) {
    return (
      <EmptyState
        icon={<History className="size-5" />}
        title="No scan runs in the last 30 days"
        description="Push a new tag or rescan an existing one to populate this feed."
      />
    );
  }
  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <PageSizeSelector value={pageSize} onChange={setPageSize} />
      </div>
      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Image</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="hidden sm:table-cell">Trigger</TableHead>
              <TableHead>Findings</TableHead>
              <TableHead className="hidden lg:table-cell">Scanner</TableHead>
              <TableHead className="text-right">When</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {flat.map((s) => (
              <ScanRow key={s.scan_id} s={s} />
            ))}
          </TableBody>
        </Table>
      </div>

      {q.hasNextPage ? (
        <div className="flex justify-center">
          <Button
            variant="outline"
            size="sm"
            onClick={() => void q.fetchNextPage()}
            loading={q.isFetchingNextPage}
            disabled={q.isFetchingNextPage}
          >
            {q.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function ScanRow({ s }: { s: ScanHistoryEntry }): React.ReactElement {
  const [org, repo] = splitRepo(s.repo);
  const counts = toSeverityBarCounts(s.severity_counts);
  const total =
    counts.CRITICAL + counts.HIGH + counts.MEDIUM + counts.LOW;
  const duration = formatDuration(s.started_at, s.completed_at);
  return (
    <TableRow>
      <TableCell>
        <Link
          to="/repositories/$org/$repo/tags/$tag"
          params={{ org, repo, tag: s.tag }}
          className="flex flex-col gap-0.5 hover:underline"
        >
          <code className="font-mono text-sm font-medium text-[var(--color-fg)]">
            {s.repo}:{s.tag}
          </code>
          <code className="truncate font-mono text-[10px] text-[var(--color-fg-subtle)]">
            {s.manifest_digest}
          </code>
        </Link>
      </TableCell>
      <TableCell>
        <StatusBadge status={s.status} />
      </TableCell>
      <TableCell className="hidden sm:table-cell">
        <TriggerBadge trigger={s.trigger} />
      </TableCell>
      <TableCell>
        {total > 0 ? (
          <div className="flex max-w-[160px] flex-col gap-1">
            <SeverityBar counts={counts} />
            <span className="text-[11px] text-[var(--color-fg-muted)]">
              {total} {total === 1 ? "finding" : "findings"}
            </span>
          </div>
        ) : s.status === "complete" ? (
          <span className="inline-flex items-center gap-1 text-xs text-[var(--color-success)]">
            <CheckCircle2 className="size-3" />
            Clean
          </span>
        ) : (
          <span className="text-xs text-[var(--color-fg-subtle)]">—</span>
        )}
      </TableCell>
      <TableCell className="hidden text-xs text-[var(--color-fg-muted)] lg:table-cell">
        <code className="font-mono">{s.scanner || "—"}</code>
      </TableCell>
      <TableCell className="text-right">
        <div className="flex flex-col items-end gap-0.5">
          <span
            className="text-xs text-[var(--color-fg)]"
            title={formatAbsoluteDate(s.started_at)}
          >
            {formatRelativeDate(s.started_at)}
          </span>
          {duration ? (
            <span className="inline-flex items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
              <Timer className="size-3" />
              {duration}
            </span>
          ) : s.status === "running" || s.status === "pending" ? (
            <span className="inline-flex items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
              <TimerOff className="size-3" />
              in flight
            </span>
          ) : null}
        </div>
      </TableCell>
    </TableRow>
  );
}

function StatusBadge({ status }: { status: string }): React.ReactElement {
  switch (status) {
    case "complete":
      return (
        <Badge tone="success">
          <CheckCircle2 className="size-3" />
          complete
        </Badge>
      );
    case "running":
      return (
        <Badge tone="accent">
          <Loader2 className="size-3 animate-spin" />
          running
        </Badge>
      );
    case "pending":
      return (
        <Badge tone="neutral">
          <Clock className="size-3" />
          pending
        </Badge>
      );
    case "failed":
      return (
        <Badge tone="danger">
          <XCircle className="size-3" />
          failed
        </Badge>
      );
    default:
      return <Badge tone="neutral">{status || "—"}</Badge>;
  }
}

function TriggerBadge({ trigger }: { trigger: string }): React.ReactElement {
  switch (trigger) {
    case "push":
      return (
        <Badge tone="neutral">
          <Upload className="size-3" />
          push
        </Badge>
      );
    case "manual":
      return (
        <Badge tone="accent">
          <Wrench className="size-3" />
          manual
        </Badge>
      );
    case "scheduled":
      return (
        <Badge tone="neutral">
          <Pickaxe className="size-3" />
          scheduled
        </Badge>
      );
    default:
      return <Badge tone="neutral">{trigger || "—"}</Badge>;
  }
}

function formatDuration(
  startedAt: string,
  completedAt: string | null | undefined,
): string | null {
  if (!completedAt) return null;
  const ms = Date.parse(completedAt) - Date.parse(startedAt);
  if (!Number.isFinite(ms) || ms <= 0) return null;
  if (ms < 1_000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1_000).toFixed(1)}s`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)}m`;
  return `${(ms / 3_600_000).toFixed(1)}h`;
}

function splitRepo(full: string): [string, string] {
  const i = full.indexOf("/");
  if (i < 0) return ["", full];
  return [full.slice(0, i), full.slice(i + 1)];
}

function SkeletonRows(): React.ReactElement {
  return (
    <div className="space-y-2">
      {Array.from({ length: 6 }).map((_, i) => (
        <Skeleton key={i} className="h-12 w-full" />
      ))}
    </div>
  );
}
