import * as React from "react";
import { GitCommitHorizontal, ScrollText } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import { useManifest, type ImageProvenance } from "@/lib/api/manifest";
import { formatAbsoluteDate } from "@/lib/format";

interface ProvenancePanelProps {
  org: string;
  repo: string;
  tag: string;
}

// Beacon — ProvenancePanel (Tier 2 #4, image lineage / provenance).
//
// Surfaces the well-known OCI `org.opencontainers.image.*` annotations the BFF
// lifted off the manifest: git commit + source repo (linkified to the commit
// when possible), build/documentation URLs, vendor/version/created, base
// image, and a collapsible raw view of every annotation.
//
// The URL-bearing fields (url, documentation, source) were already sanitised
// server-side through safeExternalURL, so they are guaranteed http(s) or
// absent. As defense in depth we still guard the ONE href we construct on the
// client (the linkified commit URL) with safeHref.
export function ProvenancePanel({
  org,
  repo,
  tag,
}: ProvenancePanelProps): React.ReactElement {
  const { data, isLoading, isError, refetch } = useManifest(org, repo, tag);

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load provenance"
        description="The management API didn't answer. Retry, or check the BFF logs."
        onRetry={() => void refetch()}
      />
    );
  }

  if (isLoading) {
    return <LoadingSkeleton />;
  }

  const provenance = data?.provenance;

  // No provenance block at all → the image was pushed without any OCI
  // `org.opencontainers.image.*` labels (the common case). Render the empty
  // state rather than an error.
  if (!provenance) {
    return (
      <EmptyState
        icon={<ScrollText className="size-5" />}
        title="No provenance annotations"
        description="This image was built without OCI org.opencontainers.image.* labels. Add them at build time (e.g. LABEL org.opencontainers.image.source=…) to surface git commit, source repo, and base image here."
      />
    );
  }

  return (
    <div className="space-y-4">
      <SourceCard provenance={provenance} />
      <MetadataCard provenance={provenance} />
      <BaseImageCard provenance={provenance} />
      <RawAnnotations annotations={provenance.annotations} />
    </div>
  );
}

// SourceCard renders the git-provenance triad: source repo, commit (revision),
// and build/documentation URLs. When both source and revision are present AND
// source is a normal https repo URL we linkify the commit to
// `<source>/commit/<revision>` (GitHub/GitLab style).
function SourceCard({
  provenance,
}: {
  provenance: ImageProvenance;
}): React.ReactElement | null {
  const { source, revision, url, documentation } = provenance;

  // Nothing source-shaped to show → skip the whole card so the panel doesn't
  // render an empty shell.
  if (!source && !revision && !url && !documentation) {
    return null;
  }

  // Construct the commit link only when we have both halves and the source
  // looks like a normal https repo URL. safeHref is the client-side belt to
  // the server's braces (safeExternalURL) so a constructed href can never
  // carry a dangerous scheme.
  const commitUrl =
    source && revision && looksLikeRepoUrl(source)
      ? safeHref(`${stripGitSuffix(source)}/commit/${revision}`)
      : "";

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Source & build
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2.5">
        {revision ? (
          <Row label="Commit">
            <div className="flex min-w-0 items-center gap-1.5">
              <GitCommitHorizontal className="size-3.5 shrink-0 text-[var(--color-fg-subtle)]" />
              {commitUrl ? (
                <a
                  href={commitUrl}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="truncate font-mono text-xs text-[var(--color-accent)] hover:underline"
                  title={revision}
                >
                  {shortRevision(revision)}
                </a>
              ) : (
                <code
                  className="truncate font-mono text-xs text-[var(--color-fg-muted)]"
                  title={revision}
                >
                  {shortRevision(revision)}
                </code>
              )}
              <CopyButton value={revision} iconOnly />
            </div>
          </Row>
        ) : null}
        {source ? (
          <Row label="Source repo">
            <ExternalLink href={source} />
          </Row>
        ) : null}
        {url ? (
          <Row label="Homepage">
            <ExternalLink href={url} />
          </Row>
        ) : null}
        {documentation ? (
          <Row label="Documentation">
            <ExternalLink href={documentation} />
          </Row>
        ) : null}
      </CardContent>
    </Card>
  );
}

