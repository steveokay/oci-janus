import * as React from "react";
import { Link } from "@tanstack/react-router";
import { Boxes, ArrowRight } from "lucide-react";
import { formatBytes, formatRelativeDate, formatAbsoluteDate } from "@/lib/format";
import type { OrgSummary } from "@/lib/api/orgs";

// OrgCard — one environment on the /repositories overview. The whole card
// is a link into that environment's repository list. Shows the three v1
// metrics: repo count, total storage, and last push (or "No activity yet"
// when the org has no manifests).
export function OrgCard({ org }: { org: OrgSummary }): React.ReactElement {
  return (
    <Link
      to="/repositories/$org"
      params={{ org: org.org }}
      className="group flex flex-col gap-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)] transition-colors hover:border-[var(--color-border-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/40"
    >
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span
            className="grid size-9 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
            aria-hidden
          >
            <Boxes className="size-5" />
          </span>
          <span className="font-display text-lg font-medium tracking-tight">
            {org.org}
          </span>
        </div>
        <ArrowRight
          className="size-4 text-[var(--color-fg-subtle)] transition-transform group-hover:translate-x-0.5"
          aria-hidden
        />
      </div>

      <dl className="grid grid-cols-3 gap-3 text-sm">
        <div className="space-y-0.5">
          <dt className="text-xs text-[var(--color-fg-subtle)]">Repositories</dt>
          <dd className="font-mono">{org.repo_count}</dd>
        </div>
        <div className="space-y-0.5">
          <dt className="text-xs text-[var(--color-fg-subtle)]">Storage</dt>
          <dd className="font-mono">{formatBytes(org.storage_used_bytes)}</dd>
        </div>
        <div className="space-y-0.5">
          <dt className="text-xs text-[var(--color-fg-subtle)]">Last push</dt>
          <dd
            className="text-[var(--color-fg-muted)]"
            title={
              org.last_activity_at
                ? formatAbsoluteDate(org.last_activity_at)
                : undefined
            }
          >
            {org.last_activity_at
              ? formatRelativeDate(org.last_activity_at)
              : "No activity yet"}
          </dd>
        </div>
      </dl>

      {/* Quiet type split: "N images · M charts". Rendered only when the org
          actually holds images and/or Helm charts. Swatch colors mirror the
          artifact Type badges — accent (teal) for images, warning for charts. */}
      {(org.image_repo_count ?? 0) > 0 || (org.helm_repo_count ?? 0) > 0 ? (
        <div className="flex items-center gap-4 border-t border-[var(--color-border)] pt-3 text-xs text-[var(--color-fg-muted)]">
          {(org.image_repo_count ?? 0) > 0 ? (
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-[3px] bg-[var(--color-accent)]" aria-hidden />
              <span className="font-mono text-[var(--color-fg)]">
                {org.image_repo_count} {org.image_repo_count === 1 ? "image" : "images"}
              </span>
            </span>
          ) : null}
          {(org.helm_repo_count ?? 0) > 0 ? (
            <span className="inline-flex items-center gap-1.5">
              <span className="size-2 rounded-[3px] bg-[var(--color-warning)]" aria-hidden />
              <span className="font-mono text-[var(--color-fg)]">
                {org.helm_repo_count} {org.helm_repo_count === 1 ? "chart" : "charts"}
              </span>
            </span>
          ) : null}
        </div>
      ) : null}
    </Link>
  );
}
