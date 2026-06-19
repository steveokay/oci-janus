import * as React from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { Globe, Lock } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { formatBytes, formatRelativeDate } from "@/lib/format";
import type { Repository } from "@/lib/api/types";

interface Props {
  repositories: Repository[];
  loading?: boolean;
}

// Beacon — RepositoriesTable. The standard list view across all visibility
// filters. Click row → /repositories/:org/:repo.
export function RepositoriesTable({
  repositories,
  loading,
}: Props): React.ReactElement {
  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[40%]">Repository</TableHead>
            <TableHead>Visibility</TableHead>
            <TableHead>Storage</TableHead>
            <TableHead className="hidden md:table-cell">Created</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading
            ? Array.from({ length: 6 }).map((_, i) => <SkeletonRow key={i} />)
            : repositories.map((r) => <Row key={r.repo_id} repo={r} />)}
        </TableBody>
      </Table>
    </div>
  );
}

function Row({ repo }: { repo: Repository }): React.ReactElement {
  const navigate = useNavigate();
  const pct =
    repo.storage_quota_bytes > 0
      ? Math.min(
          100,
          Math.round((repo.storage_used_bytes / repo.storage_quota_bytes) * 100),
        )
      : 0;
  // Whole-row click is convenience; the real focusable target is the Link in
  // the first cell so keyboard nav + middle-click + right-click all behave.
  return (
    <TableRow
      interactive
      onClick={() =>
        void navigate({
          to: "/repositories/$org/$repo",
          params: { org: repo.org, repo: repo.name },
        })
      }
    >
      <TableCell>
        <Link
          to="/repositories/$org/$repo"
          params={{ org: repo.org, repo: repo.name }}
          onClick={(e) => e.stopPropagation()}
          className="flex items-center gap-3 text-[var(--color-fg)]"
        >
          <span
            className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] font-display text-sm font-semibold text-[var(--color-accent)]"
            aria-hidden
          >
            {repo.name[0]?.toUpperCase() ?? "·"}
          </span>
          <div className="min-w-0">
            <div className="truncate text-sm font-medium">
              <span className="text-[var(--color-fg-muted)]">{repo.org}/</span>
              <span>{repo.name}</span>
            </div>
            <div className="font-mono text-[11px] text-[var(--color-fg-subtle)]">
              {repo.repo_id.slice(0, 8)}
            </div>
          </div>
        </Link>
      </TableCell>
      <TableCell>
        {repo.is_public ? (
          <Badge tone="accent">
            <Globe className="size-3" aria-hidden /> Public
          </Badge>
        ) : (
          <Badge tone="neutral">
            <Lock className="size-3" aria-hidden /> Private
          </Badge>
        )}
      </TableCell>
      <TableCell className="min-w-[180px]">
        <div className="flex flex-col gap-1.5">
          <span className="font-mono text-xs text-[var(--color-fg)]">
            {formatBytes(repo.storage_used_bytes)}
            <span className="text-[var(--color-fg-subtle)]">
              {" "}/ {formatBytes(repo.storage_quota_bytes)}
            </span>
          </span>
          <Progress value={pct} className="h-1" />
        </div>
      </TableCell>
      <TableCell className="hidden text-[var(--color-fg-muted)] md:table-cell">
        {formatRelativeDate(repo.created_at)}
      </TableCell>
    </TableRow>
  );
}

function SkeletonRow(): React.ReactElement {
  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-3">
          <Skeleton className="size-8 rounded-md" />
          <div className="space-y-1.5">
            <Skeleton className="h-3.5 w-44" />
            <Skeleton className="h-2.5 w-20" />
          </div>
        </div>
      </TableCell>
      <TableCell>
        <Skeleton className="h-5 w-16 rounded-full" />
      </TableCell>
      <TableCell>
        <div className="space-y-1.5">
          <Skeleton className="h-3 w-28" />
          <Skeleton className="h-1 w-32" />
        </div>
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <Skeleton className="h-3 w-20" />
      </TableCell>
    </TableRow>
  );
}
