import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { AxiosError } from "axios";
import { toast } from "sonner";
import { Boxes, Database, Layers, Repeat, Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  useCacheStats,
  useCachedManifests,
  useEvictCachedManifest,
  type CachedManifest,
} from "@/lib/api/proxy-cache";
import { formatBytes, formatRelativeDate } from "@/lib/format";

// /workspace/proxy-cache — FUT-013.
//
// Shows what's sitting in the pull-through cache: every manifest the
// proxy has fetched and persisted, plus per-row last-pulled-at and
// pull count. Stats card on top, filterable table below.
//
// Posture:
//   • Workspace-admin gated server-side; the sidebar entry is hidden
//     for non-admins (probe-and-hide via useCacheStats). Hitting the
//     URL directly as a non-admin shows the EmptyState below.
//   • Filters (upstream substring + image substring) are client-only
//     state — they translate into query params on the next list call
//     and the infinite query resets to page 1 because the query key
//     incorporates them.
//   • Evict is a `medium`-severity confirm (type the image:reference
//     to confirm) — same shape as repo deletion. A typo'd evict on a
//     proxy-cached row isn't catastrophic (next pull re-caches), but
//     it does cost an upstream round-trip and breaks layer reuse for
//     that ref, so the friction is calibrated proportionally.

export const Route = createFileRoute("/_authenticated/workspace/proxy-cache")({
  component: ProxyCachePage,
});

