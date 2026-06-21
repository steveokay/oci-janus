import * as React from "react";
import {
  ShieldCheck,
  ShieldAlert,
  CircleCheck,
  CircleX,
  Clock,
  Play,
  AlertTriangle,
  ExternalLink,
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  SeverityBar,
  SeverityLegend,
} from "@/components/security/severity-bar";
import {
  parseFindings,
  SEVERITY_ORDER,
  type ScanFinding,
  type SeverityKey,
  totalSeverityCount,
} from "@/lib/api/scan";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import type { ScanResult } from "@/lib/api/types";

interface ScanPanelProps {
  scan: ScanResult | null | undefined;
  loading?: boolean;
  isError?: boolean;
  triggering?: boolean;
  onTrigger: () => void;
  onRetry: () => void;
}

// Beacon — ScanPanel. Renders the four real states a scan can be in
// (pending / running / complete / failed) plus the "no scan yet" prequel
// and the network error case. The Trivy findings table only renders when
// `findings_json` is present + parseable; otherwise the panel summarizes
// from `severity_counts` alone.
export function ScanPanel({
  scan,
  loading,
  isError,
  triggering,
  onTrigger,
  onRetry,
}: ScanPanelProps): React.ReactElement {
  if (isError) {
    return (
      <ErrorState
        title="Couldn't load scan"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={onRetry}
      />
    );
  }

  if (loading) {
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

  if (!scan) {
    return (
      <EmptyState
        icon={<ShieldCheck className="size-5" />}
        title="No vulnerability scan yet"
        description="Trigger a scan and we'll surface findings here. Scans typically complete within a minute for small images."
        action={
          <Button onClick={onTrigger} loading={triggering} disabled={triggering}>
            <Play className="size-4" />
            {triggering ? "Queuing" : "Trigger scan"}
          </Button>
        }
      />
    );
  }

  if (scan.status === "pending" || scan.status === "running") {
    return <InFlightCard scan={scan} />;
  }

  if (scan.status === "failed") {
    return <FailedCard scan={scan} onTrigger={onTrigger} triggering={triggering} />;
  }

  return (
    <CompleteCard
      scan={scan}
      onTrigger={onTrigger}
      triggering={triggering}
    />
  );
}

// ─── status panels ──────────────────────────────────────────────────────────

// STUCK_THRESHOLD_MS — how long a scan can sit in pending/running before
// we degrade the "Scanning…" UI to "Scanner isn't producing results."
// 90 seconds covers the realistic worst case for a small image (Trivy
// DB download, layer extraction, scan). Anything past that almost
// always means the scanner profile isn't running or the adapter
// crashed silently — better to surface that than spin forever.
const STUCK_THRESHOLD_MS = 90_000;

function InFlightCard({ scan }: { scan: ScanResult }): React.ReactElement {
  // Compute "stuck" client-side. Don't trust the started_at parse if
  // it's malformed — fall back to "in flight" so a parse bug never
  // turns into a permanent stuck banner.
  const startedMs = Date.parse(scan.started_at);
  const isStuck =
    Number.isFinite(startedMs) && Date.now() - startedMs > STUCK_THRESHOLD_MS;

  if (isStuck) {
    return (
      <Card accentBar="danger">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Vulnerability scan
            </CardDescription>
            <Badge tone="warning">
              <Clock className="size-3" /> Stuck
            </Badge>
          </div>
          <CardTitle className="!text-lg font-display !font-medium">
            Scanner isn't producing results.
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-[var(--color-fg-muted)]">
            We queued this scan {formatRelativeDate(scan.started_at)} but
            the scanner hasn't written a result yet. The most common cause
            in dev is that the scanner profile isn't running.
          </p>
          <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3 font-mono text-xs">
            docker compose --profile scanner up -d registry-scanner
          </div>
          <p className="text-xs text-[var(--color-fg-subtle)]">
            See <code className="font-mono">docs/SCANNER.md</code> for the
            adapter contract + how to swap between Trivy and the dev stub.
            REM-011 tracks bringing this surface to first-class
            "is the scanner alive?" detection on the backend.
          </p>
        </CardContent>
      </Card>
    );
  }

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
            : `${scan.scanner_name} is examining this image…`}
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
  triggering?: boolean;
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
      <CardContent className="space-y-4">
        <div className="flex items-start gap-3 rounded-md border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 p-3 text-sm">
          <AlertTriangle className="size-4 shrink-0 text-[var(--color-danger)]" />
          <p className="text-[var(--color-fg-muted)]">
            {scan.scanner_name} reported an error before producing findings.
            Re-queue, or check <code className="font-mono">registry-scanner</code> logs.
          </p>
        </div>
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
  triggering?: boolean;
}): React.ReactElement {
  const total = totalSeverityCount(scan.severity_counts);
  const findings = React.useMemo(
    () => parseFindings(scan.findings_json),
    [scan.findings_json],
  );

  // Tone the card based on whether anything CRITICAL or HIGH was found —
  // operator's eye lands on the right thing without reading numbers.
  // Backend may return null severity_counts on pending / failed scans;
  // optional chaining keeps the card from crashing in that state.
  const accentBar =
    (scan.severity_counts?.CRITICAL ?? 0) > 0
      ? "danger"
      : (scan.severity_counts?.HIGH ?? 0) > 0
        ? "warning"
        : "success";

  const headline =
    total === 0
      ? "Clean — no vulnerabilities found."
      : `${total.toLocaleString()} finding${total === 1 ? "" : "s"}`;

  return (
    <div className="space-y-4">
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
                {scan.scanner_name}@{scan.scanner_version}
              </span>{" "}
              · {formatAbsoluteDate(scan.completed_at)}
            </span>
            <Button
              variant="outline"
              size="sm"
              onClick={onTrigger}
              loading={triggering}
              disabled={triggering}
            >
              <Play className="size-3" />
              Rescan
            </Button>
          </div>
        </CardContent>
      </Card>

      {findings.length > 0 ? <FindingsTable findings={findings} /> : null}
    </div>
  );
}

