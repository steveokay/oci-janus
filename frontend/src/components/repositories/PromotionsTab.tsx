import * as React from "react";
import { Ship, ArrowRight } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { formatRelativeDate, formatAbsoluteDate } from "@/lib/format";
import { usePromotionHistory, type Promotion } from "@/lib/api/promotions";

interface PromotionsTabProps {
  org: string;
  repo: string;
}

// PromotionsTab renders the recent promotions touching this repo (src OR
// dst side). Sits under the repo detail Tabs alongside Tags / Members /
// Retention / Settings. The BFF caps limit at 50 so there's no need for
// client-side pagination in v1 — a "show more" affordance can layer on top
// of usePromotionHistory later without changing the row shape.
export function PromotionsTab({
  org,
  repo,
}: PromotionsTabProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = usePromotionHistory(org, repo);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load promotions"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  if (isLoading) {
    return <PromotionsSkeleton />;
  }

  if (!data || data.length === 0) {
    return (
      <EmptyState
        icon={<Ship className="size-6" aria-hidden />}
        title="No promotions yet"
        description={`No promotions touching ${org}/${repo} have been recorded. Trigger one from the Promote button on any tag detail page.`}
      />
    );
  }

  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>When</TableHead>
            <TableHead>Source</TableHead>
            <TableHead aria-hidden />
            <TableHead>Destination</TableHead>
            <TableHead>Actor</TableHead>
            <TableHead>Note</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {data.map((p) => (
            <PromotionRow key={p.id} p={p} />
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

// PromotionRow renders one row of the history table. Split out so the
// styling stays readable and future affordances (link the tag, copy the
// digest) can land in one place.
function PromotionRow({ p }: { p: Promotion }): React.ReactElement {
  return (
    <TableRow>
      {/* Absolute timestamp on hover — matches the app-wide relative-date +
          title-tooltip convention. */}
      <TableCell
        className="whitespace-nowrap text-xs text-[var(--color-fg-muted)]"
        title={formatAbsoluteDate(p.promoted_at)}
      >
        {formatRelativeDate(p.promoted_at)}
      </TableCell>
      <TableCell className="font-mono text-xs">
        {p.src_org}/{p.src_repo}:{p.src_tag}
      </TableCell>
      <TableCell className="pointer-events-none px-1 text-[var(--color-fg-subtle)]">
        <ArrowRight className="size-3.5" aria-hidden />
      </TableCell>
      <TableCell className="font-mono text-xs">
        {p.dst_org}/{p.dst_repo}:{p.dst_tag}
      </TableCell>
      <TableCell className="whitespace-nowrap text-xs text-[var(--color-fg-muted)]">
        {p.actor_user_id ? (
          // Truncate the UUID for the compact table cell; the tooltip
          // still shows the full id for auditors that need it.
          <span title={p.actor_user_id}>
            {p.actor_user_id.slice(0, 8)}…
          </span>
        ) : (
          <span className="italic text-[var(--color-fg-subtle)]">
            automated
          </span>
        )}
      </TableCell>
      <TableCell className="text-xs">
        {p.note ? (
          p.note
        ) : (
          <span className="text-[var(--color-fg-subtle)]">—</span>
        )}
      </TableCell>
    </TableRow>
  );
}

// PromotionsSkeleton — 3 placeholder rows while the initial fetch is in
// flight. Cheap and unbranded, matches the size/loading state on tags-panel.
function PromotionsSkeleton(): React.ReactElement {
  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>When</TableHead>
            <TableHead>Source</TableHead>
            <TableHead aria-hidden />
            <TableHead>Destination</TableHead>
            <TableHead>Actor</TableHead>
            <TableHead>Note</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {Array.from({ length: 3 }).map((_, i) => (
            <TableRow key={i}>
              <TableCell>
                <Skeleton className="h-3 w-16" />
              </TableCell>
              <TableCell>
                <Skeleton className="h-3 w-32" />
              </TableCell>
              <TableCell aria-hidden />
              <TableCell>
                <Skeleton className="h-3 w-32" />
              </TableCell>
              <TableCell>
                <Skeleton className="h-3 w-12" />
              </TableCell>
              <TableCell>
                <Skeleton className="h-3 w-24" />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