function ProxyCachePage(): React.ReactElement {
  const stats = useCacheStats();
  const [imageFilter, setImageFilter] = React.useState("");
  // Debounce the filter input so the user doesn't fire a list call on
  // every keystroke. 250ms is short enough to feel responsive.
  const debouncedImageFilter = useDebounced(imageFilter, 250);

  const listQuery = useCachedManifests({
    image_contains: debouncedImageFilter || undefined,
    page_size: 50,
  });

  const [evictTarget, setEvictTarget] = React.useState<CachedManifest | null>(null);
  const evict = useEvictCachedManifest();

  // Sidebar hides this page when the stats probe returns null, but a
  // deeplink can still land here. Render an EmptyState the operator
  // can act on (rather than a generic error).
  if (stats.isSuccess && stats.data === null) {
    return (
      <div className="space-y-6">
        <PageHeader />
        <EmptyState
          icon={<Repeat className="size-5" />}
          title="Pull-through cache isn't available"
          description="This deployment doesn't have the proxy backend wired (PROXY_GRPC_ADDR unset on the management service), or your role doesn't include workspace admin."
        />
      </div>
    );
  }

  if (stats.isError) {
    return (
      <div className="space-y-6">
        <PageHeader />
        <ErrorState
          title="Couldn't load cache stats"
          error={stats.error}
          onRetry={() => void stats.refetch()}
        />
      </div>
    );
  }

  const allManifests = listQuery.data?.pages.flatMap((p) => p.manifests) ?? [];
  const hasNextPage = !!listQuery.data?.pages.at(-1)?.next_page_token;

  return (
    <div className="space-y-6">
      <PageHeader />

      {/* Stats strip */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard
          icon={Layers}
          label="Cached manifests"
          value={
            stats.isLoading
              ? undefined
              : Intl.NumberFormat().format(stats.data?.total_manifests ?? 0)
          }
        />
        <StatCard
          icon={Database}
          label="Storage"
          value={
            stats.isLoading
              ? undefined
              : formatBytes(stats.data?.total_bytes ?? 0)
          }
        />
        <StatCard
          icon={Boxes}
          label="Upstreams"
          value={
            stats.isLoading
              ? undefined
              : Intl.NumberFormat().format(stats.data?.unique_upstreams ?? 0)
          }
        />
        <StatCard
          icon={Repeat}
          label="Total pulls served"
          value={
            stats.isLoading
              ? undefined
              : Intl.NumberFormat().format(stats.data?.total_pulls ?? 0)
          }
        />
      </div>

      {/* Filter + table */}
      <Card>
        <CardHeader className="flex flex-row items-center justify-between gap-3">
          <div>
            <h3 className="text-base font-semibold">Cached images</h3>
            <CardDescription className="mt-1">
              One row per cached <code className="rounded bg-[var(--color-surface-sunken)] px-1 py-0.5 text-[11px]">tenant_id + upstream + image + reference</code>.
              Pulling re-uses the cached manifest until it ages out; evicting
              a row forces the next pull to re-fetch from upstream.
            </CardDescription>
          </div>
          <div className="w-72 shrink-0">
            <Input
              placeholder="Filter by image…"
              value={imageFilter}
              onChange={(e) => setImageFilter(e.target.value)}
              aria-label="Filter cached images by name"
            />
          </div>
        </CardHeader>
        <CardContent className="px-0 pb-2">
          {listQuery.isLoading ? (
            <div className="space-y-2 px-6 py-4">
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-9 w-full" />
              ))}
            </div>
          ) : listQuery.isError ? (
            <div className="px-6 py-6">
              <ErrorState
                title="Couldn't load cached manifests"
                error={listQuery.error}
                onRetry={() => void listQuery.refetch()}
              />
            </div>
          ) : allManifests.length === 0 ? (
            <div className="px-6 py-10">
              <EmptyState
                icon={<Repeat className="size-5" />}
                title={
                  debouncedImageFilter
                    ? `No cached images match "${debouncedImageFilter}"`
                    : "Nothing in the cache yet"
                }
                description={
                  debouncedImageFilter
                    ? "Try a shorter prefix or clear the filter."
                    : "Pull an image through the proxy to populate this list."
                }
              />
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Upstream</TableHead>
                  <TableHead>Image</TableHead>
                  <TableHead>Reference</TableHead>
                  <TableHead className="text-right">Size</TableHead>
                  <TableHead>Cached</TableHead>
                  <TableHead>Last pulled</TableHead>
                  <TableHead className="text-right">Pulls</TableHead>
                  <TableHead aria-label="Actions" className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {allManifests.map((m) => (
                  <TableRow key={m.id}>
                    <TableCell>
                      <Badge tone="neutral">{m.upstream_name}</Badge>
                    </TableCell>
                    <TableCell className="font-medium">{m.image}</TableCell>
                    <TableCell>
                      <code className="text-xs">{m.reference}</code>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatBytes(m.size_bytes)}
                    </TableCell>
                    <TableCell className="text-[var(--color-fg-muted)]">
                      {formatRelativeDate(m.fetched_at)}
                    </TableCell>
                    <TableCell className="text-[var(--color-fg-muted)]">
                      {m.last_pulled_at
                        ? formatRelativeDate(m.last_pulled_at)
                        : "Never"}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {Intl.NumberFormat().format(m.pull_count)}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`Evict ${m.image}:${m.reference}`}
                        onClick={() => setEvictTarget(m)}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          {hasNextPage ? (
            <div className="flex justify-center py-3">
              <Button
                variant="outline"
                size="sm"
                disabled={listQuery.isFetchingNextPage}
                onClick={() => void listQuery.fetchNextPage()}
              >
                {listQuery.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            </div>
          ) : null}
        </CardContent>
      </Card>

      <ConfirmDestructiveDialog
        open={evictTarget !== null}
        onOpenChange={(open) => {
          if (!open) setEvictTarget(null);
        }}
        title="Evict cached image"
        description={
          evictTarget ? (
            <span>
              The next pull of{" "}
              <code className="rounded bg-[var(--color-surface-sunken)] px-1 py-0.5 text-[12px]">
                {evictTarget.image}:{evictTarget.reference}
              </code>{" "}
              will re-fetch the manifest from{" "}
              <strong>{evictTarget.upstream_name}</strong>. Layer blobs
              stay in storage and are reclaimed by GC if no other manifest
              references them.
            </span>
          ) : null
        }
        severity="medium"
        resourceName={evictTarget ? `${evictTarget.image}:${evictTarget.reference}` : ""}
        confirmLabel="Evict"
        loading={evict.isPending}
        onConfirm={async () => {
          if (!evictTarget) return;
          try {
            await evict.mutateAsync(evictTarget.id);
            toast.success(`Evicted ${evictTarget.image}:${evictTarget.reference}`);
            setEvictTarget(null);
          } catch (e) {
            toast.error(errorMessage(e));
          }
        }}
      />
    </div>
  );
}

function PageHeader(): React.ReactElement {
  return (
    <div className="flex items-start justify-between gap-3">
      <div>
        <h1 className="font-display text-2xl font-semibold tracking-tight">
          Pull-through cache
        </h1>
        <p className="mt-1 max-w-prose text-sm text-[var(--color-fg-muted)]">
          Visibility for images served via the pull-through proxy. The cache
          sits in front of upstreams configured under{" "}
          <code className="text-[12px]">UpstreamRegistries</code> and shortens
          repeated pulls to local-disk speed.
        </p>
      </div>
    </div>
  );
}

interface StatCardProps {
  icon: typeof Layers;
  label: string;
  // undefined while loading — renders a skeleton instead of a value.
  value: string | undefined;
}

function StatCard({ icon: Icon, label, value }: StatCardProps): React.ReactElement {
  return (
    <Card>
      <CardContent className="flex items-start gap-3 py-4">
        <span
          aria-hidden
          className="grid size-8 place-items-center rounded-md bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
        >
          <Icon className="size-4" />
        </span>
        <div className="min-w-0">
          <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            {label}
          </div>
          {value === undefined ? (
            <Skeleton className="mt-1 h-6 w-20" />
          ) : (
            <div className="font-display text-xl font-semibold tabular-nums">
              {value}
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

// Tiny debounce so the filter input doesn't fire a list refetch on
// every keystroke. Inline because no other route on this page would
// reuse it; promoting to a shared hook is premature.
function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = React.useState(value);
  React.useEffect(() => {
    const t = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

function errorMessage(err: unknown): string {
  if (err instanceof AxiosError) {
    const detail = (err.response?.data as { error?: string } | undefined)?.error;
    if (detail) return detail;
    return err.message;
  }
  if (err instanceof Error) return err.message;
  return "Unexpected error";
}
