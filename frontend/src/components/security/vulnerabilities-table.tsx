import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  ChevronDown,
  ChevronRight,
  ExternalLink,
  ShieldCheck,
} from "lucide-react";
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
  SEVERITIES,
  severityTone,
  useVulnerabilities,
  type Severity,
} from "@/lib/api/security";
import { formatRelativeDate, formatAbsoluteDate } from "@/lib/format";
import { cn } from "@/lib/utils";

// VulnerabilitiesTable — FE-API-014.
//
// One row per rolled-up CVE, with an inline-expandable "affected" list per
// row so the operator can drill into the (repo, tag, digest) triples
// without leaving the page. Severity chips above the table drive the
// `severity` query param.
//
// Pagination uses the BFF's opaque page_token via useInfiniteQuery; the
// "Load more" button only renders when there's a next page.
export function VulnerabilitiesTable(): React.ReactElement {
  const [severity, setSeverity] = React.useState<Severity | "">("");
  const q = useVulnerabilities({ severity });

  const flat = React.useMemo(
    () => q.data?.pages.flatMap((p) => p.vulnerabilities) ?? [],
    [q.data],
  );

  return (
    <div className="space-y-4">
      <SeverityChips selected={severity} onChange={setSeverity} />

      {q.isError ? (
        <ErrorState
          title="Couldn't load vulnerabilities"
          description="The metadata service didn't answer. Try again, or check the BFF logs."
          onRetry={() => void q.refetch()}
        />
      ) : q.isLoading ? (
        <SkeletonRows />
      ) : flat.length === 0 ? (
        <EmptyState
          icon={<ShieldCheck className="size-5" />}
          title={
            severity
              ? `No ${severity.toLowerCase()} findings`
              : "Workspace is clean"
          }
          description={
            severity
              ? "Clear the severity filter to see findings at other severities."
              : "No open CVEs across any tag's most-recent scan. Trigger a scan on any tag to refresh."
          }
        />
      ) : (
        <>
          <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[40px]"></TableHead>
                  <TableHead>CVE</TableHead>
                  <TableHead>Severity</TableHead>
                  <TableHead>Package</TableHead>
                  <TableHead>Fix</TableHead>
                  <TableHead className="hidden md:table-cell">
                    Affected
                  </TableHead>
                  <TableHead className="hidden lg:table-cell">Last seen</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {flat.map((v) => (
                  <VulnRow key={v.cve_id} v={v} />
                ))}
              </TableBody>
            </Table>
          </div>

          {q.hasNextPage ? (
            <div className="flex justify-center">
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
        </>
      )}
    </div>
  );
}

function SeverityChips({
  selected,
  onChange,
}: {
  selected: Severity | "";
  onChange: (s: Severity | "") => void;
}): React.ReactElement {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <ChipButton
        label="All"
        active={selected === ""}
        onClick={() => onChange("")}
      />
      {SEVERITIES.map((s) => (
        <ChipButton
          key={s}
          label={s}
          active={selected === s}
          onClick={() => onChange(s)}
        />
      ))}
    </div>
  );
}

function ChipButton({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}): React.ReactElement {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors",
        active
          ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
          : "border-[var(--color-border-strong)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
      )}
    >
      {label}
    </button>
  );
}

interface VulnRowProps {
  v: ReturnType<typeof useVulnerabilities>["data"] extends infer D
    ? D extends { pages: Array<{ vulnerabilities: Array<infer V> }> }
      ? V
      : never
    : never;
}

