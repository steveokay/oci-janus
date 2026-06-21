import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { FileText, Plus } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import {
  isInFlight,
  useDownloadReport,
  useGenerateReport,
  useReports,
  type ComplianceReport,
  type ReportFormat,
  type ReportStatus,
} from "@/lib/api/compliance-reports";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

// Beacon — ReportsPanel (FE-API-019).
//
// Header strip: "Generate report" button. Body: table of report rows. The
// rows poll every 10s while any row is in-flight; once everything is
// terminal the poll stops to avoid hammering the BFF.

// statusTone — Badge tone keyed to ReportStatus. Falls back to neutral for
// anything unrecognised (forward-compat guard if a new status ever lands).
function statusTone(
  s: string,
): React.ComponentProps<typeof Badge>["tone"] {
  switch (s as ReportStatus) {
    case "pending":
      return "neutral";
    case "running":
      return "accent";
    case "succeeded":
      return "success";
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
}

export function ReportsPanel(): React.ReactElement {
  // First-pass list to compute the polling decision. Then a derived
  // `refetchInterval` keeps the cadence honest while a row is in-flight.
  const q = useReports({ perPage: 50 });
  const gen = useGenerateReport();
  const download = useDownloadReport();

  const rows = React.useMemo(
    () => q.data?.pages.flatMap((p) => p.reports) ?? [],
    [q.data],
  );

  // While any visible row is pending/running we poll every 10s. The poll
  // gate flips off naturally when every row reaches a terminal state, so
  // the UI does not spam the network for an idle workspace.
  const hasInflight = rows.some((r) => isInFlight(r.status));
  React.useEffect(() => {
    if (!hasInflight) return;
    const id = window.setInterval(() => {
      void q.refetch();
    }, 10_000);
    return () => window.clearInterval(id);
  }, [hasInflight, q]);

  async function handleGenerate(): Promise<void> {
    try {
      await gen.mutateAsync();
      // Toast message — keep the language neutral; the table will surface
      // the new row + carry it through running → succeeded.
      toast.message("Report queued", {
        description: "It'll appear in the table below and update as the worker progresses.",
      });
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 404
          ? "Scanner backend isn't wired on the management BFF."
          : "Couldn't queue a report. Try again, or check the BFF logs.",
      );
    }
  }

  async function handleDownload(
    report: ComplianceReport,
    format: ReportFormat,
  ): Promise<void> {
    try {
      await download.mutateAsync({ id: report.report_id, format });
    } catch (e) {
      const err = e as { message?: string };
      toast.error(err.message ?? "Download failed.");
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="text-xs text-[var(--color-fg-muted)]">
          PDF + SPDX 2.3 SBOM rendered on demand. Each generate cycles the
          worker; expect a few seconds end-to-end on a small tenant.
        </div>
        <Button onClick={() => void handleGenerate()} disabled={gen.isPending} loading={gen.isPending}>
          <Plus className="size-4" />
          {gen.isPending ? "Queuing" : "Generate report"}
        </Button>
      </div>

      {q.isError ? (
        <ErrorState
          title="Couldn't load reports"
          description="The /security/reports endpoint didn't answer. Try again, or check the BFF logs."
          onRetry={() => void q.refetch()}
        />
      ) : q.isLoading ? (
        <ReportsSkeleton />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<FileText className="size-5" />}
          title="No compliance reports yet"
          description="Generate the first one — it bundles the latest scan state for every repository in this workspace."
          action={
            <Button onClick={() => void handleGenerate()} disabled={gen.isPending}>
              <Plus className="size-4" />
              Generate report
            </Button>
          }
        />
      ) : (
        <Card>
          <CardHeader className="pb-3">
            <div className="flex items-center justify-between gap-2">
              <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                Reports
              </CardDescription>
              {hasInflight ? (
                <Badge tone="accent" dot pulse>
                  Polling
                </Badge>
              ) : null}
            </div>
          </CardHeader>
          <CardContent className="pt-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Report ID</TableHead>
                  <TableHead>Requested by</TableHead>
                  <TableHead>Requested</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Download</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r) => (
                  <ReportRow
                    key={r.report_id}
                    r={r}
                    onDownload={(format) => void handleDownload(r, format)}
                    downloading={download.isPending}
                  />
                ))}
              </TableBody>
            </Table>

            {q.hasNextPage ? (
              <div className="mt-4 flex justify-center">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void q.fetchNextPage()}
                  loading={q.isFetchingNextPage}
                  disabled={q.isFetchingNextPage}
                >
                  {q.isFetchingNextPage ? "Loading…" : "Load more"}
                </Button>
              </div>
            ) : null}
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function ReportRow({
  r,
  onDownload,
  downloading,
}: {
  r: ComplianceReport;
  onDownload: (format: ReportFormat) => void;
  downloading: boolean;
}): React.ReactElement {
  const succeeded = r.status === "succeeded";
  const failed = r.status === "failed";
  const inflight = isInFlight(r.status);

  return (
    <TableRow>
      <TableCell>
        <code className="block max-w-[180px] truncate font-mono text-xs text-[var(--color-fg)]">
          {r.report_id}
        </code>
      </TableCell>
      <TableCell>
        <code className="font-mono text-xs text-[var(--color-fg-muted)]">
          {r.requested_by || "—"}
        </code>
      </TableCell>
      <TableCell className="text-xs text-[var(--color-fg-muted)]">
        {r.requested_at ? (
          <span title={formatAbsoluteDate(r.requested_at)}>
            {formatRelativeDate(r.requested_at)}
          </span>
        ) : (
          "—"
        )}
      </TableCell>
      <TableCell>
        <div className="flex flex-col gap-1">
          <Badge tone={statusTone(r.status)} dot={inflight} pulse={inflight}>
            {r.status}
          </Badge>
          {failed && r.error_message ? (
            <p className="max-w-[280px] truncate font-mono text-[10px] text-[var(--color-danger)]" title={r.error_message}>
              {r.error_message}
            </p>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="text-right">
        {succeeded ? (
          <div className="inline-flex gap-1.5">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onDownload("pdf")}
              disabled={downloading}
            >
              PDF
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onDownload("sbom")}
              disabled={downloading}
            >
              SBOM
            </Button>
          </div>
        ) : (
          <span className="text-xs italic text-[var(--color-fg-subtle)]">
            {inflight ? "Generating…" : "Unavailable"}
          </span>
        )}
      </TableCell>
    </TableRow>
  );
}

function ReportsSkeleton(): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <Skeleton className="h-3 w-24" />
      </CardHeader>
      <CardContent className="space-y-2 pt-0">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-10 w-full" />
        ))}
      </CardContent>
    </Card>
  );
}
