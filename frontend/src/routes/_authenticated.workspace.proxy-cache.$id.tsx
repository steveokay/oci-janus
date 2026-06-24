import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { AxiosError } from "axios";
import { ArrowLeft, Box, Layers, Repeat } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import {
  useCachedManifest,
  type CachedManifestDetail,
} from "@/lib/api/proxy-cache";
import { formatBytes, formatRelativeDate } from "@/lib/format";

// /workspace/proxy-cache/$id — FUT-016.
//
// Click-through detail page for a single cached manifest row. Shows the
// upstream + image + reference header (with copy-able `docker pull`),
// a Layers tab (or Platforms tab for an image index), and a Manifest tab
// with the raw JSON body.
//
// Server-side parsing: the BFF already hands us `layers` / `manifests`
// as typed arrays + a `kind` discriminator. We never re-parse the body
// in TS. The "Manifest" tab decodes `body_base64` for the raw view only.
//
// Auth + availability:
//   • 404 (route disabled OR id not cached) → EmptyState with a back link
//   • 403 (non-workspace-admin)             → EmptyState with a back link
//   • everything else                       → ErrorState with retry
//
// We keep the BACK link visible in every state so a misclick from the
// list page is one tap away from undoing.

export const Route = createFileRoute(
  "/_authenticated/workspace/proxy-cache/$id",
)({
  component: ProxyCacheDetailPage,
});

