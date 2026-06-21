import * as React from "react";
import {
  CheckCircle2,
  CircleX,
  Play,
  X,
  Timer,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { SeverityBar, SeverityLegend } from "@/components/security/severity-bar";
import type { ScanResult } from "@/lib/api/types";
import type { AdminTestScanResult } from "@/lib/api/admin-scanners";

interface TestScanPanelProps {
  result: AdminTestScanResult | undefined;
  loading: boolean;
  error: unknown;
  // Name of the adapter the test scan is hitting — surfaced in the
  // "Running test scan against …" copy so the operator knows which
  // adapter just ran.
  activeAdapterName: string;
  onRetry: () => void;
  // Dismiss button — clears the local mutation state so the panel goes
  // back to collapsed.
  onDismiss: () => void;
}

// Beacon — TestScanPanel.
//
// Inline panel that appears just below the adapter grid after the operator
// clicks "Run test scan" on the active card. Three states:
//   1. loading  → skeleton + "Running against {adapter}…"
//   2. ok=true  → success card with duration + severity bar
//   3. ok=false → ErrorState with the error_message + Retry CTA
//
// Persists for the session (state lives in the parent route). Backend hits
// the SCANNER_TEST_* fixture (defaults to dev tenant + dev/alpine:latest).
export function TestScanPanel({
  result,
  loading,
  error,
  activeAdapterName,
  onRetry,
  onDismiss,
}: TestScanPanelProps): React.ReactElement {
  if (loading) {
    return (
      <Card accentBar="accent">
        <CardHeader>
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Test scan
          </CardDescription>
          <CardTitle className="!text-base font-display !font-medium">
            Running test scan against{" "}
            <span className="font-mono">{activeAdapterName || "active adapter"}</span>
            …
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <Skeleton className="h-2 w-full" />
          <Skeleton className="h-3 w-2/3" />
          <p className="text-xs text-[var(--color-fg-muted)]">
            Trivy typically takes 5–10s on the dev fixture; cold starts
            (first scan after container recreate) can take ~30s while the
            CVE database downloads.
          </p>
        </CardContent>
      </Card>
    );
  }

  if (error) {
    return (
      <ErrorState
        title="Test scan request failed"
        description="The management BFF didn't accept the test-scan request. Verify SCANNER_GRPC_ADDR is set and the scanner service is reachable."
        onRetry={onRetry}
      />
    );
  }

  if (!result) return <></>;

  if (!result.ok) {
    return (
      <Card accentBar="danger">
        <CardHeader>
          <div className="flex items-center justify-between gap-3">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Test scan · {formatMs(result.duration_ms)}
            </CardDescription>
            <Button variant="ghost" size="sm" onClick={onDismiss} aria-label="Dismiss">
              <X className="size-3.5" />
            </Button>
          </div>
          <CardTitle className="!text-base font-display !font-medium">
            <span className="inline-flex items-center gap-1.5">
              <CircleX className="size-4 text-[var(--color-danger)]" />
              Test scan failed
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <pre className="overflow-x-auto whitespace-pre-wrap rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 p-3 font-mono text-xs text-[var(--color-fg)]">
            {result.error_message ?? "(no error message returned)"}
          </pre>
          <Button variant="outline" size="sm" onClick={onRetry}>
            <Play className="size-3.5" />
            Retry
          </Button>
        </CardContent>
      </Card>
    );
  }

  // ok=true — success card with severity counts. Backend returns
  // CRITICAL / HIGH / MEDIUM / LOW / NEGLIGIBLE; SeverityBar consumes
  // the first four. NEGLIGIBLE is shown in the dismiss-row count for
  // completeness but not in the bar (matches the rest of the app).
  const counts = (result.severity_counts ?? {}) as Record<string, number>;
  const severityCounts: ScanResult["severity_counts"] = {
    CRITICAL: counts.CRITICAL ?? 0,
    HIGH: counts.HIGH ?? 0,
    MEDIUM: counts.MEDIUM ?? 0,
    LOW: counts.LOW ?? 0,
  };
  const negligible = counts.NEGLIGIBLE ?? 0;
  const total =
    (severityCounts.CRITICAL ?? 0) +
    (severityCounts.HIGH ?? 0) +
    (severityCounts.MEDIUM ?? 0) +
    (severityCounts.LOW ?? 0) +
    negligible;

  return (
    <Card accentBar="success">
      <CardHeader>
        <div className="flex items-center justify-between gap-3">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Test scan
          </CardDescription>
          <Button variant="ghost" size="sm" onClick={onDismiss} aria-label="Dismiss">
            <X className="size-3.5" />
          </Button>
        </div>
        <CardTitle className="!text-base font-display !font-medium">
          <span className="inline-flex items-center gap-1.5">
            <CheckCircle2 className="size-4 text-[var(--color-success)]" />
            Test scan completed
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-[var(--color-fg-muted)]">
          <span className="inline-flex items-center gap-1.5">
            <Timer className="size-3.5" />
            <span className="tabular-nums text-[var(--color-fg)]">
              {formatMs(result.duration_ms)}
            </span>
          </span>
          {result.scanner_name ? (
            <span>
              Scanner{" "}
              <span className="font-mono text-[var(--color-fg)]">
                {result.scanner_name}@{result.scanner_version ?? "?"}
              </span>
            </span>
          ) : null}
          <span>
            <span className="font-mono tabular-nums text-[var(--color-fg)]">
              {total.toLocaleString()}
            </span>{" "}
            finding{total === 1 ? "" : "s"}
          </span>
        </div>

        <SeverityBar counts={severityCounts} />
        <SeverityLegend counts={severityCounts} />

        <p className="border-t border-[var(--color-border)] pt-3 text-[11px] text-[var(--color-fg-subtle)]">
          Scan ran against the configured fixture (env{" "}
          <code className="font-mono">SCANNER_TEST_*</code> on the BFF; defaults
          to <code className="font-mono">dev/alpine:latest</code> on the dev
          tenant).
        </p>
      </CardContent>
    </Card>
  );
}

// Render the duration in ms or s depending on magnitude — anything under
// a second reads as "612 ms"; longer scans read as "8.0s".
function formatMs(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${ms.toLocaleString()} ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}
