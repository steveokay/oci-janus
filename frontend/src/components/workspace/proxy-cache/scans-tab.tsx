import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import {
  CircleCheck,
  CircleX,
  Clock,
  Play,
  ShieldAlert,
  ShieldCheck,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import {
  SeverityBar,
  SeverityLegend,
} from "@/components/security/severity-bar";
import {
  useScanByDigest,
  useTriggerScanByDigest,
} from "@/lib/api/proxy-cache";
import { totalSeverityCount } from "@/lib/api/scan";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import type { ScanResult } from "@/lib/api/types";

// FUT-018 — Scans tab on the proxy-cache detail page.
//
// Renders the digest-keyed scan state for one cached manifest. Wraps
// the same SeverityBar / SeverityLegend primitives the per-tag
// ScanPanel uses so a CRITICAL count looks identical between an
// owned tag and a cached pull-through manifest — same visual
// vocabulary across the dashboard.
//
// Why a new component vs reusing `components/security/scan-panel.tsx`?
//   • That panel is keyed on a `ScanResult | null` prop and renders a
//     long findings table (top-50 CVEs). Cached manifests usually come
//     from upstream images we don't own — the operator's mental model
//     is "give me a posture summary, not a per-CVE drill-down here."
//     The detail page sits in the workspace nav, not the repo nav.
//   • We still expose the same Trigger/Rescan affordance; the trigger
//     POST goes to `/scan-by-digest/{digest}` (writer-or-above), which
//     useTriggerScanByDigest fronts.
//   • Hard reuse: SeverityBar + SeverityLegend + totalSeverityCount.

interface ScansTabProps {
  // The cached manifest's digest. Always present on the FUT-016
  // detail page (the BFF requires it on every cache row), so we don't
  // need a nullable variant.
  digest: string;
}

export function ScansTab({ digest }: ScansTabProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useScanByDigest(digest);
  const trigger = useTriggerScanByDigest();

  // triggering covers both "queue posted" + "404 → null-poll window"
  // so the button shows a spinner the whole time the operator is
  // waiting for the first poll to land. Once a scan row exists, the
  // pending/running pills take over (via InFlightCard).
  const triggering = trigger.isPending;

  const onTrigger = async () => {
    try {
      await trigger.mutateAsync(digest);
    } catch (e) {
      toast.error(triggerErrorMessage(e));
    }
  };

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load scan"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-7 w-56" />
        </CardHeader>
        <CardContent className="space-y-3">
          <Skeleton className="h-2 w-full" />
          <Skeleton className="h-3 w-2/3" />
        </CardContent>
      </Card>
    );
  }

  // No scan recorded yet (BFF returned 404). The hook also collapses
  // 403 here — a read-only tenant member sees the same CTA but the
  // mutation will reject with 403, surfaced as a toast.
  if (!data) {
    return (
      <EmptyState
        icon={<ShieldCheck className="size-5" />}
        title="No vulnerability scan yet"
        description="Trigger a scan and we'll surface findings here. Scans typically complete within a minute for small images."
        action={
          <Button
            onClick={() => void onTrigger()}
            loading={triggering}
            disabled={triggering}
          >
            <Play className="size-4" />
            {triggering ? "Queuing" : "Trigger scan"}
          </Button>
        }
      />
    );
  }

  if (data.status === "pending" || data.status === "running") {
    return <InFlightCard scan={data} />;
  }

  if (data.status === "failed") {
    return (
      <FailedCard
        scan={data}
        onTrigger={() => void onTrigger()}
        triggering={triggering}
      />
    );
  }

  return (
    <CompleteCard
      scan={data}
      onTrigger={() => void onTrigger()}
      triggering={triggering}
    />
  );
}

// ─── Status panels ──────────────────────────────────────────────────