// Exported so unit tests can render the component without bringing up
// a TanStack Router context. The Route is the runtime entry point.
export function ProxyCacheDetailPage(): React.ReactElement {
  const { id } = Route.useParams();
  const { data, isLoading, isError, error, refetch } = useCachedManifest(id);

  if (isLoading) {
    return (
      <div className="space-y-6">
        <BackBar />
        <HeaderSkeleton />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (isError) {
    const status =
      error instanceof AxiosError ? error.response?.status : undefined;
    // 404 + 403 are "expected" states (deleted out from under the user,
    // or they navigated here without workspace-admin). Show a friendly
    // empty rather than a noisy error card.
    if (status === 404) {
      return (
        <div className="space-y-6">
          <BackBar />
          <EmptyState
            icon={<Repeat className="size-5" />}
            title="Cached manifest not found"
            description="This row may have been evicted or the link is stale. Head back to the cache page."
          />
        </div>
      );
    }
    if (status === 403) {
      return (
        <div className="space-y-6">
          <BackBar />
          <EmptyState
            icon={<Repeat className="size-5" />}
            title="Workspace admin role required"
            description="Only workspace admins can view the pull-through cache. Ask an owner to grant you the role."
          />
        </div>
      );
    }
    return (
      <div className="space-y-6">
        <BackBar />
        <ErrorState
          title="Couldn't load cached manifest"
          error={error}
          onRetry={() => void refetch()}
        />
      </div>
    );
  }

  if (!data) {
    return (
      <div className="space-y-6">
        <BackBar />
        <EmptyState
          icon={<Repeat className="size-5" />}
          title="No data"
          description="The detail response came back empty. Try refreshing."
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <BackBar />
      <DetailHeader detail={data} />
      <DetailTabs detail={data} />
    </div>
  );
}

// BackBar is a single anchor back to the list page. Kept as its own
// component so error / empty / loading states all render the same nav
// affordance without duplicating the markup.
function BackBar(): React.ReactElement {
  return (
    <Link
      to="/workspace/proxy-cache"
      className="inline-flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
    >
      <ArrowLeft className="size-4" />
      Back to pull-through cache
    </Link>
  );
}

// DetailHeader is the chrome above the tabs — the docker-pull command
// + the row's metadata strip (size, cached, last-pulled, pulls).
//
// We render the pull command inline (rather than reusing
// PullCommandCard which is org/repo/tag-scoped) because proxy cache
// rows are `<upstream>/<image>:<reference>` not `<host>/<org>/<repo>`.
function DetailHeader({ detail }: { detail: CachedManifestDetail }): React.ReactElement {
  const pullCommand = `docker pull ${detail.image}:${detail.reference}`;
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="neutral">{detail.upstream_name}</Badge>
            <h1 className="font-display text-2xl font-semibold tracking-tight">
              <span className="text-[var(--color-fg)]">{detail.image}</span>
              <span className="text-[var(--color-fg-muted)]">:</span>
              <code className="font-mono text-xl">{detail.reference}</code>
            </h1>
            {detail.kind === "index" ? (
              <Badge tone="accent">
                <Layers className="size-3" /> Index
              </Badge>
            ) : null}
          </div>
          <p className="mt-1 max-w-prose text-sm text-[var(--color-fg-muted)]">
            Cached manifest served from{" "}
            <strong>{detail.upstream_name}</strong>. Layer blobs follow normal
            GC refcounting — evicting this row only removes the manifest.
          </p>
        </div>
      </div>

      {/* docker pull command — single inline row with copy button */}
      <Card>
        <CardContent className="flex items-center gap-3 py-3">
          <span
            aria-hidden
            className="font-mono text-sm text-[var(--color-fg-subtle)]"
          >
            $
          </span>
          <code className="flex-1 truncate font-mono text-sm">{pullCommand}</code>
          <CopyButton value={pullCommand} label="Copy" />
        </CardContent>
      </Card>

      {/* metadata strip — same shape as the list table columns so the
          operator's eye doesn't have to re-anchor between pages */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <MetaCell label="Digest">
          <div className="flex items-center gap-1.5">
            <code
              className="truncate font-mono text-[11px] text-[var(--color-fg-muted)]"
              title={detail.digest}
            >
              {detail.digest.slice(0, 24)}…
            </code>
            <CopyButton value={detail.digest} iconOnly />
          </div>
        </MetaCell>
        <MetaCell label="Size">
          <span className="font-mono text-sm tabular-nums">
            {formatBytes(detail.size_bytes)}
          </span>
        </MetaCell>
        <MetaCell label="Cached">
          <span className="text-sm text-[var(--color-fg)]">
            {formatRelativeDate(detail.fetched_at)}
          </span>
        </MetaCell>
        <MetaCell label="Last pulled">
          <span className="text-sm text-[var(--color-fg)]">
            {detail.last_pulled_at
              ? formatRelativeDate(detail.last_pulled_at)
              : "Never"}
            <span className="ml-2 text-[var(--color-fg-subtle)]">
              ({Intl.NumberFormat().format(detail.pull_count)} pulls)
            </span>
          </span>
        </MetaCell>
      </div>
    </div>
  );
}

function MetaCell({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <Card>
      <CardContent className="py-3">
        <div className="text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {label}
        </div>
        <div className="mt-1">{children}</div>
      </CardContent>
    </Card>
  );
}

// DetailTabs holds the Layers/Platforms + Manifest views. The first
// tab's label flips based on `kind` so an image-index lands on
// "Platforms" instead of an empty Layers table.
function DetailTabs({ detail }: { detail: CachedManifestDetail }): React.ReactElement {
  const firstTabLabel = detail.kind === "index" ? "Platforms" : "Layers";
  return (
    <Tabs defaultValue="layers">
      <TabsList>
        <TabsTrigger value="layers">{firstTabLabel}</TabsTrigger>
        <TabsTrigger value="manifest">Manifest</TabsTrigger>
      </TabsList>

      <TabsContent value="layers" className="mt-4">
        {detail.kind === "index" ? (
          <PlatformsTable detail={detail} />
        ) : (
          <LayersTable detail={detail} />
        )}
      </TabsContent>

      <TabsContent value="manifest" className="mt-4">
        <ManifestTab detail={detail} />
      </TabsContent>
    </Tabs>
  );
}

function LayersTable({ detail }: { detail: CachedManifestDetail }): React.ReactElement {
  if (detail.layers.length === 0) {
    return (
      <EmptyState
        icon={<Box className="size-5" />}
        title="No layers"
        description="This manifest has no layer entries — most often an artifact or attestation, not a regular image."
      />
    );
  }
  return (
    <Card>
      <CardHeader>
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          {detail.layers.length}{" "}
          {detail.layers.length === 1 ? "layer" : "layers"}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="overflow-hidden rounded-md border border-[var(--color-border)]">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-[55px]">#</TableHead>
                <TableHead>Digest</TableHead>
                <TableHead>Media type</TableHead>
                <TableHead className="text-right">Size</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {detail.layers.map((l, i) => (
                <TableRow key={`${l.digest}-${i}`}>
                  <TableCell className="font-mono text-xs text-[var(--color-fg-subtle)]">
                    {i + 1}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <code
                        className="truncate font-mono text-[11px] text-[var(--color-fg-muted)]"
                        title={l.digest}
                      >
                        {l.digest.slice(0, 24)}…
                      </code>
                      <CopyButton value={l.digest} iconOnly />
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge tone="neutral" className="font-mono text-[10px]">
                      {shortMediaType(l.media_type)}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right font-mono text-xs">
                    {formatBytes(l.size)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

function PlatformsTable({ detail }: { detail: CachedManifestDetail }): React.ReactElement {
  return (
    <Card accentBar="accent">
      <CardHeader>
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Multi-platform image — {detail.manifests.length}{" "}
          {detail.manifests.length === 1 ? "child manifest" : "child manifests"}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <p className="mb-3 text-sm text-[var(--color-fg-muted)]">
          Each row is a per-platform child manifest cached alongside the index.
          A future iteration will let you click through to each child's own
          detail page — for now, the digest copy button is enough to inspect
          via <code className="text-[12px]">crane</code> or <code className="text-[12px]">docker manifest inspect</code>.
        </p>
        <div className="overflow-hidden rounded-md border border-[var(--color-border)]">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Platform</TableHead>
                <TableHead>Digest</TableHead>
                <TableHead>Media type</TableHead>
                <TableHead className="text-right">Size</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {detail.manifests.map((m, i) => (
                <TableRow key={`${m.digest}-${i}`}>
                  <TableCell>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge tone="accent" className="font-mono">
                        {m.os}/{m.architecture}
                      </Badge>
                      {m.variant ? (
                        <Badge tone="neutral" className="font-mono">
                          {m.variant}
                        </Badge>
                      ) : null}
                      {m.os_version ? (
                        <span className="font-mono text-[11px] text-[var(--color-fg-subtle)]">
                          {m.os_version}
                        </span>
                      ) : null}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <code
                        className="truncate font-mono text-[11px] text-[var(--color-fg-muted)]"
                        title={m.digest}
                      >
                        {m.digest.slice(0, 24)}…
                      </code>
                      <CopyButton value={m.digest} iconOnly />
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge tone="neutral" className="font-mono text-[10px]">
                      {shortMediaType(m.media_type)}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right font-mono text-xs">
                    {formatBytes(m.size)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

// ManifestTab renders the raw manifest body as pretty-printed JSON.
//
// `body_base64` is base64-decoded inline (no CodeBlock component
// exists yet — see the FUT-016 plan: drop in <CodeBlock> once we
// promote one). We attempt JSON.parse + stringify(_, 2) for pretty
// printing; if the body isn't JSON (vanishingly rare for a manifest
// row) we fall back to the raw decoded string.
function ManifestTab({ detail }: { detail: CachedManifestDetail }): React.ReactElement {
  const pretty = React.useMemo(() => {
    try {
      // atob is global in browsers; tests run under jsdom which provides it.
      const raw = atob(detail.body_base64);
      try {
        return JSON.stringify(JSON.parse(raw), null, 2);
      } catch {
        return raw;
      }
    } catch {
      return "(unable to decode manifest body)";
    }
  }, [detail.body_base64]);

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-2">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Raw manifest body
        </CardDescription>
        <CopyButton value={pretty} label="Copy JSON" />
      </CardHeader>
      <CardContent>
        <pre
          className="max-h-[60vh] overflow-auto rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3 font-mono text-xs"
          data-testid="proxy-cache-manifest-raw"
        >
          {pretty}
        </pre>
      </CardContent>
    </Card>
  );
}

function HeaderSkeleton(): React.ReactElement {
  return (
    <div className="space-y-3">
      <Skeleton className="h-8 w-2/3" />
      <Skeleton className="h-12 w-full" />
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
    </div>
  );
}

// shortMediaType mirrors the LayersPanel helper — trim the OCI / Docker
// prefix so badges stay readable. Duplicated rather than imported because
// LayersPanel is a tag-scoped component; promoting this to shared
// formatters is a follow-up if a third caller appears.
function shortMediaType(mt: string): string {
  return mt
    .replace(/^application\/vnd\.(oci|docker)\.(?:distribution\.)?/, "")
    .replace(/^application\//, "");
}
