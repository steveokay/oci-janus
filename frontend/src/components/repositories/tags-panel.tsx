import * as React from "react";
import { Tag as TagIcon } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import { ComingSoonHint } from "@/components/common/coming-soon-hint";
import { formatBytes, formatRelativeDate } from "@/lib/format";
import { useTags } from "@/lib/api/tags";

interface TagsPanelProps {
  org: string;
  repo: string;
}

// Beacon — TagsPanel. Lives inside the Tabs on the repository detail page.
// Light wrapper around the tags table — keeps the state machine local so
// switching tabs doesn't re-mount the rest of the page.
export function TagsPanel({ org, repo }: TagsPanelProps): React.ReactElement {
  const navigate = useNavigate();
  const { data, isLoading, isError, refetch } = useTags(org, repo);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load tags"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  if (!isLoading && (data?.length ?? 0) === 0) {
    return (
      <EmptyState
        icon={<TagIcon className="size-5" />}
        title="No tags yet"
        description="Push your first image with the pull command above (swap `pull` for `push`). The tag will appear here within a few seconds."
      />
    );
  }

  return (
    <div className="space-y-3">
      <ComingSoonHint apiId="FE-API-036">
        Row-select checkboxes + "Delete selected" toolbar land here. Today the
        only delete affordance is per-tag on the detail page.
      </ComingSoonHint>
      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
        <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[30%]">Tag</TableHead>
            <TableHead>Digest</TableHead>
            <TableHead>Size</TableHead>
            <TableHead className="hidden md:table-cell">Updated</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {isLoading ? (
            <SkeletonRows />
          ) : (
            data?.map((t) => {
              // Programmatic navigation on the row's onClick + a parallel
              // mousedown trigger (some browsers stall the onClick on
              // <tr> with display:table-row; mousedown always fires). Each
              // cell's content gets pointer-events:none so clicks fall
              // through to the row; the CopyButton column re-enables
              // pointer-events with stopPropagation so it acts on its own.
              const target = {
                to: "/repositories/$org/$repo/tags/$tag" as const,
                params: { org, repo, tag: t.name },
              };
              const open = () => {
                // Diagnostic — leave in for now so the user can prove from
                // the console whether the click event reaches this handler.
                // Remove once the route navigation is confirmed working in
                // the user's browser.
                console.log("[beacon] tag-row navigate", target);
                void navigate(target);
              };
              // Concrete URL for the row — used as a real <a href> so even
              // if React's synthetic event system doesn't fire on <tr>,
              // browser native navigation still resolves the click.
              const hrefTo = `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(repo)}/tags/${encodeURIComponent(t.name)}`;
              return (
                <TableRow
                  key={`${t.name}-${t.manifest_digest}`}
                  interactive
                  role="link"
                  tabIndex={0}
                  onClick={open}
                  onMouseDown={(e) => {
                    if (e.button === 0) open();
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      open();
                    }
                  }}
                >
                  <TableCell className="p-0">
                    {/* Native anchor — fills the cell + carries href so
                        browser navigation resolves even if React's synthetic
                        click misses. TanStack Router's history listener
                        upgrades the navigation to SPA in-app. */}
                    <a
                      href={hrefTo}
                      onClick={(e) => {
                        e.preventDefault();
                        open();
                      }}
                      className="block px-4 py-3 text-inherit no-underline"
                    >
                      <Badge tone="accent">
                        <TagIcon className="size-3" /> {t.name}
                      </Badge>
                    </a>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <code
                        className="pointer-events-none truncate font-mono text-xs text-[var(--color-fg-muted)]"
                        title={t.manifest_digest}
                      >
                        {t.manifest_digest.slice(0, 19)}…
                      </code>
                      <span
                        onClick={(e) => e.stopPropagation()}
                        onMouseDown={(e) => e.stopPropagation()}
                      >
                        <CopyButton value={t.manifest_digest} iconOnly />
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className="pointer-events-none font-mono text-xs">
                    {t.size_bytes > 0 ? (
                      formatBytes(t.size_bytes)
                    ) : (
                      <span className="text-[var(--color-fg-subtle)]">—</span>
                    )}
                  </TableCell>
                  <TableCell className="pointer-events-none hidden text-xs text-[var(--color-fg-muted)] md:table-cell">
                    {formatRelativeDate(t.updated_at)}
                  </TableCell>
                </TableRow>
              );
            })
          )}
        </TableBody>
      </Table>
      </div>
    </div>
  );
}

function SkeletonRows(): React.ReactElement {
  return (
    <>
      {Array.from({ length: 4 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell>
            <Skeleton className="h-5 w-20 rounded-full" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-3 w-44" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-3 w-16" />
          </TableCell>
          <TableCell className="hidden md:table-cell">
            <Skeleton className="h-3 w-20" />
          </TableCell>
        </TableRow>
      ))}
    </>
  );
}