// MetadataCard renders the descriptive provenance: vendor, version, created,
// title, description, licenses, authors, ref name. Skipped entirely when none
// are present.
function MetadataCard({
  provenance,
}: {
  provenance: ImageProvenance;
}): React.ReactElement | null {
  const {
    vendor,
    version,
    created,
    title,
    description,
    licenses,
    authors,
    ref_name,
  } = provenance;

  if (
    !vendor &&
    !version &&
    !created &&
    !title &&
    !description &&
    !licenses &&
    !authors &&
    !ref_name
  ) {
    return null;
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Image metadata
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2.5">
        {title ? (
          <Row label="Title">
            <span className="text-sm text-[var(--color-fg)]">{title}</span>
          </Row>
        ) : null}
        {description ? (
          <Row label="Description">
            <span className="text-sm text-[var(--color-fg-muted)]">
              {description}
            </span>
          </Row>
        ) : null}
        {version ? (
          <Row label="Version">
            <code className="font-mono text-xs text-[var(--color-fg-muted)]">
              {version}
            </code>
          </Row>
        ) : null}
        {vendor ? (
          <Row label="Vendor">
            <span className="text-sm text-[var(--color-fg-muted)]">
              {vendor}
            </span>
          </Row>
        ) : null}
        {authors ? (
          <Row label="Authors">
            <span className="text-sm text-[var(--color-fg-muted)]">
              {authors}
            </span>
          </Row>
        ) : null}
        {licenses ? (
          <Row label="Licenses">
            <code className="font-mono text-xs text-[var(--color-fg-muted)]">
              {licenses}
            </code>
          </Row>
        ) : null}
        {ref_name ? (
          <Row label="Ref name">
            <code className="font-mono text-xs text-[var(--color-fg-muted)]">
              {ref_name}
            </code>
          </Row>
        ) : null}
        {created ? (
          <Row label="Created">
            <span
              className="text-sm text-[var(--color-fg-muted)]"
              title={created}
            >
              {formatAbsoluteDate(created)}
            </span>
          </Row>
        ) : null}
      </CardContent>
    </Card>
  );
}

// BaseImageCard renders the base image the artifact was built FROM
// (org.opencontainers.image.base.name / .base.digest). Skipped when neither
// is present.
function BaseImageCard({
  provenance,
}: {
  provenance: ImageProvenance;
}): React.ReactElement | null {
  const { base_name, base_digest } = provenance;
  if (!base_name && !base_digest) {
    return null;
  }
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Base image
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2.5">
        {base_name ? (
          <Row label="Name">
            <code className="truncate font-mono text-xs text-[var(--color-fg-muted)]" title={base_name}>
              {base_name}
            </code>
          </Row>
        ) : null}
        {base_digest ? (
          <Row label="Digest">
            <div className="flex min-w-0 items-center gap-1.5">
              <code
                className="truncate font-mono text-xs text-[var(--color-fg-muted)]"
                title={base_digest}
              >
                {base_digest}
              </code>
              <CopyButton value={base_digest} iconOnly />
            </div>
          </Row>
        ) : null}
      </CardContent>
    </Card>
  );
}

