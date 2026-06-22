import * as React from "react";
import { History } from "lucide-react";
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
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import {
  useRepoRetentionRuns,
  type RepoRetentionRun,
} from "@/lib/api/retention";
import {
  formatAbsoluteDate,
  formatBytes,
  formatRelativeDate,
} from "@/lib/format";

// Beacon — RetentionRunHistoryPanel (REM-013 gap 2).
//
// Sits below `RetentionRunCard` on the per-repo Retention tab. Lists
// historical retention + retention_grace runs scoped to this repo,
// server-side filtered by the gc service. Infinite-query backed so a
// "Load more" button extends the list without re-querying everything.
//
// Hidden on inherited policies — the panel only renders when the repo
// has its own per-repo policy; an inherited row's runs would live under
// the org-default surface, which is its own future feature.
//
// Empty / loading / error states match the rest of the dashboard.

interface RetentionRunHistoryPanelProps {
  org: string;
  repo: string;
  // Mirrors RetentionRunCard's `disabled` flag — hide on inherited
  // policies so the surface stays scoped to per-repo overrides.
  disabled: boolean;
}

export function RetentionRunHistoryPanel({
  org,
  repo,
  disabled,
}: RetentionRunHistoryPanelProps): React.ReactElement | null {
  const q = useRepoRetentionRuns(org, repo);

  if (disabled) return null;

  if (q.isError) {
    return (
      <ErrorState
        title="Couldn't load retention run history"
        description="The retention runs endpoint didn't answer. Try again, or check the BFF logs."
        onRetry={() => void q.refetch()}
      />
    );
  }

  const runs: RepoRetentionRun[] =
    q.data?.pages.flatMap((p) => p.runs) ?? [];

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between gap-3">
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Retention run history
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Historical soft-delete and grace finaliser runs for this
              repository. Cross-tenant grace ticks also show up here when
              they touch this repo.
            </p>
          </div>
          <History
            className="size-4 text-[var(--color-fg-muted)]"
            aria-hidden
          />
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {q.isLoading ? (
          <HistorySkeleton />
        ) : runs.length === 0 ? (
          <EmptyState
            icon={<History className="size-5" />}
            title="No retention runs yet"
            description="Once you trigger a retention sweep or the cross-tenant grace ticker fires for this repo, the rows land here."
          />
        ) : (
          <>
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
                      <Badge
                        tone={r.mode === "retention" ? "accent" : "neutral"}
                      >
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

            {q.hasNextPage ? (
              <div className="mt-3 flex justify-center">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => void q.fetchNextPage()}
                  disabled={q.isFetchingNextPage}
                  loading={q.isFetchingNextPage}
                >
                  Load more
                </Button>
              </div>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  );
}

// statusTone — same mapping the admin GCCard / RetentionCard use so the
// three surfaces speak one vocabulary.
function statusTone(
  s: string,
): React.ComponentProps<typeof Badge>["tone"] {
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

function HistorySkeleton(): React.ReactElement {
  return (
    <div className="space-y-2">
      <Skeleton className="h-3 w-32" />
      <Skeleton className="h-40 w-full" />
    </div>
  );
}
