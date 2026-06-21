import * as React from "react";
import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronRight, Wrench } from "lucide-react";
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
import { useRemediations, type Remediation } from "@/lib/api/remediation";
import { severityTone } from "@/lib/api/security";
import { cn } from "@/lib/utils";

// RemediationTable — FE-API-017.
//
// One row per upgrade path. The expand chevron reveals the CVE list and
// the affected (repo, tag, digest) triples, each of which deep-links to
// the tag detail page so the operator can act on the upgrade from here.
//
// Mirrors the visual rhythm of VulnerabilitiesTable so the two tabs in
// /security read as siblings.
export function RemediationTable(): React.ReactElement {
  const q = useRemediations();

  // Flatten the infinite-query pages into a single list — every row carries
  // a stable composite key so React reconciles cleanly across page loads.
  const flat = React.useMemo(
    () => q.data?.pages.flatMap((p) => p.remediations) ?? [],
    [q.data],
  );

  return (
    <div className="space-y-4">
      {q.isError ? (
        <ErrorState
          title="Couldn't load remediation suggestions"
          description="The metadata service didn't answer. Try again, or check the BFF logs."
          onRetry={() => void q.refetch()}
        />
      ) : q.isLoading ? (
        <SkeletonRows />
      ) : flat.length === 0 ? (
        <EmptyState
          icon={<Wrench className="size-5" />}
          title="Nothing to upgrade right now"
          description="Either there are no open CVEs with a known fix version, or all your tags are already on the latest. Run scans on any tag to refresh."
        />
      ) : (
        <>
          <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[40px]"></TableHead>
                  <TableHead>Package</TableHead>
                  <TableHead>Upgrade</TableHead>
                  <TableHead>Severity</TableHead>
                  <TableHead>CVEs closed</TableHead>
                  <TableHead className="hidden md:table-cell">Affected</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {flat.map((r) => (
                  <RemediationRow
                    key={`${r.package_name}@${r.from_version}->${r.to_version}`}
                    r={r}
                  />
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

function RemediationRow({ r }: { r: Remediation }): React.ReactElement {
  const [open, setOpen] = React.useState(false);
  const capped = r.affected_count > r.affected.length;

  return (
    <>
      <TableRow>
        <TableCell className="w-[40px] !pr-0">
          <button
            type="button"
            onClick={() => setOpen((p) => !p)}
            aria-label={open ? "Collapse details" : "Expand details"}
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
          <code className="font-mono text-sm font-medium text-[var(--color-fg)]">
            {r.package_name || "—"}
          </code>
        </TableCell>
        <TableCell>
          {/* The arrow communicates the change at a glance; both versions
              stay in mono so the eye lines them up across rows. */}
          <div className="flex items-center gap-1.5 font-mono text-xs">
            <code className="text-[var(--color-fg-muted)]">
              {r.from_version || "?"}
            </code>
            <span className="text-[var(--color-fg-subtle)]">→</span>
            <code className="text-[var(--color-success)]">
              {r.to_version || "?"}
            </code>
          </div>
        </TableCell>
        <TableCell>
          <Badge tone={severityTone(r.max_severity)}>{r.max_severity}</Badge>
        </TableCell>
        <TableCell>
          <span className="font-mono tabular-nums text-sm text-[var(--color-fg)]">
            {r.cves_fixed_count.toLocaleString()}
          </span>
        </TableCell>
        <TableCell className="hidden md:table-cell">
          <span className="text-sm tabular-nums text-[var(--color-fg)]">
            {r.affected_count.toLocaleString()}
          </span>
          <span className="ml-1 text-xs text-[var(--color-fg-muted)]">
            {r.affected_count === 1 ? "image" : "images"}
          </span>
        </TableCell>
      </TableRow>

      {open ? (
        <TableRow className="!bg-[var(--color-surface-sunken)]">
          <TableCell />
          <TableCell colSpan={5} className="py-3">
            <DetailsPanel r={r} capped={capped} />
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

function DetailsPanel({
  r,
  capped,
}: {
  r: Remediation;
  capped: boolean;
}): React.ReactElement {
  return (
    <div className="space-y-3">
      {r.cves_fixed.length > 0 ? (
        <div>
          <div className="mb-1.5 text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            CVEs closed by this upgrade
          </div>
          <div className="flex flex-wrap gap-1">
            {r.cves_fixed.map((id) => {
              // Deep-link to NVD when the ID looks like a CVE; otherwise
              // render as a plain chip so non-CVE advisory IDs (e.g.
              // GHSA-*) don't 404 on the upstream catalogue.
              const isCVE = id.startsWith("CVE-");
              const href = isCVE
                ? `https://nvd.nist.gov/vuln/detail/${encodeURIComponent(id)}`
                : null;
              return href ? (
                <a
                  key={id}
                  href={href}
                  target="_blank"
                  rel="noreferrer"
                  className="rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-accent)] hover:underline"
                >
                  {id}
                </a>
              ) : (
                <code
                  key={id}
                  className="rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-fg-muted)]"
                >
                  {id}
                </code>
              );
            })}
          </div>
        </div>
      ) : null}

      <div>
        <div
          className={cn(
            "mb-1.5 flex items-baseline justify-between gap-2",
            "text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]",
          )}
        >
          <span>Affected images</span>
          {capped ? (
            <span className="normal-case tracking-normal text-[var(--color-fg-muted)]">
              showing {r.affected.length} of {r.affected_count.toLocaleString()}
            </span>
          ) : null}
        </div>
        <div className="grid gap-1.5 sm:grid-cols-2 lg:grid-cols-3">
          {r.affected.map((a) => {
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
    </div>
  );
}

// splitRepo — backend returns "org/name". Mirrors the helper in
// vulnerabilities-table.tsx; we deliberately duplicate (rather than
// re-export) so the two tables can diverge without one breaking the other.
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
