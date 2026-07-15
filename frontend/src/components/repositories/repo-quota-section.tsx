import * as React from "react";
import { HardDrive, AlertTriangle } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useRepository, useUpdateRepository } from "@/lib/api/repositories";
import { formatBytes } from "@/lib/format";

// RepoQuotaSection — General-section card on the repo Settings tab (Tier 2 #2).
//
// Overrides the per-repo storage cap. Wires the existing (previously orphaned)
// UpdateRepositoryQuota metadata RPC through the PATCH route's new
// `storage_quota_bytes` field. Operators think in GB/TB, so the input takes a
// number + unit and converts to bytes on Save; the BFF rejects a non-positive
// value with a 400.

interface RepoQuotaSectionProps {
  org: string;
  repo: string;
}

const UNITS = ["GB", "TB"] as const;
type Unit = (typeof UNITS)[number];

const UNIT_MULTIPLIER: Record<Unit, number> = {
  GB: 1024 ** 3,
  TB: 1024 ** 4,
};

export function RepoQuotaSection({
  org,
  repo,
}: RepoQuotaSectionProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useRepository(org, repo);
  const update = useUpdateRepository();
  const currentBytes = data?.storage_quota_bytes ?? 0;
  const usedBytes = data?.storage_used_bytes ?? 0;

  // Draft as a number-in-GB string so typing doesn't PATCH per keystroke.
  // Default unit is GB — the platform default quota (10 GiB) reads cleanly.
  const [value, setValue] = React.useState<string>("");
  const [unit, setUnit] = React.useState<Unit>("GB");

  // Seed / re-seed the input from the server value when it changes. Keying on
  // currentBytes means an unchanged background refetch won't clobber an edit.
  React.useEffect(() => {
    if (currentBytes > 0) {
      setValue(String(currentBytes / UNIT_MULTIPLIER.GB));
      setUnit("GB");
    }
  }, [currentBytes]);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load repository settings"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  const parsed = Number(value);
  const valid = Number.isFinite(parsed) && parsed > 0;
  const previewBytes = valid ? Math.floor(parsed * UNIT_MULTIPLIER[unit]) : 0;
  const isDirty = valid && previewBytes !== currentBytes;
  const belowUsage = valid && previewBytes < usedBytes;

  async function save(): Promise<void> {
    if (!valid) return;
    try {
      await update.mutateAsync({ org, repo, storage_quota_bytes: previewBytes });
      toast.success(`Storage quota set to ${formatBytes(previewBytes, 2)}.`);
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : "Couldn't update the storage quota. Check the BFF logs.",
      );
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start gap-2">
          <HardDrive className="mt-0.5 size-4 shrink-0 text-[var(--color-fg-subtle)]" />
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Storage quota
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Per-repo cap on stored bytes. Pushes that would exceed it are
              rejected. Takes effect on the next push.
            </p>
          </div>
        </div>
      </CardHeader>
      <CardContent className="pt-0 space-y-3">
        {isLoading ? (
          <Skeleton className="h-10 w-full" />
        ) : (
          <>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Currently using{" "}
              <span className="font-mono text-[var(--color-fg)]">
                {formatBytes(usedBytes, 2)}
              </span>{" "}
              of{" "}
              <span className="font-mono text-[var(--color-fg)]">
                {formatBytes(currentBytes, 2)}
              </span>
              .
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <label
                htmlFor="repo-quota"
                className="text-xs font-medium text-[var(--color-fg)]"
              >
                New quota:
              </label>
              <Input
                id="repo-quota"
                type="number"
                inputMode="decimal"
                min={0}
                step={1}
                value={value}
                disabled={update.isPending}
                onChange={(e) => setValue(e.target.value)}
                className="w-24 font-mono"
              />
              <div className="flex gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-1">
                {UNITS.map((u) => {
                  const active = unit === u;
                  return (
                    <button
                      key={u}
                      type="button"
                      onClick={() => setUnit(u)}
                      aria-pressed={active}
                      className={
                        active
                          ? "rounded-sm bg-[var(--color-surface-sunken)] px-3 py-1 text-xs font-medium text-[var(--color-fg)]"
                          : "rounded-sm px-3 py-1 text-xs font-medium text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
                      }
                    >
                      {u}
                    </button>
                  );
                })}
              </div>
              <Button
                size="sm"
                disabled={!isDirty || update.isPending}
                onClick={() => void save()}
              >
                {update.isPending ? "Saving…" : "Save"}
              </Button>
            </div>
            {valid ? (
              <p className="text-[11px] text-[var(--color-fg-subtle)]">
                = {formatBytes(previewBytes, 2)} (
                {previewBytes.toLocaleString()} bytes)
              </p>
            ) : null}
            {belowUsage ? (
              <p className="flex items-start gap-2 rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning-subtle)]/30 px-3 py-2 text-xs text-[var(--color-fg)]">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-[var(--color-warning)]" />
                <span>
                  This is below the {formatBytes(usedBytes, 2)} already stored —
                  existing blobs are kept, but every new push will be rejected
                  until usage drops below the cap.
                </span>
              </p>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  );
}