// ─── findings table ────────────────────────────────────────────────────────

function FindingsTable({
  findings,
}: {
  findings: ScanFinding[];
}): React.ReactElement {
  // Sort CRITICAL → HIGH → MEDIUM → LOW → rest. Within a severity, keep
  // input order so the scanner's own ranking is preserved.
  const sorted = React.useMemo(() => {
    const rank: Record<string, number> = {
      CRITICAL: 0,
      HIGH: 1,
      MEDIUM: 2,
      LOW: 3,
    };
    return [...findings].sort(
      (a, b) =>
        (rank[(a.severity ?? "").toUpperCase()] ?? 99) -
        (rank[(b.severity ?? "").toUpperCase()] ?? 99),
    );
  }, [findings]);

  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
      <div className="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-3">
        <span className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Findings
        </span>
        <span className="text-xs text-[var(--color-fg-muted)]">
          {findings.length.toLocaleString()} total
        </span>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[110px]">Severity</TableHead>
            <TableHead>CVE</TableHead>
            <TableHead>Package</TableHead>
            <TableHead>Fixed in</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sorted.slice(0, 50).map((f, i) => {
            const sev = (f.severity ?? "").toUpperCase() as SeverityKey;
            const tone: "critical" | "high" | "medium" | "low" | "neutral" =
              SEVERITY_ORDER.includes(sev)
                ? (sev.toLowerCase() as Exclude<typeof tone, "neutral">)
                : "neutral";
            return (
              <TableRow key={`${f.vulnerability_id}-${i}`}>
                <TableCell>
                  <Badge tone={tone}>{f.severity ?? "—"}</Badge>
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <code className="font-mono text-xs font-medium">
                      {f.vulnerability_id ?? "—"}
                    </code>
                    {f.primary_url ? (
                      <a
                        href={f.primary_url}
                        target="_blank"
                        rel="noreferrer noopener"
                        className="text-[var(--color-accent)] hover:underline"
                        aria-label={`Open ${f.vulnerability_id} reference`}
                      >
                        <ExternalLink className="size-3" />
                      </a>
                    ) : null}
                  </div>
                  {f.title ? (
                    <div className="mt-0.5 truncate text-xs text-[var(--color-fg-muted)]">
                      {f.title}
                    </div>
                  ) : null}
                </TableCell>
                <TableCell className="font-mono text-xs">
                  <div>{f.package_name ?? "—"}</div>
                  {f.installed_version ? (
                    <div className="text-[var(--color-fg-muted)]">
                      {f.installed_version}
                    </div>
                  ) : null}
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {f.fixed_version ? (
                    <span className="text-[var(--color-success)]">
                      {f.fixed_version}
                    </span>
                  ) : (
                    <span className="text-[var(--color-fg-subtle)]">
                      no fix yet
                    </span>
                  )}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      {findings.length > 50 ? (
        <div className="border-t border-[var(--color-border)] px-4 py-3 text-center text-xs text-[var(--color-fg-muted)]">
          Showing top 50 of {findings.length.toLocaleString()} findings.
        </div>
      ) : null}
    </div>
  );
}