// InFlightCard — "Queued" / "Scanning…" pill + the started-at
// relative timestamp. The 4s polling on useScanByDigest replaces the
// per-tag panel's "stuck after 90s" heuristic; the cached-manifest
// detail page doesn't show the admin scanner-liveness banner because
// that surface lives on the per-tag detail (DSGN-019).
function InFlightCard({ scan }: { scan: ScanResult }): React.ReactElement {
  return (
    <Card accentBar="warning">
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Vulnerability scan
          </CardDescription>
          <Badge tone="warning" dot pulse>
            {scan.status === "pending" ? "Queued" : "Scanning"}
          </Badge>
        </div>
        <CardTitle className="!text-lg font-display !font-medium">
          {scan.status === "pending"
            ? "Waiting for a scanner slot…"
            : `${scan.scanner_name || "Scanner"} is examining this image…`}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="flex items-center gap-3 text-sm text-[var(--color-fg-muted)]">
          <Clock className="size-4" />
          <span>
            Started {formatRelativeDate(scan.started_at)} · refreshes every few
            seconds
          </span>
        </div>
      </CardContent>
    </Card>
  );
}

function FailedCard({
  scan,
  onTrigger,
  triggering,
}: {
  scan: ScanResult;
  onTrigger: () => void;
  triggering: boolean;
}): React.ReactElement {
  return (
    <Card accentBar="danger">
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Vulnerability scan
          </CardDescription>
          <Badge tone="danger">
            <CircleX className="size-3" /> Failed
          </Badge>
        </div>
        <CardTitle className="!text-lg font-display !font-medium">
          The last scan didn't complete.
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-[var(--color-fg-muted)]">
          {scan.scanner_name || "The scanner"} reported an error before
          producing findings. Re-queue, or check{" "}
          <code className="font-mono">registry-scanner</code> logs.
        </p>
        <Button onClick={onTrigger} loading={triggering} disabled={triggering}>
          <Play className="size-4" />
          {triggering ? "Queuing" : "Trigger scan again"}
        </Button>
      </CardContent>
    </Card>
  );
}

function CompleteCard({
  scan,
  onTrigger,
  triggering,
}: {
  scan: ScanResult;
  onTrigger: () => void;
  triggering: boolean;
}): React.ReactElement {
  const total = totalSeverityCount(scan.severity_counts);
  // accentBar mirrors the worst severity present so the card header
  // visually communicates risk at a glance. Matches ScanPanel's posture
  // for the per-tag surface.
  const accentBar =
    (scan.severity_counts?.CRITICAL ?? 0) > 0
      ? ("danger" as const)
      : (scan.severity_counts?.HIGH ?? 0) > 0
        ? ("warning" as const)
        : ("success" as const);

  const headline =
    total === 0
      ? "Clean — no vulnerabilities found."
      : `${total.toLocaleString()} finding${total === 1 ? "" : "s"}`;

  return (
    <Card accentBar={accentBar}>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Vulnerability scan
          </CardDescription>
          <div className="flex items-center gap-2">
            {total === 0 ? (
              <Badge tone="success">
                <CircleCheck className="size-3" /> Clean
              </Badge>
            ) : (
              <Badge tone={accentBar === "danger" ? "danger" : "warning"}>
                <ShieldAlert className="size-3" />
                {total.toLocaleString()} open
              </Badge>
            )}
          </div>
        </div>
        <CardTitle className="!text-lg font-display !font-medium">
          {headline}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <SeverityBar counts={scan.severity_counts} />
        <SeverityLegend counts={scan.severity_counts} />
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--color-border)] pt-3 text-xs text-[var(--color-fg-muted)]">
          <span>
            Scanned by{" "}
            <span className="font-mono text-[var(--color-fg)]">
              {scan.scanner_name || "scanner"}
              {scan.scanner_version ? `@${scan.scanner_version}` : ""}
            </span>
            {scan.completed_at ? (
              <> · {formatAbsoluteDate(scan.completed_at)}</>
            ) : null}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={onTrigger}
            loading={triggering}
            disabled={triggering}
            data-testid="rescan-button"
          >
            <Play className="size-3" />
            Rescan
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

// triggerErrorMessage — maps the BFF's error envelope to an
// operator-friendly string. 403 is the only branch with bespoke copy;
// other statuses fall back to whatever the BFF returned in `error`.
function triggerErrorMessage(err: unknown): string {
  if (err instanceof AxiosError) {
    const status = err.response?.status;
    if (status === 403) return "Writer role required to trigger a scan.";
    if (status === 404)
      return "Scanner service is not wired on this BFF (SCANNER_GRPC_ADDR).";
    const detail = (err.response?.data as { error?: string } | undefined)?.error;
    if (detail) return detail;
    return err.message;
  }
  if (err instanceof Error) return err.message;
  return "Couldn't queue scan.";
}
