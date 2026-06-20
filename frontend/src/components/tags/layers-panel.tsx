import * as React from "react";
import { Layers, Box, Download } from "lucide-react";
import { toast } from "sonner";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import {
  useDownloadSbom,
  SBOM_FORMATS,
  type SbomFormat,
} from "@/lib/api/sbom";
import { useManifest, type ManifestDetail } from "@/lib/api/manifest";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/utils";

interface LayersPanelProps {
  org: string;
  repo: string;
  tag: string;
}

// Beacon — LayersPanel (FE-API-002).
//
// Renders one of two surfaces depending on the manifest type:
//  - **Image manifest** → config descriptor card + layers table
//  - **Image index**    → per-platform manifests table with arch/os/variant
//
// Both share the same Card chrome so switching tabs doesn't visually jump.
export function LayersPanel({
  org,
  repo,
  tag,
}: LayersPanelProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useManifest(org, repo, tag);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load manifest"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  if (isLoading) {
    return <LoadingSkeleton />;
  }

  if (!data) {
    return (
      <EmptyState
        icon={<Layers className="size-5" />}
        title="No manifest available"
        description="The tag exists but the manifest blob hasn't been recorded by metadata yet. Try again in a moment."
      />
    );
  }

  return (
    <div className="space-y-4">
      {data.is_index ? <IndexView manifest={data} /> : <ImageView manifest={data} />}
      <SbomPanel org={org} repo={repo} tag={tag} />
    </div>
  );
}

interface SbomPanelProps {
  org: string;
  repo: string;
  tag: string;
}

// SbomPanel — FE-API-033 live wiring.
//
// Picks a default format (SPDX, the only one the scanner emits today). The
// CycloneDX chip is rendered as a disabled "coming soon" hint rather than
// hiding it — operators planning their SBOM tooling can see which format
// they're picking AND which one is on the roadmap.
function SbomPanel({ org, repo, tag }: SbomPanelProps): React.ReactElement {
  const [format, setFormat] = React.useState<SbomFormat>("spdx-json");
  const download = useDownloadSbom();

  async function handleDownload() {
    try {
      await download.mutateAsync({ org, repo, tag, format });
      toast.success("SBOM download started.");
    } catch (e) {
      const err = e as { status?: string; message?: string };
      if (err.status === "no-sbom") {
        toast.error(err.message ?? "No SBOM recorded.");
      } else if (err.status === "format-unsupported") {
        toast.error(err.message ?? "Format not supported yet.");
      } else {
        toast.error(err.message ?? "SBOM download failed.");
      }
    }
  }

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Software bill of materials
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => void handleDownload()}
            loading={download.isPending}
            disabled={download.isPending}
          >
            <Download className="size-4" />
            Download SBOM
          </Button>
          <span className="text-xs text-[var(--color-fg-subtle)]">Format:</span>
          {SBOM_FORMATS.map((f) => (
            <button
              key={f.key}
              type="button"
              onClick={() => f.available && setFormat(f.key)}
              disabled={!f.available}
              aria-pressed={format === f.key}
              className={cn(
                "rounded-full border px-2 py-0.5 font-mono text-[11px]",
                f.available
                  ? format === f.key
                    ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
                    : "border-[var(--color-border-strong)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
                  : "cursor-not-allowed border-[var(--color-border)] text-[var(--color-fg-subtle)]",
              )}
              title={f.available ? "" : "Coming soon"}
            >
              {f.label}
            </button>
          ))}
        </div>
        <p className="text-xs text-[var(--color-fg-muted)]">
          Surfaces the SBOM persisted alongside the latest scan result for this
          digest. If you see "no SBOM recorded", trigger a scan on the Security
          tab and try again.
        </p>
      </CardContent>
    </Card>
  );
}

function ImageView({ manifest }: { manifest: ManifestDetail }): React.ReactElement {
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Image config
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          <DigestRow
            label="Config digest"
            value={manifest.config.digest}
            mediaType={manifest.config.media_type}
            size={manifest.config.size}
          />
          <DigestRow
            label="Manifest digest"
            value={manifest.digest}
            mediaType={manifest.media_type}
            size={manifest.size_bytes}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Layers
            </CardDescription>
            <span className="text-xs text-[var(--color-fg-muted)]">
              {manifest.layers.length}{" "}
              {manifest.layers.length === 1 ? "layer" : "layers"}
            </span>
          </div>
        </CardHeader>
        <CardContent>
          {manifest.layers.length === 0 ? (
            <EmptyState
              icon={<Box className="size-5" />}
              title="No layers"
              description="This manifest has no layer entries — most often an artifact or attestation, not an image."
            />
          ) : (
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
                  {manifest.layers.map((l, i) => (
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
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function IndexView({ manifest }: { manifest: ManifestDetail }): React.ReactElement {
  return (
    <div className="space-y-4">
      <Card accentBar="accent">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Multi-platform image
            </CardDescription>
            <Badge tone="accent">
              <Layers className="size-3" /> Index
            </Badge>
          </div>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-[var(--color-fg-muted)]">
            This tag points at an image index (manifest list) — each row below
            is a per-platform child manifest that Docker pulls based on the
            client's architecture.
          </p>
        </CardContent>
      </Card>

      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
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
            {manifest.manifests.map((m, i) => (
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
    </div>
  );
}

function DigestRow({
  label,
  value,
  mediaType,
  size,
}: {
  label: string;
  value: string;
  mediaType: string;
  size: number;
}): React.ReactElement {
  return (
    <div className="grid grid-cols-[140px_1fr_auto] items-center gap-3">
      <div className="text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div className="flex min-w-0 items-center gap-1.5">
        <code
          className="truncate font-mono text-xs text-[var(--color-fg-muted)]"
          title={value}
        >
          {value}
        </code>
        <CopyButton value={value} iconOnly />
      </div>
      <div className="flex items-center gap-2 text-xs text-[var(--color-fg-muted)]">
        <Badge tone="neutral" className="font-mono text-[10px]">
          {shortMediaType(mediaType)}
        </Badge>
        <span className="font-mono">{formatBytes(size)}</span>
      </div>
    </div>
  );
}

// shortMediaType trims the OCI / Docker media type prefix so the badge
// doesn't dominate the row. "application/vnd.oci.image.layer.v1.tar+gzip"
// → "image.layer.v1.tar+gzip".
function shortMediaType(mt: string): string {
  return mt
    .replace(/^application\/vnd\.(oci|docker)\.(?:distribution\.)?/, "")
    .replace(/^application\//, "");
}

function LoadingSkeleton(): React.ReactElement {
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <Skeleton className="h-3 w-32" />
        </CardHeader>
        <CardContent className="space-y-2">
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-full" />
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <Skeleton className="h-3 w-20" />
        </CardHeader>
        <CardContent className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-8 w-full" />
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
