import * as React from "react";
import { Activity, CheckCircle2, XCircle } from "lucide-react";
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
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { useActivity, type PrincipalActivity } from "@/lib/api/activity";

// ActivityTable — FE-API-048 T27.
//
// Renders the principal activity feed returned by `useActivity`. Supports
// "Load more" via the `next_page_token` from the hook response.
//
// TODO (pagination): `useActivity` currently exposes `data.next_page_token`
// but does not yet expose a `fetchNextPage` / cursor-based infinite query.
// The "Load more" button below is wired to a local `limit` state increment
// as a pragmatic workaround — the component re-fetches with a higher `limit`
// value until either the list exhausts or a proper cursor API ships.

interface ActivityTableProps {
  // The user or service-account ID whose activity feed to render.
  principalUserID: string | undefined;
  // limit controls how many events we request from the backend (default 50).
  limit?: number;
  // since is an RFC3339 lower bound on event time (the selected window). When
  // set, the backend time-bounds the feed server-side (FUT-088 #1).
  since?: string;
  // onLoadMore is called when the operator clicks "Load more". The parent
  // controls the limit so it can raise it in state.
  onLoadMore?: () => void;
  // Principal display name shown in the Principal column for the "self" row.
  // Falls back to "—" when not provided.
  principalDisplayName?: string;
}

export function ActivityTable({
  principalUserID,
  limit = 50,
  since,
  onLoadMore,
  principalDisplayName,
}: ActivityTableProps): React.ReactElement {
  const q = useActivity(principalUserID, limit, since);

  // Flatten: the hook wraps the list in `data.activity`.
  const rows: PrincipalActivity[] = q.data?.activity ?? [];
  // `next_page_token` signals the backend has more rows beyond our current
  // `limit`. We surface "Load more" when it is present.
  const hasMore = !!q.data?.next_page_token;

  if (q.isError) {
    return (
      <ErrorState
        title="Couldn't load activity"
        description="The auth service didn't answer. Check that the BFF is reachable."
        onRetry={() => void q.refetch()}
      />
    );
  }

  if (q.isLoading) {
    return <SkeletonRows />;
  }

  if (rows.length === 0) {
    return (
      <EmptyState
        icon={<Activity className="size-5" />}
        title="No activity in this window"
        description="Events will appear here once the principal makes authenticated requests."
      />
    );
  }

  return (
    <div className="space-y-4">
      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
        <Table>
          <TableHeader>
            <TableRow>
              {/* When — relative time; tooltip shows the absolute timestamp. */}
              <TableHead>When</TableHead>
              {/* Principal — who made the request. */}
              <TableHead className="hidden sm:table-cell">Principal</TableHead>
              {/* Action — audit action code e.g. "push.image". */}
              <TableHead>Action</TableHead>
              {/* Repo — empty for non-repository-scoped actions. */}
              <TableHead className="hidden md:table-cell">Repo</TableHead>
              {/* IP — source IP from the audit event; blank when absent. */}
              <TableHead className="hidden lg:table-cell">IP</TableHead>
              {/* Key — API key prefix when key-authenticated. */}
              <TableHead className="hidden lg:table-cell">Key</TableHead>
              {/* Status — success (green) or failure (amber). */}
              <TableHead className="text-right">Status</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row, i) => (
              <ActivityRow
                // `at` can repeat within a burst; combine with index to keep
                // keys unique without introducing an external ID field.
                key={`${row.at}-${i}`}
                row={row}
                principalDisplayName={principalDisplayName}
              />
            ))}
          </TableBody>
        </Table>
      </div>

      {/* Load more — only shown when the backend signals more rows are available. */}
      {hasMore && onLoadMore ? (
        <div className="flex justify-center">
          <Button
            variant="outline"
            size="sm"
            onClick={onLoadMore}
            loading={q.isFetching}
            disabled={q.isFetching}
          >
            {q.isFetching ? "Loading…" : "Load more"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

// ── ActivityRow ───────────────────────────────────────────────────────────────

interface ActivityRowProps {
  row: PrincipalActivity;
  principalDisplayName?: string;
}

function ActivityRow({
  row,
  principalDisplayName,
}: ActivityRowProps): React.ReactElement {
  // API key prefix — truncate the UUID to the first 8 chars to keep the
  // column tight. The full UUID is shown in the tooltip via `title`.
  const keyPrefix = row.api_key_id
    ? row.api_key_id.slice(0, 8) + "…"
    : "—";

  return (
    <TableRow>
      {/* When */}
      <TableCell>
        <span
          className="text-sm text-[var(--color-fg)]"
          title={formatAbsoluteDate(row.at)}
        >
          {formatRelativeDate(row.at)}
        </span>
      </TableCell>

      {/* Principal */}
      <TableCell className="hidden sm:table-cell">
        <span className="text-sm text-[var(--color-fg-muted)]">
          {principalDisplayName ?? "—"}
        </span>
      </TableCell>

      {/* Action */}
      <TableCell>
        <code className="rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 font-mono text-xs text-[var(--color-fg)]">
          {row.action || "—"}
        </code>
      </TableCell>

      {/* Repo */}
      <TableCell className="hidden max-w-[180px] truncate md:table-cell">
        {row.repo ? (
          <code className="font-mono text-xs text-[var(--color-fg-muted)]">
            {row.repo}
          </code>
        ) : (
          <span className="text-xs text-[var(--color-fg-subtle)]">—</span>
        )}
      </TableCell>

      {/* IP */}
      <TableCell className="hidden lg:table-cell">
        <code className="font-mono text-xs text-[var(--color-fg-muted)]">
          {row.source_ip || "—"}
        </code>
      </TableCell>

      {/* Key prefix */}
      <TableCell className="hidden lg:table-cell">
        <code
          className="font-mono text-xs text-[var(--color-fg-muted)]"
          title={row.api_key_id || undefined}
        >
          {keyPrefix}
        </code>
      </TableCell>

      {/* Status badge */}
      <TableCell className="text-right">
        <StatusBadge status={row.status} />
      </TableCell>
    </TableRow>
  );
}

// ── StatusBadge ───────────────────────────────────────────────────────────────

function StatusBadge({
  status,
}: {
  status: "success" | "failure";
}): React.ReactElement {
  if (status === "success") {
    return (
      <Badge tone="success">
        <CheckCircle2 className="size-3" />
        success
      </Badge>
    );
  }
  return (
    <Badge tone="danger">
      <XCircle className="size-3" />
      failure
    </Badge>
  );
}

// ── SkeletonRows ──────────────────────────────────────────────────────────────

function SkeletonRows(): React.ReactElement {
  return (
    <div className="space-y-2">
      {Array.from({ length: 8 }).map((_, i) => (
        <Skeleton key={i} className="h-12 w-full" />
      ))}
    </div>
  );
}
