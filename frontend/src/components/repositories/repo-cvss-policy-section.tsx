import * as React from "react";
import { ShieldCheck, ShieldAlert, AlertTriangle } from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useRepository, useUpdateRepository } from "@/lib/api/repositories";
import { cn } from "@/lib/utils";

// RepoCVSSPolicySection — Settings-tab card on the repo detail page.
//
// Toggles the `max_cvss_score` threshold on the parent repository
// (futures.md FUT-021). When set, services/core's GetManifest reads
// the latest scan result for the manifest and rejects any pull whose
// top CVSS score exceeds the threshold — the OCI client sees a
// 403 DENIED with the numeric context in the body so CI tooling can
// decide waive / patch / rebuild.
//
// Distinct from RepoScanPolicySection (FE-API-049): that card gates
// which scans FIRE (auto-scan-on-push + block-on-severity for pushes);
// this card gates which PULLS are allowed based on the already-stored
// scan verdict. Both live on the Settings tab but occupy separate
// mental slots — one is push admission, one is pull admission.
//
// Load-bearing three-state semantics:
//   - null (undefined)  → no gate, pull-through
//   - threshold set     → gate active
// The Switch drives which state we send to the BFF; the number input
// only appears when the switch is on. Turning the switch off sends an
// explicit `max_cvss_score: null` so the BFF's UnmarshalJSON path
// clears the SQL column instead of leaving stale state behind.
//
// Standard band midpoints (matches services/core.topCVSSFromSeverity):
//   LOW=39, MEDIUM=69, HIGH=89, CRITICAL=100
// So:
//   threshold 100 → blocks nothing (opt-in default; 0-severity guardrail)
//   threshold  89 → blocks CRITICAL only
//   threshold  69 → blocks HIGH + CRITICAL
//   threshold  39 → blocks MEDIUM + HIGH + CRITICAL
//   threshold   0 → blocks ANY finding

interface RepoCVSSPolicySectionProps {
  org: string;
  repo: string;
}

// Preset labels for the operator. Values are the midpoints so a click
// on "Block HIGH+CRITICAL" sets 69 — the exact number that means "any
// HIGH finding (midpoint 89) exceeds this threshold".
const PRESETS: Array<{ label: string; value: number; hint: string }> = [
  { label: "CRITICAL only", value: 89, hint: "89" },
  { label: "HIGH + CRITICAL", value: 69, hint: "69" },
  { label: "MEDIUM + up", value: 39, hint: "39" },
  { label: "Any finding", value: 0, hint: "0" },
];

// Default threshold applied when the switch is flipped on. Matches the
// most common operator posture ("block HIGH + CRITICAL") so the user
// doesn't have to guess at a starting number.
const DEFAULT_THRESHOLD = 69;

