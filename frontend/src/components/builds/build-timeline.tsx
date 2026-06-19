import * as React from "react";
import { CircleCheck, CircleX, GitCommit, User2, Clock } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Badge } from "@/components/ui/badge";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { BuildRecord } from "@/lib/api/types";

interface BuildTimelineProps {
  builds: BuildRecord[] | undefined;
  loading?: boolean;
  isError?: boolean;
  onRetry: () => void;
}

// Beacon — BuildTimeline. Vertical timeline view of recent pushes / runs,
// pulled from the audit log via /api/v1/.../builds.
//
// The timeline rail is a single 1px line behind a stack of status dots;
// the visual hierarchy puts success/failure first, then `triggered_by`,
// then commit + duration metadata. Designed to read top-down without
// requiring the operator to scan columns.
export function BuildTimeline({
  builds,
  loading,
  isError,
  onRetry,
}: BuildTimelineProps): React.ReactElement {
  if (isError) {
    return (
      <ErrorState
        title="Couldn't load build history"
        description="The audit log query failed. Try again, or check the BFF logs."
        onRetry={onRetry}
      />
    );
  }

  if (loading) {
    return (
      <Card>
        <CardHeader>
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Push history
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="space-y-5">
            {Array.from({ length: 4 }).map((_, i) => (
              <SkeletonRow key={i} />
            ))}
          </div>
        </CardContent>
      </Card>
    );
  }

  if (!builds || builds.length === 0) {
    return (
      <EmptyState
        icon={<GitCommit className="size-5" />}
        title="No push history yet"
        description="Once an image is pushed under this tag, the audit log will surface every event here — including who triggered it and how long it took."
      />
    );
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Push history
          </CardDescription>
          <span className="text-xs text-[var(--color-fg-muted)]">
            {builds.length} {builds.length === 1 ? "event" : "events"}
          </span>
        </div>
      </CardHeader>
      <CardContent>
        <ol className="relative space-y-5">
          {/* The vertical rail — anchored to the dot column. */}
          <span
            aria-hidden
            className="absolute left-[7px] top-2 bottom-2 w-px bg-[var(--color-border)]"
          />
          {builds.map((b) => (
            <TimelineItem key={b.build_id} build={b} />
          ))}
        </ol>
      </CardContent>
    </Card>
  );
}

function TimelineItem({ build }: { build: BuildRecord }): React.ReactElement {
  const ok = build.status === "success";
  return (
    <li className="relative flex gap-4 pl-0">
      <span
        aria-hidden
        className={cn(
          "relative z-10 mt-0.5 grid size-[15px] shrink-0 place-items-center rounded-full",
          ok
            ? "bg-[var(--color-success)]/15 text-[var(--color-success)]"
            : "bg-[var(--color-danger)]/15 text-[var(--color-danger)]",
        )}
      >
        {ok ? (
          <CircleCheck className="size-3" />
        ) : (
          <CircleX className="size-3" />
        )}
      </span>
      <div className="min-w-0 flex-1 pb-1">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span className="text-sm font-medium text-[var(--color-fg)]">
            {ok ? "Push succeeded" : "Push failed"}
          </span>
          {build.commit_hash ? (
            <span className="font-mono text-xs text-[var(--color-fg-muted)]">
              {build.commit_hash.slice(0, 7)}
            </span>
          ) : null}
          <span
            className="text-xs text-[var(--color-fg-subtle)]"
            title={formatAbsoluteDate(build.occurred_at)}
          >
            {formatRelativeDate(build.occurred_at)}
          </span>
        </div>
        <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-[var(--color-fg-muted)]">
          <span className="inline-flex items-center gap-1">
            <User2 className="size-3" />
            <span className="font-mono">
              {build.triggered_by.length > 12
                ? `${build.triggered_by.slice(0, 8)}…`
                : build.triggered_by || "—"}
            </span>
          </span>
          {build.duration ? (
            <span className="inline-flex items-center gap-1">
              <Clock className="size-3" />
              {build.duration}
            </span>
          ) : null}
          {!ok ? (
            <Badge tone="danger" className="!py-0">
              failure
            </Badge>
          ) : null}
        </div>
      </div>
    </li>
  );
}

function SkeletonRow(): React.ReactElement {
  return (
    <div className="flex gap-4">
      <Skeleton className="size-[15px] rounded-full" />
      <div className="flex-1 space-y-1.5">
        <Skeleton className="h-3 w-44" />
        <Skeleton className="h-2.5 w-64" />
      </div>
    </div>
  );
}