// RawAnnotations renders the bounded raw annotation map inside a native
// <details> disclosure so the common case (operator wants the curated fields)
// stays uncluttered while the full key/value set is one click away. The map is
// already capped server-side (maxRawAnnotations entries, per-value truncation).
function RawAnnotations({
  annotations,
}: {
  annotations?: Record<string, string>;
}): React.ReactElement | null {
  const entries = annotations ? Object.entries(annotations) : [];
  if (entries.length === 0) {
    return null;
  }
  return (
    <Card>
      <CardContent className="pt-4">
        <details className="group">
          <summary className="flex cursor-pointer list-none items-center justify-between text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            <span>Raw annotations ({entries.length})</span>
            <span className="text-[var(--color-fg-subtle)] transition-transform group-open:rotate-90">
              ›
            </span>
          </summary>
          <dl className="mt-3 space-y-1.5">
            {entries.map(([k, v]) => (
              <div
                key={k}
                className="grid grid-cols-[minmax(0,240px)_1fr] gap-3 text-[11px] leading-tight"
              >
                <dt
                  className="truncate font-mono text-[var(--color-fg-subtle)]"
                  title={k}
                >
                  {k}
                </dt>
                <dd
                  className="truncate font-mono text-[var(--color-fg-muted)]"
                  title={v}
                >
                  {v}
                </dd>
              </div>
            ))}
          </dl>
        </details>
      </CardContent>
    </Card>
  );
}

// Row is the shared label/value line used across the provenance cards so the
// left gutter stays aligned.
function Row({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}): React.ReactElement {
  return (
    <div className="grid grid-cols-[120px_1fr] items-start gap-3">
      <div className="pt-0.5 text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

// ExternalLink renders a sanitised http(s) URL as an anchor. The href already
// passed safeExternalURL server-side; safeHref is the defense-in-depth client
// guard so a dangerous scheme can never become a live href even if the wire
// contract regressed.
function ExternalLink({ href }: { href: string }): React.ReactElement {
  const safe = safeHref(href);
  if (!safe) {
    // The value survived to the FE but isn't a safe href — render as plain
    // text rather than a clickable link so nothing dangerous is actionable.
    return (
      <span className="truncate text-sm text-[var(--color-fg-muted)]" title={href}>
        {href}
      </span>
    );
  }
  return (
    <a
      href={safe}
      target="_blank"
      rel="noreferrer noopener"
      className="truncate text-sm text-[var(--color-accent)] hover:underline"
      title={safe}
    >
      {safe}
    </a>
  );
}

// safeHref returns url only when it parses as an http(s) URL — the client-side
// mirror of the backend's safeExternalURL. Anything else (javascript:, data:,
// mailto:, a malformed string) becomes "" so it never reaches an href.
function safeHref(url: string): string {
  if (!url) return "";
  try {
    const parsed = new URL(url);
    if (parsed.protocol === "http:" || parsed.protocol === "https:") {
      return url;
    }
  } catch {
    return "";
  }
  return "";
}

// looksLikeRepoUrl gates commit linkification: only build a `<source>/commit/…`
// URL when source is a normal https web repo URL (github.com / gitlab.com /
// self-hosted over https). We deliberately keep this conservative — a source
// that isn't an obvious browsable repo (git@…, ssh://, git://) is left as a
// non-linkified commit hash rather than guessing a broken URL.
function looksLikeRepoUrl(source: string): boolean {
  try {
    const parsed = new URL(source);
    return parsed.protocol === "https:" && parsed.hostname.length > 0;
  } catch {
    return false;
  }
}

// stripGitSuffix drops a trailing ".git" and/or "/" so the constructed commit
// URL is `https://host/org/repo/commit/<sha>` rather than
// `…/repo.git/commit/<sha>`.
function stripGitSuffix(source: string): string {
  return source.replace(/\/+$/, "").replace(/\.git$/, "");
}

// shortRevision trims a full git SHA to the conventional 12-char short form
// while keeping non-SHA revisions (tags, branch names) intact. The full value
// stays available via the title tooltip + CopyButton.
function shortRevision(revision: string): string {
  if (/^[0-9a-f]{40}$/i.test(revision) || /^[0-9a-f]{64}$/i.test(revision)) {
    return revision.slice(0, 12);
  }
  return revision;
}

function LoadingSkeleton(): React.ReactElement {
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="pb-3">
          <Skeleton className="h-3 w-28" />
        </CardHeader>
        <CardContent className="space-y-2.5">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-5 w-full" />
          ))}
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-3">
          <Skeleton className="h-3 w-24" />
        </CardHeader>
        <CardContent className="space-y-2.5">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-5 w-full" />
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
