import * as React from "react";
import { Ship } from "lucide-react";
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
  useChart,
  type ChartMetadata,
  type ChartDependency,
  type ChartMaintainer,
} from "@/lib/api/chart";

interface ChartPanelProps {
  org: string;
  repo: string;
  tag: string;
  // Passed straight through to useChart's `enabled` — the query only fires
  // while the Chart tab is actually open (Task 7 gates this on artifact_type).
  active: boolean;
}

// Beacon — ChartPanel (Helm chart detail tab).
//
// Renders the parsed Chart.yaml metadata + values.yaml that the BFF extracts
// from a Helm artifact's config + content-layer blobs (see lib/api/chart.ts).
// The two halves of the response fail independently: `metadata` is null (with
// metadata_error) when the config blob is unreadable, and `values` is "" (with
// values_error) when the content layer can't be read — so each section guards
// its own presence rather than assuming a fully-populated response.
//
// data === null means the BFF's chart route isn't wired (CORE_GRPC_ADDR unset,
// 404 → null in useChart) — we render an info EmptyState for that, mirroring
// referrers-panel.tsx's "not enabled" vs. "real outage" split.
export function ChartPanel({
  org,
  repo,
  tag,
  active,
}: ChartPanelProps): React.ReactElement {
  const { data, isLoading, isError, error, refetch } = useChart(
    org,
    repo,
    tag,
    active,
  );

  if (isError) {
    return (
      <ErrorState
        title="Couldn't load chart details"
        description="The management API didn't answer. Retry, or check the BFF logs."
        error={error}
        onRetry={() => void refetch()}
      />
    );
  }

  if (isLoading) {
    return <LoadingSkeleton />;
  }

  // Falsy (null) → chart inspection isn't enabled on this control plane.
  if (!data) {
    return (
      <EmptyState
        icon={<Ship className="size-5" />}
        title="Chart details not available"
        description="registry-core chart inspection isn't wired on this control plane, or this tag isn't a Helm chart."
      />
    );
  }

  return (
    <div className="space-y-4">
      {/* Metadata card — or an inline error when the config blob was unreadable. */}
      {data.metadata ? (
        <MetadataCard metadata={data.metadata} />
      ) : (
        <MetadataError message={data.metadata_error} />
      )}

      {/* Dependencies + maintainers only render when the metadata carries them. */}
      {data.metadata?.dependencies?.length ? (
        <DependenciesCard dependencies={data.metadata.dependencies} />
      ) : null}
      {data.metadata?.maintainers?.length ? (
        <MaintainersCard maintainers={data.metadata.maintainers} />
      ) : null}

      {/* values.yaml block (copyable) + truncation / error notes. */}
      <ValuesCard
        values={data.values}
        truncated={data.values_truncated}
        valuesError={data.values_error}
      />
    </div>
  );
}

