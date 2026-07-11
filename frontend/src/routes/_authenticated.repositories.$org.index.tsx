import * as React from "react";
import { createFileRoute, Link, useParams } from "@tanstack/react-router";
import { Boxes, ChevronLeft } from "lucide-react";
import { useRepositories } from "@/lib/api/repositories";
import type { RepoVisibilityFilter } from "@/lib/api/repositories";
import { RepositoriesTable } from "@/components/repositories/repositories-table";
import { RepositoriesToolbar } from "@/components/repositories/toolbar";
import { CreateRepositoryDialog } from "@/components/repositories/create-repository-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/_authenticated/repositories/$org/")({
  component: OrgRepositoriesPage,
});

// Per-environment repository list. Same table as the old flat /repositories
// view, but scoped to a single org via the BFF ?org= filter — so search and
// sort now cover just this environment instead of the whole catalogue.
function OrgRepositoriesPage(): React.ReactElement {
  const { org } = useParams({ from: "/_authenticated/repositories/$org/" });
  const [query, setQuery] = React.useState("");
  const [visibility, setVisibility] = React.useState<RepoVisibilityFilter>("all");
  const [createOpen, setCreateOpen] = React.useState(false);

  const {
    data, isLoading, isError, error, refetch,
    fetchNextPage, hasNextPage, isFetchingNextPage,
  } = useRepositories({ visibility, org });

  const flat = React.useMemo(
    () => data?.pages.flatMap((p) => p.repositories) ?? [],
    [data],
  );
  const filtered = React.useMemo(() => {
    if (!query.trim()) return flat;
    const q = query.toLowerCase();
    return flat.filter((r) => r.name.toLowerCase().includes(q));
  }, [flat, query]);

  const searchActive = query.trim() !== "";
  React.useEffect(() => {
    if (searchActive && hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [searchActive, hasNextPage, isFetchingNextPage, fetchNextPage]);

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <Link
          to="/repositories"
          className="flex items-center gap-1 text-xs font-medium text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
        >
          <ChevronLeft className="size-3.5" aria-hidden /> Environments
        </Link>
        <div className="flex items-end justify-between">
          <h1 className="font-display flex items-center gap-3 text-3xl font-medium tracking-tight">
            <Boxes className="size-7 text-[var(--color-accent)]" aria-hidden />
            {org}
          </h1>
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
          error={error}
          onRetry={() => void refetch()}
        />
      ) : !isLoading && filtered.length === 0 ? (
        <EmptyState
          icon={<Boxes className="size-5" />}
          title={query ? `No repositories match "${query}"` : `No repositories in ${org} yet`}
          description={
            query
              ? "Try a different search term, or clear the filter."
              : "Create a repository to push images into this environment."
          }
          action={
            !query ? (
              <Button onClick={() => setCreateOpen(true)}>Create a repository</Button>
            ) : (
              <Button variant="outline" onClick={() => setQuery("")}>Clear filter</Button>
            )
          }
        />
      ) : (
        <>
          <RepositoriesTable
            repositories={filtered}
            loading={isLoading}
            linkArtifactType="image"
            hasNextPage={hasNextPage}
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

      <CreateRepositoryDialog open={createOpen} onOpenChange={setCreateOpen} defaultOrg={org} />
    </div>
  );
}
