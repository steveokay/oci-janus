import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Boxes } from "lucide-react";
import { useRepositories } from "@/lib/api/repositories";
import type { RepoVisibilityFilter } from "@/lib/api/repositories";
import { RepositoriesTable } from "@/components/repositories/repositories-table";
import { RepositoriesToolbar } from "@/components/repositories/toolbar";
import { CreateRepositoryDialog } from "@/components/repositories/create-repository-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/_authenticated/repositories/")({
  component: RepositoriesPage,
});

function RepositoriesPage(): React.ReactElement {
  const [query, setQuery] = React.useState("");
  const [visibility, setVisibility] =
    React.useState<RepoVisibilityFilter>("all");
  const [createOpen, setCreateOpen] = React.useState(false);

  // F4 follow-up — /repositories is now the container-image catalogue.
  // Helm charts, Cosign signatures, and SBOMs live on their own routes
  // (/helm today; more dedicated landings to follow). Passing
  // artifactType: "image" makes the BFF EXISTS-filter manifests so a
  // shared org/repo namespace that holds both an image AND a chart only
  // shows up on the matching listing — no double-counting.
  const {
    data,
    isLoading,
    isError,
    refetch,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useRepositories({ visibility, artifactType: "image" });

  // Flatten infinite pages into a single list. Filtering on the client by
  // name/org is acceptable for the page sizes we expect; server-side search
  // is FE-API-future.
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
          {/* Icon matches the sidebar Boxes glyph — consistent with the */}
          {/* /security (ShieldCheck), /activity (Activity), /helm (Ship) */}
          {/* page-header pattern. */}
          <h1 className="font-display flex items-center gap-3 text-3xl font-medium tracking-tight">
            <Boxes
              className="size-7 text-[var(--color-accent)]"
              aria-hidden
            />
            Repositories
          </h1>
          <p className="text-sm text-[var(--color-fg-muted)]">
            {isLoading
              ? "Loading…"
              : `${total} ${total === 1 ? "repository" : "repositories"} in this workspace`}
          </p>
        </div>
      </header>

      <RepositoriesToolbar
        query={query}
        onQueryChange={setQuery}
        visibility={visibility}
        onVisibilityChange={setVisibility}
        onCreateClick={() => setCreateOpen(true)}
      />

      {isError ? (
        <ErrorState
          title="Couldn't load repositories"
          description="The management API didn't answer. Verify the BFF is reachable, then retry."
          onRetry={() => void refetch()}
        />
      ) : !isLoading && filtered.length === 0 ? (
        <EmptyState
          icon={<Boxes className="size-5" />}
          title={
            query
              ? `No repositories match "${query}"`
              : "No repositories yet"
          }
          description={
            query
              ? "Try a different search term, or clear the filter to see everything."
              : "Create your first repository to push images into this workspace."
          }
          action={
            !query ? (
              <Button onClick={() => setCreateOpen(true)}>
                Create a repository
              </Button>
            ) : (
              <Button variant="outline" onClick={() => setQuery("")}>
                Clear filter
              </Button>
            )
          }
        />
      ) : (
        <>
          <RepositoriesTable
            repositories={filtered}
            loading={isLoading}
            linkArtifactType="image"
          />
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

      <CreateRepositoryDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
      />
    </div>
  );
}
