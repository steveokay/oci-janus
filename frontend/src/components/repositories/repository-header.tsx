import * as React from "react";
import { Link } from "@tanstack/react-router";
import { Globe, Lock, Trash2, ChevronRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { formatAbsoluteDate, formatBytes } from "@/lib/format";
import type { Repository } from "@/lib/api/types";

interface RepositoryHeaderProps {
  repo?: Repository;
  loading?: boolean;
  onDelete: () => void;
}

// Beacon — RepositoryHeader. Compact identity card: name + breadcrumb +
// visibility + storage + key metadata, plus the danger action tucked to the
// right.
export function RepositoryHeader({
  repo,
  loading,
  onDelete,
}: RepositoryHeaderProps): React.ReactElement {
  const pct =
    repo && repo.storage_quota_bytes > 0
      ? Math.min(
          100,
          Math.round((repo.storage_used_bytes / repo.storage_quota_bytes) * 100),
        )
      : 0;
  return (
    <div className="space-y-4">
      {/* Breadcrumb */}
      <nav
        aria-label="Breadcrumb"
        className="flex items-center gap-1 text-xs text-[var(--color-fg-muted)]"
      >
        <Link to="/repositories" className="hover:text-[var(--color-fg)]">
          Repositories
        </Link>
        <ChevronRight className="size-3 text-[var(--color-fg-subtle)]" />
        {loading ? (
          <Skeleton className="h-3 w-32" />
        ) : (
          <span className="font-mono text-[var(--color-fg)]">
            {repo?.org ? `${repo.org}/` : ""}
            {repo?.name}
          </span>
        )}
      </nav>

      <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
        <div className="flex items-start gap-4">
          <span
            className="grid size-12 shrink-0 place-items-center rounded-lg bg-[var(--color-accent-subtle)] font-display text-xl font-semibold text-[var(--color-accent)]"
            aria-hidden
          >
            {repo?.name[0]?.toUpperCase() ?? "·"}
          </span>
          <div className="min-w-0 space-y-1">
            {loading ? (
              <Skeleton className="h-7 w-72" />
            ) : (
              <h1 className="font-display text-2xl font-medium tracking-tight">
                {/* Skip the empty-org slash for older dev rows. */}
                {repo?.org ? (
                  <span className="text-[var(--color-fg-muted)]">
                    {repo.org}/
                  </span>
                ) : null}
                {repo?.name}
              </h1>
            )}
            <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-fg-muted)]">
              {repo ? (
                repo.is_public ? (
                  <Badge tone="accent">
                    <Globe className="size-3" /> Public
                  </Badge>
                ) : (
                  <Badge tone="neutral">
                    <Lock className="size-3" /> Private
                  </Badge>
                )
              ) : (
                <Skeleton className="h-5 w-20 rounded-full" />
              )}
              {loading ? (
                <Skeleton className="h-3 w-44" />
              ) : (
                <span>
                  Created {formatAbsoluteDate(repo?.created_at)}
                </span>
              )}
            </div>
          </div>
        </div>

        <Button
          variant="ghost"
          onClick={onDelete}
          className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
          disabled={!repo}
        >
          <Trash2 className="size-4" />
          Delete repository
        </Button>
      </div>

      {/* Storage strip */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="flex items-center justify-between text-xs text-[var(--color-fg-muted)]">
          <span className="font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
            Storage
          </span>
          {loading ? (
            <Skeleton className="h-3 w-32" />
          ) : (
            <span className="font-mono text-[var(--color-fg)]">
              {formatBytes(repo?.storage_used_bytes ?? 0)}{" "}
              <span className="text-[var(--color-fg-subtle)]">
                / {formatBytes(repo?.storage_quota_bytes ?? 0)}
              </span>
            </span>
          )}
        </div>
        <Progress value={pct} className="mt-2 h-1.5" />
      </div>
    </div>
  );
}
