import * as React from "react";
import { Tag as TagIcon, ArrowRight } from "lucide-react";
import { Link } from "@tanstack/react-router";
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
            data?.map((t) => (
              <TableRow key={`${t.name}-${t.manifest_digest}`}>
                <TableCell>
                  <div className="flex items-center gap-3">
                    <Badge tone="accent">
                      <TagIcon className="size-3" /> {t.name}
                    </Badge>
                    <Link
                      to="/repositories/$org/$repo"
                      params={{ org, repo }}
                      className="invisible text-xs text-[var(--color-accent)] hover:underline group-hover:visible"
                      aria-label={`Open tag ${t.name}`}
                    >
                      Open <ArrowRight className="inline size-3" />
                    </Link>
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-2">
                    <code
                      className="truncate font-mono text-xs text-[var(--color-fg-muted)]"
                      title={t.manifest_digest}
                    >
                      {t.manifest_digest.slice(0, 19)}…
                    </code>
                    <CopyButton value={t.manifest_digest} iconOnly />
                  </div>
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {t.size_bytes > 0 ? formatBytes(t.size_bytes) : (
                    <span className="text-[var(--color-fg-subtle)]">—</span>
                  )}
                </TableCell>
                <TableCell className="hidden text-xs text-[var(--color-fg-muted)] md:table-cell">
                  {formatRelativeDate(t.updated_at)}
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
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
