import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { Play, Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  isTerminalRunStatus,
  useRetentionRunStatus,
  useTriggerRetentionRun,
  type RetentionRunStatus,
} from "@/lib/api/retention";
import { formatAbsoluteDate, formatBytes, formatRelativeDate } from "@/lib/format";

// Beacon — RetentionRunCard (S11 Slice 3, FE-API-040).
//
// Renders below the policy summary. One button to queue a retention sweep
// + a live status panel for the latest run the operator triggered from
// this tab.
//
// The card only tracks runs triggered from THIS browser session — we
// don't have a per-repo "list recent runs" endpoint today (gc service
// ListRuns is platform-admin global). The card sticks the run_id in
// component state so the polled GET keeps rendering progression after
// the dialog closes; refreshing the page resets the card to "no run yet".
//
// Render states:
//   - never triggered           — empty hint with "Run now" CTA
//   - queued                    — neutral badge + spinner-feel hint
//   - running                   — accent dot-pulse badge
//   - completed (success)       — success badge + result strip
//   - failed                    — danger badge + error_message
//
// The card is hidden entirely for inherited policies — running retention
// from a repo-without-its-own-policy is permitted server-side (it just
// uses the effective policy), but the UX is cleaner if we show the
// trigger only on the per-repo override card. Inherited rows can still
// be processed via the cross-tenant retention_grace tick.

interface RetentionRunCardProps {
  org: string;
  repo: string;
  // Disabled when the policy is inherited so the trigger sits next to
  // the per-repo override. Inherited rows still process via the
  // cross-tenant grace tick; the per-org runner is exposed on the org
  // page (slice 4).
  disabled: boolean;
}

export function RetentionRunCard({
  org,
  repo,
  disabled,
}: RetentionRunCardProps): React.ReactElement | null {
  // Component-local last-run state. We keep this off TanStack's cache
  // because (a) the run id never leaves this component, and (b) a fresh
  // mount should reset to "no run triggered yet" — caching across page
  // navigations would show a stale completed run forever.
  const [runId, setRunId] = React.useState<string | null>(null);

  const trigger = useTriggerRetentionRun(org, repo);
  const status = useRetentionRunStatus(org, repo, runId);

  // Hide entirely on inherited policies — see file-level comment.
  if (disabled) return null;

  async function onRun(): Promise<void> {
    try {
      const resp = await trigger.mutateAsync();
      setRunId(resp.run_id);
      toast.success("Retention sweep queued.");
    } catch (e) {
      const httpStatus = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        httpStatus === 403
          ? "Admin or owner on this repo is required to trigger retention."
          : httpStatus === 404
            ? "Retention runner isn't wired on this control plane."
            : "Couldn't queue the run. Try again, or check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Retention executor
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Trigger a soft-delete sweep using the current policy. Matched
              manifests enter a 7-day grace window before any hard delete.
            </p>
          </div>
          <Button
            size="sm"
            onClick={onRun}
            disabled={trigger.isPending || isInFlight(status.data)}
            loading={trigger.isPending}
          >
            <Play className="size-3.5" />
            Run now
          </Button>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        {runId ? (
          <RunStatusStrip status={status.data} />
        ) : (
          <p className="text-xs text-[var(--color-fg-subtle)]">
            No run triggered this session. Click <strong>Run now</strong> to
            queue one.
          </p>
        )}
      </CardContent>
    </Card>
  );
}

// isInFlight — true when the run is queued or running. Used to disable
// the "Run now" button so the operator can't double-fire while the
// previous run is still processing.
function isInFlight(s: RetentionRunStatus | undefined): boolean {
  if (!s) return false;
  return !isTerminalRunStatus(s.status);
}

// RunStatusStrip — single row with a badge + a "since-N" timestamp + the
// count fields once the run lands. Compact-by-design: a long-form run
// history surface is deferred to slice 5 where the admin tile renders
// every retention run across the platform.
function RunStatusStrip({
  status,
}: {
  status: RetentionRunStatus | undefined;
}): React.ReactElement {
  if (!status) {
    return (
      <div className="flex items-center gap-2 text-xs text-[var(--color-fg-muted)]">
        <Badge tone="neutral" dot pulse>
          Queued
        </Badge>
        Waiting for the runner to pick up the row…
      </div>
    );
  }

  const tone = (() => {
    switch (status.status) {
      case "queued":
        return "neutral" as const;
      case "running":
        return "accent" as const;
      case "completed":
        return "success" as const;
      case "failed":
        return "danger" as const;
      default:
        return "neutral" as const;
    }
  })();
  const pulse = status.status === "queued" || status.status === "running";

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-fg)]">
        <Badge tone={tone} dot pulse={pulse}>
          {status.status[0].toUpperCase() + status.status.slice(1)}
        </Badge>
        <span className="text-[var(--color-fg-muted)]">
          Triggered{" "}
          <span title={formatAbsoluteDate(status.requested_at)}>
            {formatRelativeDate(status.requested_at)}
          </span>
        </span>
        {status.triggered_by ? (
          <span className="text-[var(--color-fg-subtle)]">
            · by {status.triggered_by.slice(0, 8)}
          </span>
        ) : null}
      </div>

      {status.status === "completed" ? (
        <ResultStrip status={status} />
      ) : null}

      {status.status === "failed" && status.error_message ? (
        <div className="rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 px-3 py-2 text-xs text-[var(--color-fg)]">
          {status.error_message}
        </div>
      ) : null}
    </div>
  );
}

// ResultStrip — three stats once the run completes. Marks/deleted/bytes
// reflect the soft-delete pass (retention mode); the eventual hard-delete
// totals land later via the cross-tenant retention_grace tick.
function ResultStrip({
  status,
}: {
  status: RetentionRunStatus;
}): React.ReactElement {
  return (
    <div className="grid grid-cols-3 gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3 text-center">
      <Stat
        label="Marked"
        value={status.manifests_marked.toLocaleString()}
        icon={<Trash2 className="size-3.5" aria-hidden />}
      />
      <Stat
        label="Bytes (grace)"
        value={formatBytes(status.bytes_freed)}
      />
      <Stat
        label="Completed"
        value={
          status.completed_at ? formatRelativeDate(status.completed_at) : "—"
        }
      />
    </div>
  );
}

function Stat({
  label,
  value,
  icon,
}: {
  label: string;
  value: string;
  icon?: React.ReactNode;
}): React.ReactElement {
  return (
    <div>
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        <span className="inline-flex items-center gap-1">
          {icon}
          {label}
        </span>
      </div>
      <div className="mt-0.5 font-display text-lg font-medium tabular-nums">
        {value}
      </div>
    </div>
  );
}
