import * as React from "react";
import { Link } from "@tanstack/react-router";
import { HardDrive } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { useStorageBreakdown } from "@/lib/api/stats-storage";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/utils";

interface StorageBreakdownCardProps {
  limit?: number;
}

// FE-API-031 — top-N repos by storage, with each row's bar widthed by
// percent_of_tenant. Lives on the dashboard; default cap is the top 6
// (S-MAINT-1 P1: was top 8) so the card stays readable without taking
// over the page. Each row links to /repositories/$org/$repo so an admin
// can act on the heaviest offenders in two clicks.
export function StorageBreakdownCard({
  limit = 6,
}: StorageBreakdownCardProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useStorageBreakdown();
  const repos = (data?.repositories ?? []).slice(0, limit);
  const tenantUsed = data?.tenant_storage_used_bytes ?? 0;
  // S-MAINT-1 P1: render "used / total" when quota is set so admins can
  // see capacity headroom at a glance. Falls back to just-usage when the
  // quota field is missing or zero (lazily-created tenants).
  const tenantQuota = data?.tenant_storage_quota_bytes ?? 0;
  // REM-013 gap 3: lifetime bytes reclaimed via retention across the tenant.
  // 0 (or missing, when GC isn't wired) renders as "—".
  const tenantReclaimed = data?.retention_reclaimed_bytes ?? 0;
  const extraCount = Math.max(0, (data?.repositories.length ?? 0) - repos.length);

  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Storage breakdown
          </CardDescription>
          {!isLoading && data ? (
            <span className="text-xs text-[var(--color-fg-muted)]">
              {tenantQuota > 0 ? (
                // Unified "used / total" form (matches repositories-table +
                // repository-header); dropped the trailing "total" word.
                <>
                  {formatBytes(tenantUsed)}{" "}
                  <span className="text-[var(--color-fg-subtle)]">
                    / {formatBytes(tenantQuota)}
                  </span>
                </>
              ) : (
                // No quota set — no total to divide against, so show the
                // absolute used figure with a "total" qualifier.
                <>{formatBytes(tenantUsed)} total</>
              )}
            </span>
          ) : null}
        </div>
        {/* REM-013 gap 3 — tenant-wide "reclaimed via retention" stat. Sits */}
        {/* under the used/total line so an operator can see, at the card    */}
        {/* level, how much storage retention has actually saved. Renders    */}
        {/* "—" when 0 (no retention run yet, or GC service not wired).      */}
        {!isLoading && data ? (
          <div className="mt-1 flex items-center justify-between text-[11px] text-[var(--color-fg-subtle)]">
            <span>Reclaimed via retention</span>
            <span className="tabular-nums text-[var(--color-fg-muted)]">
              {tenantReclaimed > 0 ? formatBytes(tenantReclaimed) : "—"}
            </span>
          </div>
        ) : null}
      </CardHeader>
      <CardContent className="pt-0">
        {isError ? (
          <ErrorState
            title="Couldn't load storage breakdown"
            description="The metadata service didn't answer. Retry, or check the BFF logs."
            onRetry={() => void refetch()}
          />
        ) : isLoading ? (
          <SkeletonRows />
        ) : repos.length === 0 ? (
          <EmptyState
            icon={<HardDrive className="size-5" />}
            title="No storage in use yet"
            description="Push your first image and storage usage will surface here."
          />
        ) : (
          <>
            <ul className="space-y-2.5">
              {repos.map((r) => (
                <BreakdownRow
                  key={r.repo_id}
                  org={r.org}
                  name={r.name}
                  bytes={r.storage_used_bytes}
                  percent={r.percent_of_tenant}
                  retentionSummary={r.retention_summary}
                  retentionSource={r.retention_source}
                />
              ))}
            </ul>
            {extraCount > 0 ? (
              <p className="mt-3 text-[11px] text-[var(--color-fg-subtle)]">
                + {extraCount} more in the top 50 — see Repositories for the
                full list.
              </p>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  );
}

function BreakdownRow({
  org,
  name,
  bytes,
  percent,
  retentionSummary,
  retentionSource,
}: {
  org: string;
  name: string;
  bytes: number;
  percent: number;
  retentionSummary?: string;
  retentionSource?: "repo" | "org" | "";
}): React.ReactElement {
  // Pin the bar to a 1% minimum so a repo with a real (non-zero) presence
  // doesn't render as an invisible sliver — the tenant_total can dwarf any
  // individual repo on a young workspace.
  const barWidthPct = bytes > 0 ? Math.max(percent, 1) : 0;
  const fullPath = org ? `${org}/${name}` : name;
  return (
    <li>
      <Link
        to="/repositories/$org/$repo"
        params={{ org, repo: name }}
        className="group block rounded-md px-1 py-1 transition-colors hover:bg-[var(--color-surface-sunken)]"
      >
        <div className="flex items-baseline justify-between gap-2">
          <code className="truncate font-mono text-xs font-medium text-[var(--color-fg)] group-hover:underline">
            {fullPath}
          </code>
          <span className="shrink-0 text-[11px] tabular-nums text-[var(--color-fg-muted)]">
            {formatBytes(bytes)}{" "}
            <span className="text-[var(--color-fg-subtle)]">
              · {percent.toFixed(percent < 1 ? 2 : 1)}%
            </span>
          </span>
        </div>
        <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-surface-sunken)]">
          <div
            className={cn(
              "h-full rounded-full bg-[var(--color-accent)] transition-all",
            )}
            style={{ width: `${barWidthPct}%` }}
            aria-hidden
          />
        </div>
        {/* REM-013 gap 3 — retention column sits below the storage bar */}
        {/* so a glance at the row tells the operator both "how much" and */}
        {/* "what policy". Empty when no policy applies — render "—"     */}
        {/* rather than dropping the line so the layout doesn't shift.   */}
        <div className="mt-1 flex items-center justify-between gap-2 text-[10px] text-[var(--color-fg-subtle)]">
          <span className="uppercase tracking-[0.16em]">Retention</span>
          {retentionSummary ? (
            <span className="font-mono text-[11px] text-[var(--color-fg-muted)]">
              {retentionSummary}
              {retentionSource === "org" ? (
                <span className="ml-1 text-[var(--color-fg-subtle)]">
                  (inherited)
                </span>
              ) : null}
            </span>
          ) : (
            <span>—</span>
          )}
        </div>
      </Link>
    </li>
  );
}

function SkeletonRows(): React.ReactElement {
  return (
    <ul className="space-y-2.5">
      {Array.from({ length: 5 }).map((_, i) => (
        <li key={i} className="space-y-1.5">
          <div className="flex items-center justify-between">
            <Skeleton className="h-3 w-2/5" />
            <Skeleton className="h-3 w-16" />
          </div>
          <Skeleton className="h-1.5 w-full rounded-full" />
        </li>
      ))}
    </ul>
  );
}
