import * as React from "react";
import { AxiosError } from "axios";
import { FileSignature } from "lucide-react";
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
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import {
  useReferrers,
  referrerTypeLabel,
  referrerTypeTone,
  type Referrer,
} from "@/lib/api/referrers";
import { formatBytes } from "@/lib/format";

interface ReferrersPanelProps {
  org: string;
  repo: string;
  tag: string;
}

// Beacon — ReferrersPanel (OCI referrers tab).
//
// Lists the artifacts attached to a tag's manifest via the OCI referrers API:
// Cosign signatures, SBOMs, and in-toto/DSSE attestations. Each row gets a
// friendly type label (see referrerTypeLabel) + a short/copyable digest so an
// operator can eyeball what's attached without reading raw JSON.
//
// A 404 from the BFF means CORE_GRPC_ADDR isn't wired on this control plane —
// we render an info EmptyState for that case rather than an error, mirroring
// admin/gc-card.tsx's "not wired" vs. "real outage" split.
export function ReferrersPanel({
  org,
  repo,
  tag,
}: ReferrersPanelProps): React.ReactElement {
  const { data, isLoading, isError, error, refetch } = useReferrers(
    org,
    repo,
    tag,
  );

  if (isError) {
    const code = (error as AxiosError | undefined)?.response?.status;
    // 404 → the referrers view isn't available on this deployment (the BFF
    // route is only mounted when CORE_GRPC_ADDR is set). Explain that instead
    // of showing a scary error banner.
    if (code === 404) {
      return (
        <EmptyState
          icon={<FileSignature className="size-5" />}
          title="Referrers view isn't enabled on this control plane"
          description="Set CORE_GRPC_ADDR on the management BFF and restart to see signatures, SBOMs, and attestations attached to this image."
        />
      );
    }
    return (
      <ErrorState
        title="Couldn't load referrers"
        description="The management API didn't answer. Retry, or check the BFF logs."
        error={error}
        onRetry={() => void refetch()}
      />
    );
  }

  if (isLoading) {
    return <LoadingSkeleton />;
  }

  const referrers = data?.referrers ?? [];

  if (referrers.length === 0) {
    return (
      <EmptyState
        icon={<FileSignature className="size-5" />}
        title="No referrers"
        description="Signatures, SBOMs, and attestations attached to this image will appear here."
      />
    );
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            Attached artifacts
          </CardDescription>
          <span className="text-xs text-[var(--color-fg-muted)]">
            {referrers.length}{" "}
            {referrers.length === 1 ? "referrer" : "referrers"}
          </span>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        <div className="overflow-hidden rounded-md border border-[var(--color-border)]">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Type</TableHead>
                <TableHead>Digest</TableHead>
                <TableHead className="text-right">Size</TableHead>
                <TableHead>Annotations</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {referrers.map((r, i) => (
                <ReferrerRow key={`${r.digest}-${i}`} r={r} />
              ))}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

// ReferrerRow renders a single referrer descriptor. The Type cell pairs the
// friendly label (badge) with the subdued raw artifact_type beneath so the
// classification stays legible while the ground truth is still one glance away.
function ReferrerRow({ r }: { r: Referrer }): React.ReactElement {
  const label = referrerTypeLabel(r);
  const tone = referrerTypeTone(r);
  // Show the raw artifact_type when present; fall back to the media_type so the
  // row never leaves the operator guessing what the descriptor actually was.
  const rawType = r.artifact_type || r.media_type;

  return (
    <TableRow>
      <TableCell>
        <div className="flex flex-col gap-0.5">
          <Badge tone={tone} className="w-fit">
            {label}
          </Badge>
          <code
            className="truncate font-mono text-[10px] text-[var(--color-fg-subtle)]"
            title={rawType}
          >
            {rawType}
          </code>
        </div>
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-1.5">
          <code
            className="truncate font-mono text-[11px] text-[var(--color-fg-muted)]"
            title={r.digest}
          >
            {shortDigest(r.digest)}
          </code>
          <CopyButton value={r.digest} iconOnly />
        </div>
      </TableCell>
      <TableCell className="text-right font-mono text-xs">
        {formatBytes(r.size)}
      </TableCell>
      <TableCell>
        <AnnotationCell annotations={r.annotations} />
      </TableCell>
    </TableRow>
  );
}

// AnnotationCell renders the referrer's annotations as a compact key/value
// list (not raw JSON — that's the whole point of this feature). No annotations
// → a subdued em dash so the column still reads as "nothing here".
function AnnotationCell({
  annotations,
}: {
  annotations?: Record<string, string>;
}): React.ReactElement {
  const entries = annotations ? Object.entries(annotations) : [];
  if (entries.length === 0) {
    return <span className="text-xs text-[var(--color-fg-subtle)]">—</span>;
  }
  return (
    <dl className="space-y-0.5">
      {entries.map(([k, v]) => (
        <div key={k} className="flex gap-1.5 text-[11px] leading-tight">
          <dt
            className="shrink-0 truncate font-mono text-[var(--color-fg-subtle)]"
            title={k}
          >
            {shortAnnotationKey(k)}
          </dt>
          <dd className="truncate text-[var(--color-fg-muted)]" title={v}>
            {v}
          </dd>
        </div>
      ))}
    </dl>
  );
}

// shortDigest renders the algorithm prefix + the first 8 hex chars, e.g.
// "sha256:1a2b3c4d…". The full digest stays available via the title tooltip
// + CopyButton, mirroring how the layers table renders digests.
function shortDigest(digest: string): string {
  const [algo, hex] = digest.split(":");
  if (!hex) return digest;
  return `${algo}:${hex.slice(0, 8)}…`;
}

// shortAnnotationKey trims the long OCI annotation namespace so the key column
// stays scannable. "org.opencontainers.image.created" → "image.created".
function shortAnnotationKey(key: string): string {
  return key.replace(/^org\.opencontainers\./, "");
}

function LoadingSkeleton(): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <Skeleton className="h-3 w-32" />
      </CardHeader>
      <CardContent className="space-y-2 pt-0">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-10 w-full" />
        ))}
      </CardContent>
    </Card>
  );
}
