import * as React from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Boxes, Plus, Search } from "lucide-react";
import { useOrgs } from "@/lib/api/orgs";
import { OrgCard } from "@/components/orgs/org-card";
import { CreateRepositoryDialog } from "@/components/repositories/create-repository-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export const Route = createFileRoute("/_authenticated/repositories/")({
  component: EnvironmentsPage,
});

// Environments overview. Orgs are the top-level axis (operators use them as
// dev/stage/prod), so /repositories lands on a card per org and drills into
// /repositories/$org. Keeps the "Repositories" label + org vocabulary —
// "environment" is the operator's convention, not baked into the platform.
function EnvironmentsPage(): React.ReactElement {
  const navigate = useNavigate();
  const [query, setQuery] = React.useState("");
  const [createOpen, setCreateOpen] = React.useState(false);
  const { data, isLoading, isError, error, refetch } = useOrgs();

  const orgs = data?.orgs ?? [];

  // Single-org shortcut: a one-environment deployment skips the lonely
  // one-card overview and lands directly in that environment. Deriving a
  // stable `soleOrg` (a string | null, not the freshly-allocated `orgs`
  // array) keeps the effect keyed on the primitive it actually reads, so
  // there's no every-render dependency churn to reason about.
  const soleOrg =
    !isLoading && !isError && orgs.length === 1 ? orgs[0].org : null;

  React.useEffect(() => {
    if (soleOrg) {
      void navigate({
        to: "/repositories/$org",
        params: { org: soleOrg },
        replace: true,
      });
    }
  }, [soleOrg, navigate]);

  const filtered = React.useMemo(() => {
    if (!query.trim()) return orgs;
    const q = query.toLowerCase();
    return orgs.filter((o) => o.org.toLowerCase().includes(q));
  }, [orgs, query]);

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Catalog
        </p>
        <div className="flex items-end justify-between">
          <h1 className="font-display flex items-center gap-3 text-3xl font-medium tracking-tight">
            <Boxes className="size-7 text-[var(--color-accent)]" aria-hidden />
            Repositories
          </h1>
          <p className="text-sm text-[var(--color-fg-muted)]">
            {isLoading
              ? "Loading…"
              : `${orgs.length} ${orgs.length === 1 ? "environment" : "environments"}`}
          </p>
        </div>
      </header>

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative w-full max-w-sm">
          <Search
            className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-[var(--color-fg-subtle)]"
            aria-hidden
          />
          <Input
            className="pl-9"
            type="search"
            placeholder="Filter environments…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Filter environments"
          />
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" />
          New repository
        </Button>
      </div>

      {isError ? (
        <ErrorState
          title="Couldn't load environments"
          description="The management API didn't answer. Verify the BFF is reachable, then retry."
          error={error}
          onRetry={() => void refetch()}
        />
      ) : isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <div
              key={i}
              className="h-32 animate-pulse rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)]"
            />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <EmptyState
          icon={<Boxes className="size-5" />}
          title={query ? `No environments match "${query}"` : "No repositories yet"}
          description={
            query
              ? "Try a different search term, or clear the filter."
              : "Create your first repository to push images into this workspace."
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
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((o) => (
            <OrgCard key={o.org_id} org={o} />
          ))}
        </div>
      )}

      <CreateRepositoryDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