function VulnRow({ v }: VulnRowProps): React.ReactElement {
  const [open, setOpen] = React.useState(false);
  const fix = v.fixed_in?.trim();
  const nvdHref = v.cve_id.startsWith("CVE-")
    ? `https://nvd.nist.gov/vuln/detail/${encodeURIComponent(v.cve_id)}`
    : null;

  return (
    <>
      <TableRow>
        <TableCell className="w-[40px] !pr-0">
          <button
            type="button"
            onClick={() => setOpen((p) => !p)}
            aria-label={open ? "Collapse affected list" : "Expand affected list"}
            className="inline-grid size-6 place-items-center rounded text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)]"
          >
            {open ? (
              <ChevronDown className="size-3.5" />
            ) : (
              <ChevronRight className="size-3.5" />
            )}
          </button>
        </TableCell>
        <TableCell>
          <div className="flex flex-col gap-0.5">
            {nvdHref ? (
              <a
                href={nvdHref}
                target="_blank"
                rel="noreferrer"
                className="flex items-center gap-1 font-mono text-sm font-medium text-[var(--color-accent)] hover:underline"
              >
                {v.cve_id}
                <ExternalLink className="size-3" />
              </a>
            ) : (
              <code className="font-mono text-sm font-medium text-[var(--color-fg)]">
                {v.cve_id}
              </code>
            )}
            {v.title ? (
              <span className="line-clamp-1 text-xs text-[var(--color-fg-muted)]">
                {v.title}
              </span>
            ) : null}
          </div>
        </TableCell>
        <TableCell>
          <Badge tone={severityTone(v.severity)}>{v.severity}</Badge>
        </TableCell>
        <TableCell>
          <div className="flex flex-col gap-0.5">
            <code className="font-mono text-xs text-[var(--color-fg)]">
              {v.package_name || "—"}
            </code>
            {v.package_version ? (
              <span className="font-mono text-[11px] text-[var(--color-fg-subtle)]">
                {v.package_version}
              </span>
            ) : null}
          </div>
        </TableCell>
        <TableCell>
          {fix ? (
            <code className="font-mono text-xs text-[var(--color-success)]">
              {fix}
            </code>
          ) : (
            <span className="text-xs italic text-[var(--color-fg-subtle)]">
              none
            </span>
          )}
        </TableCell>
        <TableCell className="hidden md:table-cell">
          <span className="text-sm tabular-nums text-[var(--color-fg)]">
            {v.affected.length}
          </span>
          <span className="ml-1 text-xs text-[var(--color-fg-muted)]">
            {v.affected.length === 1 ? "image" : "images"}
          </span>
        </TableCell>
        <TableCell className="hidden text-xs text-[var(--color-fg-muted)] lg:table-cell">
          <span title={formatAbsoluteDate(v.last_seen)}>
            {formatRelativeDate(v.last_seen)}
          </span>
        </TableCell>
      </TableRow>

      {open ? (
        <TableRow className="!bg-[var(--color-surface-sunken)]">
          <TableCell />
          <TableCell colSpan={6} className="py-3">
            <AffectedList affected={v.affected} description={v.description} />
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

function AffectedList({
  affected,
  description,
}: {
  affected: VulnRowProps["v"]["affected"];
  description: string;
}): React.ReactElement {
  return (
    <div className="space-y-3">
      {description ? (
        <p className="text-xs leading-relaxed text-[var(--color-fg-muted)]">
          {description}
        </p>
      ) : null}
      <div className="grid gap-1.5 sm:grid-cols-2 lg:grid-cols-3">
        {affected.map((a) => {
          const [org, repo] = splitRepo(a.repo);
          return (
            <Link
              key={`${a.repo}@${a.digest}@${a.tag}`}
              to="/repositories/$org/$repo/tags/$tag"
              params={{ org, repo, tag: a.tag }}
              className="flex flex-col gap-0.5 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2.5 py-1.5 transition-colors hover:border-[var(--color-accent-border)]"
            >
              <code className="truncate font-mono text-xs font-medium text-[var(--color-fg)]">
                {a.repo}:{a.tag}
              </code>
              <code className="truncate font-mono text-[10px] text-[var(--color-fg-subtle)]">
                {a.digest}
              </code>
            </Link>
          );
        })}
      </div>
    </div>
  );
}

// splitRepo — backend returns "org/name". The tag-detail route expects
// `$org` and `$repo` as separate params; this splits on the first slash and
// treats the rest as the repo name (org names can't contain slashes per
// the regex in CLAUDE.md §7).
function splitRepo(full: string): [string, string] {
  const i = full.indexOf("/");
  if (i < 0) return ["", full];
  return [full.slice(0, i), full.slice(i + 1)];
}

function SkeletonRows(): React.ReactElement {
  return (
    <div className="space-y-2">
      {Array.from({ length: 6 }).map((_, i) => (
        <Skeleton key={i} className="h-12 w-full" />
      ))}
    </div>
  );
}