export function RepoCVSSPolicySection({
  org,
  repo,
}: RepoCVSSPolicySectionProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useRepository(org, repo);
  const update = useUpdateRepository();
  const current = data?.max_cvss_score ?? null;
  const enabled = current !== null;

  // Local draft state so the operator can adjust the number without
  // triggering a PATCH on every keystroke. Committed on Save.
  const [draft, setDraft] = React.useState<number>(current ?? DEFAULT_THRESHOLD);
  const [validationErr, setValidationErr] = React.useState<string | null>(null);

  // Sync the draft when the server value changes (e.g. after another
  // tab / session flipped the value). Guarded so the operator's active
  // edit is not overwritten on background refetch.
  React.useEffect(() => {
    if (current !== null) setDraft(current);
  }, [current]);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load repository settings"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  async function toggle(next: boolean): Promise<void> {
    try {
      await update.mutateAsync({
        org,
        repo,
        max_cvss_score: next ? DEFAULT_THRESHOLD : null,
      });
      toast.success(
        next
          ? `CVSS admission enabled — pulls exceeding score ${DEFAULT_THRESHOLD} will be rejected.`
          : "CVSS admission disabled — all pulls allowed regardless of scan result.",
      );
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : "Couldn't update CVSS policy. Check the BFF logs.",
      );
    }
  }

  async function saveThreshold(): Promise<void> {
    // Guard the wire — the BFF re-validates but a client-side check
    // avoids the round-trip on obviously wrong input.
    if (Number.isNaN(draft) || draft < 0 || draft > 100) {
      setValidationErr("Threshold must be an integer 0-100.");
      return;
    }
    setValidationErr(null);
    try {
      await update.mutateAsync({ org, repo, max_cvss_score: draft });
      toast.success(`CVSS admission threshold set to ${draft}.`);
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : "Couldn't save threshold. Check the BFF logs.",
      );
    }
  }

  const isDirty = enabled && draft !== current;

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-1">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              CVSS admission gate
            </CardDescription>
            <p className="text-xs text-[var(--color-fg-muted)]">
              When enabled, pulls are rejected when the latest scan's
              top CVSS score exceeds the threshold. Fails OPEN if the
              scan hasn't run yet — first-time pulls always succeed.
              Uses standard v3.1 band midpoints:{" "}
              <code className="font-mono text-[10px]">LOW=39</code>,{" "}
              <code className="font-mono text-[10px]">MEDIUM=69</code>,{" "}
              <code className="font-mono text-[10px]">HIGH=89</code>,{" "}
              <code className="font-mono text-[10px]">CRITICAL=100</code>.
            </p>
          </div>
          {isLoading ? (
            <Skeleton className="size-4 rounded-full" />
          ) : enabled ? (
            <Badge tone="success">
              <ShieldCheck className="size-3" /> Threshold {current}
            </Badge>
          ) : (
            <Badge tone="neutral">
              <ShieldAlert className="size-3" /> Open
            </Badge>
          )}
        </div>
      </CardHeader>
      <CardContent className="pt-0 space-y-3">
        {isLoading ? (
          <Skeleton className="h-10 w-full" />
        ) : (
          <label className="flex cursor-pointer items-center justify-between gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2">
            <span className="flex flex-col gap-0.5">
              <span className="text-sm font-medium text-[var(--color-fg)]">
                Block pulls when CVSS exceeds threshold
              </span>
              <span className="text-xs text-[var(--color-fg-muted)]">
                Turn on with the default threshold ({DEFAULT_THRESHOLD});
                tune below. Turning off clears the gate entirely.
              </span>
            </span>
            <Switch
              checked={enabled}
              disabled={update.isPending}
              onCheckedChange={(next) => void toggle(next)}
              aria-label="Enable CVSS admission"
            />
          </label>
        )}
        {enabled ? (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <label
                htmlFor="cvss-threshold"
                className="text-xs font-medium text-[var(--color-fg)]"
              >
                Threshold (0-100):
              </label>
              <Input
                id="cvss-threshold"
                type="number"
                min={0}
                max={100}
                step={1}
                value={draft}
                disabled={update.isPending}
                onChange={(e) => {
                  const v = Number(e.target.value);
                  setDraft(v);
                  if (!Number.isNaN(v) && v >= 0 && v <= 100) {
                    setValidationErr(null);
                  }
                }}
                className="w-20"
              />
              <Button
                size="sm"
                disabled={!isDirty || update.isPending}
                onClick={() => void saveThreshold()}
              >
                {update.isPending ? "Saving…" : "Save"}
              </Button>
            </div>
            <div className="flex flex-wrap gap-2">
              {PRESETS.map((p) => (
                <button
                  key={p.value}
                  type="button"
                  onClick={() => setDraft(p.value)}
                  className={cn(
                    "rounded-md border border-[var(--color-border)] px-2 py-1 text-[10px] font-medium uppercase tracking-wider transition-colors",
                    draft === p.value
                      ? "bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
                      : "hover:bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]",
                  )}
                >
                  {p.label} <span className="opacity-60">({p.hint})</span>
                </button>
              ))}
            </div>
            {validationErr ? (
              <p className="flex items-start gap-2 rounded-md border border-red-400/30 bg-red-100/20 px-3 py-2 text-xs text-red-500">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                {validationErr}
              </p>
            ) : null}
            <p className="flex items-start gap-2 rounded-md border border-[var(--color-accent)]/30 bg-[var(--color-accent-subtle)]/30 px-3 py-2 text-xs text-[var(--color-fg)]">
              <ShieldCheck className="mt-0.5 size-3.5 shrink-0 text-[var(--color-accent)]" />
              <span>
                Pulls whose top CVSS score exceeds{" "}
                <code className="font-mono">{current}</code> return{" "}
                <code className="font-mono">403 DENIED</code>. First pulls
                (before a scan has run) always succeed — the gate never
                blocks a legitimate first push→pull cycle.
              </span>
            </p>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
