import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { AlertTriangle, Play, Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import {
  GC_MODES,
  useGCRuns,
  useGCStatus,
  useTriggerGCRun,
  type GCMode,
  type GCRun,
} from "@/lib/api/admin-gc";
import { formatAbsoluteDate, formatBytes, formatRelativeDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// Beacon — Housekeeping / Garbage collection card (FE-API-032).
//
// Top: status panel — last run summary (mode / status / blobs freed /
// manifests deleted / next scheduled). Bottom: small table of the last 10
// runs. "Run now" CTA opens a type-to-confirm dialog so an accidental
// click on this expensive operation is hard.

// statusTone keys GC run status → Badge tone. The gc service emits
// queued/running/succeeded/failed; the queued and running rows behave
// like in-flight (no terminal duration / freed counts to show).
function statusTone(s: string): React.ComponentProps<typeof Badge>["tone"] {
  switch (s) {
    case "succeeded":
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

export function GCCard(): React.ReactElement {
  const status = useGCStatus();
  const runs = useGCRuns({ limit: 10 });
  const [open, setOpen] = React.useState(false);

  const flatRuns = React.useMemo(
    () => runs.data?.pages.flatMap((p) => p.runs) ?? [],
    [runs.data],
  );

  if (status.isError) {
    // 404 here means the BFF route is disabled (GC_GRPC_ADDR unset). Surface
    // that distinctly so an operator knows it's a deployment config issue,
    // not a transient outage.
    const code = (status.error as AxiosError | undefined)?.response?.status;
    if (code === 404) {
      return (
        <EmptyState
          icon={<Trash2 className="size-5" />}
          title="GC visibility isn't wired on this control plane"
          description="Set GC_GRPC_ADDR on the management BFF and restart to see sweep history + trigger manual runs."
        />
      );
    }
    return (
      <ErrorState
        title="Couldn't load GC status"
        description="The /admin/gc/status endpoint didn't answer. Retry, or check the BFF logs."
        onRetry={() => void status.refetch()}
      />
    );
  }

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="pb-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Housekeeping · Garbage collection
            </CardDescription>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setOpen(true)}
            >
              <Play className="size-3.5" />
              Run now
            </Button>
          </div>
        </CardHeader>
        <CardContent className="pt-0">
          {status.isLoading || !status.data ? (
            <StatusSkeleton />
          ) : (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <Tile
                label="Last run"
                primary={
                  status.data.last_run_completed_at ? (
                    <span title={formatAbsoluteDate(status.data.last_run_completed_at)}>
                      {formatRelativeDate(status.data.last_run_completed_at)}
                    </span>
                  ) : (
                    <span className="text-[var(--color-fg-subtle)]">Never</span>
                  )
                }
                secondary={
                  status.data.last_run_mode ? (
                    <span className="flex items-center gap-1.5">
                      <span className="font-mono">{status.data.last_run_mode}</span>
                      {status.data.last_run_status ? (
                        <Badge tone={statusTone(status.data.last_run_status)}>
                          {status.data.last_run_status}
                        </Badge>
                      ) : null}
                    </span>
                  ) : null
                }
              />
              <Tile
                label="Blobs freed"
                primary={
                  <span className="tabular-nums">
                    {status.data.last_run_blobs_freed.toLocaleString()}
                  </span>
                }
                secondary={
                  status.data.last_run_bytes_freed > 0 ? (
                    <span className="text-[var(--color-fg-muted)]">
                      {formatBytes(status.data.last_run_bytes_freed)} reclaimed
                    </span>
                  ) : null
                }
              />
              <Tile
                label="Manifests deleted"
                primary={
                  <span className="tabular-nums">
                    {status.data.last_run_manifests_deleted.toLocaleString()}
                  </span>
                }
              />
              <Tile
                label="Next scheduled"
                primary={
                  status.data.next_scheduled_at ? (
                    <span title={formatAbsoluteDate(status.data.next_scheduled_at)}>
                      {formatRelativeDate(status.data.next_scheduled_at)}
                    </span>
                  ) : (
                    <span className="text-[var(--color-fg-subtle)]">Unknown</span>
                  )
                }
                secondary={
                  <span className="text-[10px] text-[var(--color-fg-subtle)]">
                    Best-effort — the in-process ticker is the real source.
                  </span>
                }
              />
            </div>
          )}

          {/* Error message from the last run, if any. Surfacing this on the
              status card (not just the table row) keeps a recent failure
              visible without an extra click. */}
          {status.data?.last_run_error ? (
            <div className="mt-4 flex items-start gap-2 rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-3 py-2 text-xs text-[var(--color-fg)]">
              <AlertTriangle className="size-3.5 shrink-0 text-[var(--color-danger)]" />
              <code className="font-mono">{status.data.last_run_error}</code>
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Recent runs
          </CardDescription>
        </CardHeader>
        <CardContent className="pt-0">
          {runs.isError ? (
            <ErrorState
              title="Couldn't load GC runs"
              description="The /admin/gc/runs endpoint didn't answer."
              onRetry={() => void runs.refetch()}
            />
          ) : runs.isLoading ? (
            <div className="space-y-2">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : flatRuns.length === 0 ? (
            <EmptyState
              icon={<Trash2 className="size-5" />}
              title="No runs yet"
              description="The cron sweeps in the background; manual runs queued here will appear in the list."
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Mode</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Triggered by</TableHead>
                  <TableHead className="hidden md:table-cell">Duration</TableHead>
                  <TableHead className="text-right">Freed</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {flatRuns.map((r) => (
                  <GCRunRow key={r.run_id} r={r} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <TriggerDialog open={open} onOpenChange={setOpen} />
    </div>
  );
}

function Tile({
  label,
  primary,
  secondary,
}: {
  label: string;
  primary: React.ReactNode;
  secondary?: React.ReactNode;
}): React.ReactElement {
  return (
    <div>
      <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div className="mt-1 text-sm font-medium text-[var(--color-fg)]">
        {primary}
      </div>
      {secondary ? (
        <div className="mt-0.5 text-xs">{secondary}</div>
      ) : null}
    </div>
  );
}

function StatusSkeleton(): React.ReactElement {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
      {Array.from({ length: 4 }).map((_, i) => (
        <Skeleton key={i} className="h-12 w-full" />
      ))}
    </div>
  );
}

function GCRunRow({ r }: { r: GCRun }): React.ReactElement {
  return (
    <TableRow>
      <TableCell>
        <code className="font-mono text-xs font-medium text-[var(--color-fg)]">
          {r.mode || "—"}
        </code>
      </TableCell>
      <TableCell>
        <Badge tone={statusTone(r.status)}>{r.status}</Badge>
      </TableCell>
      <TableCell>
        <code className="font-mono text-xs text-[var(--color-fg-muted)]">
          {r.triggered_by || "cron"}
        </code>
      </TableCell>
      <TableCell className="hidden text-xs tabular-nums text-[var(--color-fg-muted)] md:table-cell">
        {r.duration_ms > 0 ? `${(r.duration_ms / 1000).toFixed(1)}s` : "—"}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex flex-col items-end gap-0.5 text-xs tabular-nums">
          <span className="text-[var(--color-fg)]">
            {r.blobs_freed.toLocaleString()} blobs · {r.manifests_deleted.toLocaleString()} manifests
          </span>
          {r.bytes_freed > 0 ? (
            <span className="text-[var(--color-fg-subtle)]">
              {formatBytes(r.bytes_freed)}
            </span>
          ) : null}
        </div>
      </TableCell>
    </TableRow>
  );
}

// TriggerDialog — type "RUN GC" + pick a mode. Full GC is expensive; the
// extra friction is the brief's intent.
function TriggerDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}): React.ReactElement {
  const trigger = useTriggerGCRun();
  const [mode, setMode] = React.useState<GCMode>("dry-run");
  const [typed, setTyped] = React.useState("");
  const EXPECTED = "RUN GC";

  // Reset every time the dialog reopens so a previous near-miss doesn't
  // carry forward unexpected text.
  React.useEffect(() => {
    if (!open) {
      setTyped("");
      setMode("dry-run");
    }
  }, [open]);

  const choice = GC_MODES.find((m) => m.value === mode) ?? GC_MODES[0];
  const requiresConfirm = choice.destructive;
  const canSubmit = requiresConfirm
    ? typed === EXPECTED && !trigger.isPending
    : !trigger.isPending;

  async function handleSubmit(): Promise<void> {
    try {
      const res = await trigger.mutateAsync({ mode });
      toast.success("GC run queued", {
        description: `run_id ${res.run_id} · status ${res.status}`,
      });
      onOpenChange(false);
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Platform-admin marker grant required."
          : code === 404
            ? "GC routes aren't wired on the BFF."
            : code === 400
              ? "Backend rejected the request — check the mode."
              : "Couldn't queue the GC run. Check the BFF logs.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Play className="size-4 text-[var(--color-accent)]" />
            Run garbage collection now
          </DialogTitle>
          <DialogDescription>
            Manual sweeps stack with the cron schedule — the gc worker
            arbitrates with a per-tenant advisory lock, so duplicate runs
            won't collide. Destructive modes mutate storage immediately.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label>Mode</Label>
          <div className="space-y-1.5">
            {GC_MODES.map((m) => (
              <label
                key={m.value}
                className={cn(
                  "flex cursor-pointer items-start gap-3 rounded-md border bg-[var(--color-surface)] px-3 py-2 transition-colors",
                  mode === m.value
                    ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)]/40"
                    : "border-[var(--color-border)] hover:bg-[var(--color-surface-sunken)]",
                )}
              >
                <input
                  type="radio"
                  name="gc-mode"
                  value={m.value}
                  checked={mode === m.value}
                  onChange={() => setMode(m.value)}
                  className="sr-only"
                />
                <span
                  aria-hidden
                  className={cn(
                    "mt-0.5 grid size-4 shrink-0 place-items-center rounded-full border",
                    mode === m.value
                      ? "border-[var(--color-accent)]"
                      : "border-[var(--color-border-strong)]",
                  )}
                >
                  {mode === m.value ? (
                    <span className="size-2 rounded-full bg-[var(--color-accent)]" />
                  ) : null}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-2 text-sm font-medium text-[var(--color-fg)]">
                    {m.label}
                    {m.destructive ? (
                      <Badge tone="warning" className="font-mono text-[10px]">
                        destructive
                      </Badge>
                    ) : null}
                  </span>
                  <span className="text-xs text-[var(--color-fg-muted)]">
                    {m.description}
                  </span>
                </span>
              </label>
            ))}
          </div>
        </div>

        {requiresConfirm ? (
          <div>
            <Label htmlFor="gc-confirm" className="mb-2 inline-block">
              Type{" "}
              <code className="font-mono text-[var(--color-danger)]">
                {EXPECTED}
              </code>{" "}
              to confirm
            </Label>
            <Input
              id="gc-confirm"
              autoComplete="off"
              autoFocus
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="font-mono"
            />
          </div>
        ) : null}

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={trigger.isPending}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant={requiresConfirm ? "danger" : "accent"}
            onClick={() => void handleSubmit()}
            loading={trigger.isPending}
            disabled={!canSubmit}
          >
            <Play className="size-4" />
            Queue run
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