// MetadataCard renders the Chart.yaml header: name + version, an app-version
// badge, description, a small definition list (api_version / type /
// kube_version / deprecated), external links (home / icon / sources), and
// keyword chips. Every sub-section guards on presence so a sparse Chart.yaml
// never renders empty rows.
function MetadataCard({
  metadata,
}: {
  metadata: ChartMetadata;
}): React.ReactElement {
  const sources = metadata.sources ?? [];
  const keywords = metadata.keywords ?? [];

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-base font-semibold text-[var(--color-fg)]">
            {metadata.name}
          </h3>
          <code className="font-mono text-xs text-[var(--color-fg-muted)]">
            {metadata.version}
          </code>
          {metadata.app_version ? (
            <Badge tone="accent" className="w-fit">
              app {metadata.app_version}
            </Badge>
          ) : null}
          {metadata.deprecated ? (
            <Badge tone="danger" className="w-fit">
              deprecated
            </Badge>
          ) : null}
        </div>
        {metadata.description ? (
          <CardDescription className="text-sm text-[var(--color-fg-muted)]">
            {metadata.description}
          </CardDescription>
        ) : null}
      </CardHeader>
      <CardContent className="space-y-4 pt-0">
        {/* Compact definition list — only the fields that are actually set. */}
        <MetadataDefs metadata={metadata} />

        {/* External resources. Each opens in a new tab (rel=noreferrer). */}
        {(metadata.home || metadata.icon || sources.length > 0) && (
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
            {metadata.home ? (
              <ExternalLink href={metadata.home} label="Home" />
            ) : null}
            {metadata.icon ? (
              <ExternalLink href={metadata.icon} label="Icon" />
            ) : null}
            {sources.map((src) => (
              <ExternalLink key={src} href={src} label={src} />
            ))}
          </div>
        )}

        {/* Keyword chips. */}
        {keywords.length > 0 ? (
          <div className="flex flex-wrap gap-1.5">
            {keywords.map((kw) => (
              <Badge key={kw} tone="neutral" className="w-fit">
                {kw}
              </Badge>
            ))}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

// MetadataDefs renders the scalar Chart.yaml fields as a definition grid.
// Rows with no value are skipped so the grid never shows empty cells.
function MetadataDefs({
  metadata,
}: {
  metadata: ChartMetadata;
}): React.ReactElement | null {
  const rows: Array<[string, string]> = [];
  if (metadata.api_version) rows.push(["API version", metadata.api_version]);
  if (metadata.type) rows.push(["Type", metadata.type]);
  if (metadata.kube_version)
    rows.push(["Kube version", metadata.kube_version]);
  rows.push(["Deprecated", metadata.deprecated ? "yes" : "no"]);

  if (rows.length === 0) return null;

  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
      {rows.map(([k, v]) => (
        <React.Fragment key={k}>
          <dt className="text-[var(--color-fg-subtle)]">{k}</dt>
          <dd className="font-mono text-[var(--color-fg-muted)]">{v}</dd>
        </React.Fragment>
      ))}
    </dl>
  );
}

// safeHref returns href only for http(s) URLs (defence-in-depth — the BFF
// already strips non-http(s) Chart.yaml URLs, but never render an
// attacker-controlled javascript:/data: URL as an anchor href). FUT-022 SEC#1.
function safeHref(u?: string): string | undefined {
  if (!u) return undefined;
  try {
    const scheme = new URL(u).protocol;
    return scheme === "http:" || scheme === "https:" ? u : undefined;
  } catch {
    return undefined;
  }
}

// ExternalLink is a subdued external anchor used for home/icon/source URLs.
// A URL that isn't http(s) (safeHref → undefined) renders as plain text rather
// than an anchor, so a javascript:/data: URL can never become a live href.
function ExternalLink({
  href,
  label,
}: {
  href: string;
  label: string;
}): React.ReactElement {
  const safe = safeHref(href);
  if (!safe) {
    return (
      <span
        className="truncate text-[var(--color-fg-muted)]"
        title={label}
      >
        {label}
      </span>
    );
  }
  return (
    <a
      href={safe}
      target="_blank"
      rel="noreferrer"
      className="truncate text-[var(--color-accent)] hover:underline"
      title={safe}
    >
      {label}
    </a>
  );
}

// MetadataError is shown in place of the metadata card when the config blob
// couldn't be parsed — the values.yaml half may still have rendered fine.
function MetadataError({
  message,
}: {
  message?: string;
}): React.ReactElement {
  return (
    <div className="rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 px-3 py-2 text-xs text-[var(--color-fg-muted)]">
      Chart.yaml metadata couldn't be parsed
      {message ? `: ${message}` : "."}
    </div>
  );
}

// DependenciesCard lists the chart's dependencies (name / version / repository).
// Only rendered by the parent when there is at least one dependency.
function DependenciesCard({
  dependencies,
}: {
  dependencies: ChartDependency[];
}): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Dependencies
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-0">
        <div className="overflow-hidden rounded-md border border-[var(--color-border)]">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Version</TableHead>
                <TableHead>Repository</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {dependencies.map((dep, i) => (
                <TableRow key={`${dep.name ?? "dep"}-${i}`}>
                  <TableCell className="font-medium text-[var(--color-fg)]">
                    {dep.name ?? "—"}
                  </TableCell>
                  <TableCell className="font-mono text-xs text-[var(--color-fg-muted)]">
                    {dep.version ?? "—"}
                  </TableCell>
                  <TableCell className="truncate font-mono text-xs text-[var(--color-fg-muted)]">
                    {dep.repository ?? "—"}
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

// MaintainersCard lists chart maintainers, each linked to their email (mailto)
// or URL when present. Only rendered when there is at least one maintainer.
function MaintainersCard({
  maintainers,
}: {
  maintainers: ChartMaintainer[];
}): React.ReactElement {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
          Maintainers
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-0">
        <ul className="space-y-1 text-sm">
          {maintainers.map((m, i) => (
            <li key={`${m.name ?? "maintainer"}-${i}`}>
              <MaintainerLine maintainer={m} />
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

// MaintainerLine renders one maintainer as a link (mailto: preferred, else the
// url), falling back to plain text when neither is present.
function MaintainerLine({
  maintainer,
}: {
  maintainer: ChartMaintainer;
}): React.ReactElement {
  const name = maintainer.name ?? maintainer.email ?? "Unknown";
  // mailto: is constructed by the FE from the email field, so it is safe as-is;
  // the maintainer homepage URL is attacker-controlled and passes through
  // safeHref so a javascript:/data: value never becomes a live href.
  const href = maintainer.email
    ? `mailto:${maintainer.email}`
    : safeHref(maintainer.url);
  if (href) {
    return (
      <a
        href={href}
        target="_blank"
        rel="noreferrer"
        className="text-[var(--color-accent)] hover:underline"
      >
        {name}
      </a>
    );
  }
  return <span className="text-[var(--color-fg-muted)]">{name}</span>;
}

// ValuesCard renders the values.yaml text in a scrollable monospace block with
// a CopyButton. A truncation banner appears when the BFF capped the payload,
// and an inline note covers the "no values, but there was an error" case.
function ValuesCard({
  values,
  truncated,
  valuesError,
}: {
  values: string;
  truncated: boolean;
  valuesError?: string;
}): React.ReactElement {
  // No values + an error → surface the error rather than an empty <pre>.
  if (!values && valuesError) {
    return (
      <Card>
        <CardHeader className="pb-3">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            values.yaml
          </CardDescription>
        </CardHeader>
        <CardContent className="pt-0">
          <div className="rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 px-3 py-2 text-xs text-[var(--color-fg-muted)]">
            values.yaml couldn't be read: {valuesError}
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
            values.yaml
          </CardDescription>
          {values ? <CopyButton value={values} label="Copy" /> : null}
        </div>
      </CardHeader>
      <CardContent className="space-y-2 pt-0">
        {truncated ? (
          <div className="rounded-md border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 px-3 py-2 text-xs text-[var(--color-fg-muted)]">
            Showing the first 256 KB — values.yaml was truncated.
          </div>
        ) : null}
        <div className="overflow-x-auto rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)]">
          <pre className="p-3 font-mono text-[11px] leading-relaxed text-[var(--color-fg-muted)]">
            {values || "# values.yaml is empty"}
          </pre>
        </div>
      </CardContent>
    </Card>
  );
}

// LoadingSkeleton mirrors referrers-panel's skeleton so the tab has a stable
// shape while the chart detail request is in flight.
function LoadingSkeleton(): React.ReactElement {
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="pb-3">
          <Skeleton className="h-4 w-40" />
        </CardHeader>
        <CardContent className="space-y-2 pt-0">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-4 w-full" />
          ))}
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-3">
          <Skeleton className="h-3 w-24" />
        </CardHeader>
        <CardContent className="pt-0">
          <Skeleton className="h-24 w-full" />
        </CardContent>
      </Card>
    </div>
  );
}
