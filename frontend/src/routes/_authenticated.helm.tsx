import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Ship, Info, Boxes } from "lucide-react";
import { useRepositories } from "@/lib/api/repositories";
import type { RepoVisibilityFilter } from "@/lib/api/repositories";
import { RepositoriesTable } from "@/components/repositories/repositories-table";
import { RepositoriesToolbar } from "@/components/repositories/toolbar";
import { CreateRepositoryDialog } from "@/components/repositories/create-repository-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Button } from "@/components/ui/button";

// /helm — Helm chart catalogue (S-MAINT-1 Batch 5 F4 follow-up).
//
// Dedicated top-level route for chart users (platform engineers,
// `helm install` callers) so they can find charts without scrolling past
// hundreds of container-image repos. The MVP renders the same repository
// listing as `/repositories` but with chart-focused header copy + a
// stepwise hint that the artifact-type filter chip on a repo's tags page
// is where the actual chart-vs-image discrimination happens.
//
// Why we don't filter the list to "repos that contain at least one Helm
// chart" yet: the BFF doesn't expose a repository-level artifact-type
// summary, and the F4 chip filter that DOES exist operates on the tags
// table (one repo can legitimately hold image + chart + cosign sig). A
// workspace-wide chart browser that aggregates per-tag rows across repos
// is the right shape long-term; tracked as a follow-up in futures.md.
// Until then this page is "repository catalogue, chart edition" — same
// data, different framing.
export const Route = createFileRoute("/_authenticated/helm")({
  component: HelmChartsPage,
});

function HelmChartsPage(): React.ReactElement {
  const [query, setQuery] = React.useState("");
  const [visibility, setVisibility] =
    React.useState<RepoVisibilityFilter>("all");
  const [createOpen, setCreateOpen] = React.useState(false);

  const {
    data,
    isLoading,
    isError,
    refetch,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useRepositories({ visibility });

  // Same flatten + client-side search as /repositories. Server-side
  // search + artifact-type filtering at the repository level is a
  // future-tracker item.
  const flat = React.useMemo(
    () => data?.pages.flatMap((p) => p.repositories) ?? [],
    [data],
  );

  const filtered = React.useMemo(() => {
    if (!query.trim()) return flat;
    const q = query.toLowerCase();
    return flat.filter(
      (r) =>
        r.name.toLowerCase().includes(q) || r.org.toLowerCase().includes(q),
    );
  }, [flat, query]);

  const total = data?.pages[0]?.total ?? flat.length;

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Catalog
        </p>
        <div className="flex items-end justify-between">
          <h1 className="flex items-center gap-2.5 font-display text-3xl font-medium tracking-tight">
            <Ship
              className="size-7 text-[var(--color-accent)]"
              aria-hidden
            />
            Helm charts
          </h1>
          <p className="text-sm text-[var(--color-fg-muted)]">
            {isLoading
              ? "Loading…"
              : `${total} ${total === 1 ? "repository" : "repositories"} in this workspace`}
          </p>
        </div>
        <p className="mt-1 max-w-3xl text-sm text-[var(--color-fg-muted)]">
          Helm charts are stored as OCI artifacts alongside container images.
          A single repository can hold both — push a chart with{" "}
          <code className="rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 font-mono text-[12px]">
            helm push myapp-1.0.0.tgz oci://&lt;registry&gt;/&lt;org&gt;
          </code>{" "}
          and install it with{" "}
          <code className="rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 font-mono text-[12px]">
            helm install myapp oci://&lt;registry&gt;/&lt;org&gt;/myapp --version 1.0.0
          </code>
          .
        </p>
      </header>

      {/* Hint banner: the chart-vs-image discrimination lives on per-repo
          tags pages today. Renders only when the list is non-empty so the
          empty state can be the primary copy on first-use. */}
      {!isLoading && filtered.length > 0 ? (
        <div
          role="note"
          className="flex items-start gap-2.5 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3 text-sm text-[var(--color-fg-muted)]"
        >
          <Info
            className="mt-0.5 size-4 shrink-0 text-[var(--color-accent)]"
            aria-hidden
          />
          <div>
            This catalogue shows every repository in the workspace. Open a
            repository and use the artifact-type filter chips on its{" "}
            <strong>Tags</strong> tab to see only Helm chart manifests. A
            dedicated workspace-wide chart browser (flat list of chart
            name + version across repos) ships in a follow-up sprint.
          </div>
        </div>
      ) : null}

      <RepositoriesToolbar
        query={query}
        onQueryChange={setQuery}
        visibility={visibility}
        onVisibilityChange={setVisibility}
        onCreateClick={() => setCreateOpen(true)}
      />

      {isError ? (
        <ErrorState
          title="Couldn't load Helm charts"
          description="The management API didn't answer. Verify the BFF is reachable, then retry."
          onRetry={() => void refetch()}
        />
      ) : !isLoading && filtered.length === 0 ? (
        // EmptyState's description prop is string-only. We use the shared
        // primitive for the title + action + icon and render the richer
        // copy (with inline <code> snippets + a /repositories link) as a
        // sibling block so a Helm-curious user still gets the install
        // hints without leaving the page.
        <div className="space-y-4">
          <EmptyState
            icon={<Ship className="size-5" />}
            title={
              query
                ? `No repositories match "${query}"`
                : "No Helm charts yet"
            }
            description={
              query
                ? "Try a different search term, or clear the filter to see everything."
                : undefined
            }
            action={
              !query ? (
                <Button onClick={() => setCreateOpen(true)}>
                  <Boxes className="mr-1.5 size-3.5" />
                  Create a repository
                </Button>
              ) : (
                <Button variant="outline" onClick={() => setQuery("")}>
                  Clear filter
                </Button>
              )
            }
          />
          {!query ? (
            <p className="mx-auto max-w-2xl text-center text-sm text-[var(--color-fg-muted)]">
              Push your first chart with{" "}
              <code className="rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 font-mono text-[12px]">
                helm push &lt;chart&gt;.tgz oci://&lt;registry&gt;/&lt;org&gt;
              </code>{" "}
              — it'll appear here as a repository alongside any container
              images in the same namespace. Or browse all{" "}
              <Link
                to="/repositories"
                className="text-[var(--color-accent)] hover:underline"
              >
                repositories
              </Link>{" "}
              to see what's been pushed so far.
            </p>
          ) : null}
        </div>
      ) : (
        <>
          <RepositoriesTable repositories={filtered} loading={isLoading} />
          {hasNextPage ? (
            <div className="flex justify-center pt-2">
              <Button
                variant="outline"
                onClick={() => void fetchNextPage()}
                loading={isFetchingNextPage}
                disabled={isFetchingNextPage}
              >
                {isFetchingNextPage ? "Loading more" : "Load more"}
              </Button>
            </div>
          ) : null}
        </>
      )}

      <CreateRepositoryDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
