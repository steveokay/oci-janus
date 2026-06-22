import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  ChevronRight,
  Tag as TagIcon,
  ShieldCheck,
  Trash2,
  RefreshCw,
  Pin,
  PinOff,
} from "lucide-react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { CopyButton } from "@/components/ui/copy-button";
import { formatAbsoluteDate, formatBytes } from "@/lib/format";
import { usePinTag, useUnpinTag } from "@/lib/api/tags";
import type { Tag } from "@/lib/api/types";

interface TagHeaderProps {
  org: string;
  repo: string;
  tagName: string;
  tag?: Tag;
  loading?: boolean;
  scanRunning?: boolean;
  onRescan: () => void;
  onDelete: () => void;
}

// Beacon — TagHeader. Mirrors the RepositoryHeader pattern: breadcrumb,
// identity card, action ribbon. The digest is monospace + copy-able so
// the operator can paste it into a `cosign verify` / `docker pull <digest>`
// without round-tripping through clipboard surgery.
export function TagHeader({
  org,
  repo,
  tagName,
  tag,
  loading,
  scanRunning,
  onRescan,
  onDelete,
}: TagHeaderProps): React.ReactElement {
  // Futures.md Tier 1 #2 — per-tag pin. The Pin/Unpin button on the
  // action ribbon flips `tags.immutable`; the BFF gates it on repo
  // admin role. Failure surfaces via toast — we don't pre-validate
  // the role on the FE.
  const pin = usePinTag();
  const unpin = useUnpinTag();
  const pinning = pin.isPending || unpin.isPending;
  const pinned = Boolean(tag?.immutable);

  async function togglePin(): Promise<void> {
    try {
      if (pinned) {
        await unpin.mutateAsync({ org, repo, tag: tagName });
        toast.success(`Unpinned ${tagName} — re-pushes are allowed again.`);
      } else {
        await pin.mutateAsync({ org, repo, tag: tagName });
        toast.success(`Pinned ${tagName} — pushes that would move it will be rejected.`);
      }
    } catch (e) {
      const code = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        code === 403
          ? "Repository admin role required."
          : "Couldn't update the pin. Check the BFF logs.",
      );
    }
  }

  return (
    <div className="space-y-5">
      {/* Breadcrumb */}
      <nav
        aria-label="Breadcrumb"
        className="flex items-center gap-1 text-xs text-[var(--color-fg-muted)]"
      >
        <Link to="/repositories" className="hover:text-[var(--color-fg)]">
          Repositories
        </Link>
        <ChevronRight className="size-3 text-[var(--color-fg-subtle)]" />
        <Link
          to="/repositories/$org/$repo"
          params={{ org, repo }}
          className="font-mono hover:text-[var(--color-fg)]"
        >
          {org ? `${org}/` : ""}{repo}
        </Link>
        <ChevronRight className="size-3 text-[var(--color-fg-subtle)]" />
        <span className="font-mono text-[var(--color-fg)]">{tagName}</span>
      </nav>

      <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
        <div className="flex items-start gap-4">
          <span
            className="grid size-12 shrink-0 place-items-center rounded-lg bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
            aria-hidden
          >
            <TagIcon className="size-5" />
          </span>
          <div className="min-w-0 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="font-display text-2xl font-medium tracking-tight">
                {tagName}
              </h1>
              <Badge tone="accent">
                <TagIcon className="size-3" /> tag
              </Badge>
            </div>
            {loading ? (
              <Skeleton className="h-3 w-72" />
            ) : (
              <div className="flex items-center gap-1.5">
                <code
                  className="truncate font-mono text-xs text-[var(--color-fg-muted)]"
                  title={tag?.manifest_digest}
                >
                  {tag?.manifest_digest.slice(0, 26)}…
                </code>
                {tag ? <CopyButton value={tag.manifest_digest} iconOnly /> : null}
              </div>
            )}
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-[var(--color-fg-muted)]">
              {loading ? (
                <Skeleton className="h-3 w-44" />
              ) : (
                <>
                  <span>
                    Size{" "}
                    <span className="font-mono text-[var(--color-fg)]">
                      {tag && tag.size_bytes > 0
                        ? formatBytes(tag.size_bytes)
                        : "—"}
                    </span>
                  </span>
                  <span>
                    Updated{" "}
                    <span className="text-[var(--color-fg)]">
                      {formatAbsoluteDate(tag?.updated_at)}
                    </span>
                  </span>
                </>
              )}
            </div>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            onClick={onRescan}
            loading={scanRunning}
            disabled={!tag || scanRunning}
          >
            {scanRunning ? (
              <RefreshCw className="size-4 animate-spin" />
            ) : (
              <ShieldCheck className="size-4" />
            )}
            {scanRunning ? "Scan in flight" : "Rescan"}
          </Button>
          {/* Futures.md Tier 1 #2 — Pin / Unpin. Hidden until the tag */}
          {/* loads so the button doesn't briefly render in the wrong  */}
          {/* state on first paint.                                    */}
          {tag ? (
            <Button
              variant="outline"
              onClick={() => void togglePin()}
              loading={pinning}
              disabled={pinning}
              title={
                pinned
                  ? "Remove the per-tag pin. Repo-wide immutability (if set) still applies."
                  : "Pin this tag — pushes that would move it will be rejected."
              }
            >
              {pinned ? (
                <PinOff className="size-4" />
              ) : (
                <Pin className="size-4" />
              )}
              {pinned ? "Unpin tag" : "Pin tag"}
            </Button>
          ) : null}
          <Button
            variant="ghost"
            onClick={onDelete}
            className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
            disabled={!tag}
          >
            <Trash2 className="size-4" />
            Delete tag
          </Button>
        </div>
      </div>
    </div>
  );
}
