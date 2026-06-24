import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { AxiosError } from "axios";
import { toast } from "sonner";
import {
  Boxes,
  Check,
  ChevronDown,
  ChevronRight,
  Database,
  Layers,
  Repeat,
  Trash2,
} from "lucide-react";
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
  useCacheStats,
  useCachedManifests,
  useEvictCachedManifest,
  useScanByDigest,
  useSignaturesByDigest,
  SIGNING_DISABLED,
  type CachedManifest,
} from "@/lib/api/proxy-cache";
import { UpstreamPoliciesCard } from "@/components/workspace/proxy-cache/upstream-policies-card";
import { useWorkspace } from "@/lib/api/workspace";
import { formatAbsoluteDate, formatBytes, formatRelativeDate } from "@/lib/format";

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

  // FUT-015 — workspace host drives the `docker pull` commands in the
  // row expander. We resolve once at the page level so every row shares
  // the same value (and so jsdom tests can mock the hook in one place).
  const workspace = useWorkspace();
  const pullHost = resolvePullHost(workspace.data?.host);

  // Row-expander state — one row at a time keeps the layout calm. A
  // Set would let multiple rows stay open, but the operator usually
  // wants to inspect a single row at a time before copying.
  const [expandedId, setExpandedId] = React.useState<string | null>(null);

  const [evictTarget, setEvictTarget] = React.useState<CachedManifest | null>(null);
  const evict = useEvictCachedManifest();

  // FUT-017 — derive the upstream-name set from the cached-manifest list.
  // The PoliciesCard joins this with the server-side policy rows; sourcing
  // from the manifest list keeps us off the upstreams list API (doesn't
  // exist yet) and means the policy editor matches what the operator can
  // actually see on this page.
  //
  // Hooks must run before any early return per rules-of-hooks; reading the
  // (possibly undefined) listQuery.data here keeps the dependency stable.
  const upstreamNames = React.useMemo(() => {
    const seen = new Set<string>();
    for (const page of listQuery.data?.pages ?? []) {
      for (const m of page.manifests) {
        if (m.upstream_name) seen.add(m.upstream_name);
      }
    }
    return Array.from(seen);
  }, [listQuery.data]);

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

      {/* FUT-017 — per-upstream auto-scan + auto-sign policy editor.
          The card hides itself when both the scanner + signer clients
          are unwired on the BFF, so this slot is invisible by default
          on minimal deployments. */}
      <UpstreamPoliciesCard upstreamNames={upstreamNames} />

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
                  <TableHead className="w-[40px]">
                    <span className="sr-only">Expand</span>
                  </TableHead>
                  <TableHead>Upstream</TableHead>
                  <TableHead>Image</TableHead>
                  <TableHead>Reference</TableHead>
                  <TableHead className="text-right">Size</TableHead>
                  <TableHead>Cached</TableHead>
                  <TableHead>Last pulled</TableHead>
                  <TableHead className="text-right">Pulls</TableHead>
                  {/* FUT-018 — Severity + Signed status per row. The
                      cells fire useScanByDigest / useSignaturesByDigest
                      independently and TanStack Query dedupes identical
                      digest keys so a row that appears twice in the
                      list (rare; same digest, different reference) only
                      hits the BFF once. */}
                  <TableHead>Severity</TableHead>
                  <TableHead>Signed</TableHead>
                  <TableHead aria-label="Actions" className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {allManifests.map((m) => (
                  <CachedManifestRow
                    key={m.id}
                    m={m}
                    pullHost={pullHost}
                    expanded={expandedId === m.id}
                    onToggleExpand={() =>
                      setExpandedId((prev) => (prev === m.id ? null : m.id))
                    }
                    onEvict={() => setEvictTarget(m)}
                  />
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

// FUT-015 — single cached-manifest row + its expander panel.
//
// Mirrors DSGN-021's row-expander shape (chevron in column 0, expanded
// content rendered as a full-width <tr> with colSpan=9). The expander
// surfaces the operator-facing things the row itself doesn't have room
// for: full `docker pull` commands (tag + digest forms) with CopyButton,
// the media type, and absolute ISO timestamps for tickets/changelogs.
interface CachedManifestRowProps {
  m: CachedManifest;
  pullHost: string;
  expanded: boolean;
  onToggleExpand: () => void;
  onEvict: () => void;
}

export function CachedManifestRow({
  m,
  pullHost,
  expanded,
  onToggleExpand,
  onEvict,
}: CachedManifestRowProps): React.ReactElement {
  // Compute both pull commands up-front so the panel can render them
  // without re-doing work on every paint. The digest variant is only
  // shown when the row carries a non-empty digest — older cache rows
  // (pre-FUT-013) sometimes lack it.
  const tagPull = dockerPullCommand(pullHost, m.upstream_name, m.image, {
    reference: m.reference,
  });
  const hasDigest = m.digest.length > 0;
  const digestPull = hasDigest
    ? dockerPullCommand(pullHost, m.upstream_name, m.image, { digest: m.digest })
    : "";

  return (
    <>
      <TableRow>
        <TableCell className="w-[40px] pr-0">
          <button
            type="button"
            aria-label={expanded ? "Hide pull command" : "Show pull command"}
            aria-expanded={expanded}
            onClick={onToggleExpand}
            className="inline-flex size-6 items-center justify-center rounded-md text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-sunken)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-1"
          >
            {expanded ? (
              <ChevronDown className="size-4" />
            ) : (
              <ChevronRight className="size-4" />
            )}
          </button>
        </TableCell>
        <TableCell>
          <Badge tone="neutral">{m.upstream_name}</Badge>
        </TableCell>
        <TableCell className="font-medium">
          {/* FUT-016: click-through to the detail page. Wraps only the
              image cell (not the whole row) so the evict button + the
              row-expand chevron keep their own click semantics. */}
          <Link
            to="/workspace/proxy-cache/$id"
            params={{ id: m.id }}
            className="hover:text-[var(--color-accent)] hover:underline"
          >
            {m.image}
          </Link>
        </TableCell>
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
          {m.last_pulled_at ? formatRelativeDate(m.last_pulled_at) : "Never"}
        </TableCell>
        <TableCell className="text-right tabular-nums">
          {Intl.NumberFormat().format(m.pull_count)}
        </TableCell>
        {/* FUT-018 — Severity + Signed pill cells. Each cell owns its
            own query so a slow scanner doesn't block the signature
            badge from rendering, and vice versa. */}
        <TableCell>
          <SeverityCell digest={m.digest} />
        </TableCell>
        <TableCell>
          <SignedCell digest={m.digest} />
        </TableCell>
        <TableCell>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`Evict ${m.image}:${m.reference}`}
            onClick={onEvict}
          >
            <Trash2 className="size-4" />
          </Button>
        </TableCell>
      </TableRow>
      {expanded ? (
        <TableRow className="bg-[var(--color-surface-sunken)] hover:bg-[var(--color-surface-sunken)]">
          <TableCell colSpan={11} className="px-4 py-4">
            <div className="space-y-4 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
              <PullCommandField label="docker pull (by tag)" value={tagPull} />
              {hasDigest ? (
                <PullCommandField
                  label="docker pull (by digest)"
                  value={digestPull}
                  hint="Pinning to the digest survives upstream tag mutation."
                />
              ) : null}
              <div className="grid gap-3 sm:grid-cols-3">
                <MetaField
                  label="Media type"
                  value={m.media_type || "—"}
                  mono
                />
                <MetaField label="Cached at" value={formatAbsoluteDate(m.fetched_at)} />
                <MetaField
                  label="Last pulled at"
                  value={
                    m.last_pulled_at ? formatAbsoluteDate(m.last_pulled_at) : "Never"
                  }
                />
              </div>
            </div>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

// PullCommandField — labelled `<code>` with an inline CopyButton. Mirrors
// the ChallengeField in DomainsTable so the two row-expanders share a
// visual vocabulary; the only difference is this one carries a longer
// shell command instead of a TXT record fragment.
function PullCommandField({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}): React.ReactElement {
  return (
    <div>
      <div className="mb-1.5 text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div className="flex items-center gap-2 rounded-md border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] px-3 py-2">
        <code className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--color-fg)]">
          {value}
        </code>
        <CopyButton value={value} iconOnly />
      </div>
      {hint ? (
        <p className="mt-1.5 text-xs leading-relaxed text-[var(--color-fg-subtle)]">
          {hint}
        </p>
      ) : null}
    </div>
  );
}

// MetaField — small label/value pair for the bottom row of the expander
// (media type + absolute timestamps). `mono` toggles a monospaced value
// font for the OCI media-type string which is read more easily that way.
function MetaField({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}): React.ReactElement {
  return (
    <div>
      <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div
        className={
          mono
            ? "truncate font-mono text-xs text-[var(--color-fg)]"
            : "truncate text-xs text-[var(--color-fg)]"
        }
        title={value}
      >
        {value}
      </div>
    </div>
  );
}

// resolvePullHost picks the host string we splice into `docker pull`.
//
// Order of preference:
//   1. The workspace's resolved host from FE-API-009 (`workspace.host`).
//      When the operator has registered a custom domain + promoted it,
//      this is the real registry hostname they want their teammates to
//      use — surface it directly.
//   2. window.location.host, EXCEPT when that's the local Vite dev
//      server (`localhost:5173`). The Vite port is the FE bundle, not
//      the registry — docker push/pull against it would 404 instantly.
//      Substitute the dev gateway port `:8084` (local-setup.md §pull-
//      through-cache) so a fresh dev environment shows a command that
//      actually works copy-pasted into a terminal.
//   3. Bare `localhost:8084` as a last-ditch fallback (covers SSR-like
//      contexts where `window` isn't defined — unlikely here but free).
export function resolvePullHost(workspaceHost: string | undefined): string {
  if (workspaceHost && workspaceHost.length > 0) return workspaceHost;
  if (typeof window === "undefined") return "localhost:8084";
  const here = window.location.host;
  if (here.includes(":5173")) {
    // Replace the Vite port with the dev gateway port; preserve hostname
    // so anyone running the dashboard against a non-localhost dev host
    // (e.g. a VM, gitpod) still gets a sensible command.
    return here.replace(/:5173$/, ":8084");
  }
  return here || "localhost:8084";
}

// dockerPullCommand renders the OCI-style pull URI the proxy exposes.
//
//   docker pull <host>/cache/<upstream>/<image>:<tag>      (tag form)
//   docker pull <host>/cache/<upstream>/<image>@<digest>   (digest form)
//
// The `cache/` prefix is the proxy path (see services/proxy/internal/
// handler/http.go — `/v2/cache/<upstream>/<image>/...`). docker strips
// the leading `/v2/` segment when it constructs a pull URI, so what the
// user types is `host/cache/...`.
//
// Exactly one of `reference` / `digest` must be set; the call site
// owns the conditional and we don't try to be clever about both.
export function dockerPullCommand(
  host: string,
  upstream: string,
  image: string,
  ref: { reference: string } | { digest: string },
): string {
  const sep = "reference" in ref ? ":" : "@";
  const value = "reference" in ref ? ref.reference : ref.digest;
  return `docker pull ${host}/cache/${upstream}/${image}${sep}${value}`;
}

// FUT-018 — Severity + Signed cell components.
//
// Each cell fires its own `useScanByDigest` / `useSignaturesByDigest`
// query. TanStack Query's deduplication keys on the queryKey: two
// rows with the same digest share the same in-flight request (rare
// in the cache list — same digest under two references — but free
// to handle). The hooks set `staleTime: 30_000` so scrolling through
// a 100-row table doesn't refetch every cell on every minor
// interaction.
//
// SeverityCell renders the worst-severity badge (CRITICAL > HIGH >
// MEDIUM > LOW > none). For pending/running we render "Scanning…",
// and for unscanned/disabled rows we render an em-dash with neutral
// tone so the visual posture of the column stays calm.
//
// SignedCell renders a check icon when ≥1 signature exists, em-dash
// for unsigned/disabled, and a skeleton while the query is in
// flight. The signing-disabled sentinel collapses to em-dash —
// individual rows don't need to surface "signer unwired" copy
// (the Signing tab on the detail page already does).

interface DigestCellProps {
  digest: string;
}

const SEVERITY_PRIORITY: readonly ("CRITICAL" | "HIGH" | "MEDIUM" | "LOW")[] = [
  "CRITICAL",
  "HIGH",
  "MEDIUM",
  "LOW",
] as const;

export function SeverityCell({ digest }: DigestCellProps): React.ReactElement {
  const { data, isLoading } = useScanByDigest(digest);

  if (isLoading) {
    return <Skeleton className="h-4 w-16" />;
  }

  // No digest → never going to have a scan; render an em-dash with
  // neutral tone so the column stays visually quiet.
  if (!digest || data === null || data === undefined) {
    return (
      <Badge tone="neutral" data-testid="severity-cell-none">
        —
      </Badge>
    );
  }

  if (data.status === "pending" || data.status === "running") {
    return (
      <Badge tone="warning" dot pulse data-testid="severity-cell-scanning">
        Scanning…
      </Badge>
    );
  }

  if (data.status === "failed") {
    return (
      <Badge tone="danger" data-testid="severity-cell-failed">
        Failed
      </Badge>
    );
  }

  // Pick the worst severity present. Severity-zero scans render
  // "Clean" so the operator sees a positive signal rather than an
  // empty cell.
  const counts = data.severity_counts ?? {};
  for (const sev of SEVERITY_PRIORITY) {
    const n = counts[sev] ?? 0;
    if (n > 0) {
      // Map severity → badge tone (matches FindingsTable rows).
      const tone = sev.toLowerCase() as "critical" | "high" | "medium" | "low";
      return (
        <Badge tone={tone} data-testid={`severity-cell-${tone}`}>
          {sev[0] + sev.slice(1).toLowerCase()} ({n})
        </Badge>
      );
    }
  }
  return (
    <Badge tone="success" data-testid="severity-cell-clean">
      Clean
    </Badge>
  );
}

export function SignedCell({ digest }: DigestCellProps): React.ReactElement {
  const { data, isLoading } = useSignaturesByDigest(digest);

  if (isLoading) {
    return <Skeleton className="h-4 w-10" />;
  }

  // SIGNING_DISABLED (signer unwired) and no digest both collapse to
  // em-dash here. The Signing tab on the detail page surfaces the
  // "signer not wired" explainer; the list cell stays terse.
  if (!digest || data === SIGNING_DISABLED || !data || !data.signed) {
    return (
      <span
        className="text-[var(--color-fg-subtle)]"
        aria-label="No signatures recorded"
        data-testid="signed-cell-none"
      >
        —
      </span>
    );
  }

  // At least one signature recorded. Counts of 1 are the common case;
  // higher counts get a small "+N" affordance so the operator can
  // tell a re-sign happened without clicking through.
  const n = data.signatures.length;
  return (
    <span
      className="inline-flex items-center gap-1 text-[var(--color-success)]"
      title={
        n === 1
          ? "Signed"
          : `Signed (${n} signatures)`
      }
      data-testid="signed-cell-signed"
    >
      <Check className="size-4" aria-hidden />
      {n > 1 ? <span className="text-xs font-medium">+{n - 1}</span> : null}
    </span>
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
