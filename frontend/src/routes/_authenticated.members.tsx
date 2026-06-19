import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Building2, Users, ArrowRight } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { EmptyState } from "@/components/ui/empty-state";
import { useRepositories } from "@/lib/api/repositories";

export const Route = createFileRoute("/_authenticated/members")({
  component: MembersLanding,
});

interface OrgSummary {
  org: string;
  repoCount: number;
}

function MembersLanding(): React.ReactElement {
  const {
    data,
    isLoading,
    isError,
    refetch,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = useRepositories({ visibility: "all" });

  // Derive the unique-org list from every page that has come back so far.
  // Auto-fetch the next page in the background — the org count is small
  // enough that we can comfortably read every repo to enumerate orgs.
  // Stops automatically once `hasNextPage` flips false.
  React.useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  const orgs: OrgSummary[] = React.useMemo(() => {
    const map = new Map<string, number>();
    for (const page of data?.pages ?? []) {
      for (const r of page.repositories) {
        map.set(r.org, (map.get(r.org) ?? 0) + 1);
      }
    }
    return [...map.entries()]
      .map(([org, repoCount]) => ({ org, repoCount }))
      .sort((a, b) => a.org.localeCompare(b.org));
  }, [data]);

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-1">
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--color-fg-subtle)]">
          Access
        </p>
        <h1 className="font-display text-3xl font-medium tracking-tight">
          Members
        </h1>
        <p className="text-sm text-[var(--color-fg-muted)]">
          Manage role assignments per organization. Per-repository overrides
          live on each repository's Members tab.
        </p>
      </header>

      {isError ? (
        <ErrorState
          title="Couldn't load organizations"
          description="The repositories list didn't answer, so we can't enumerate orgs. Retry, or check the BFF."
          onRetry={() => void refetch()}
        />
      ) : isLoading && orgs.length === 0 ? (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-4 w-20" />
                <Skeleton className="h-6 w-32" />
              </CardHeader>
              <CardContent>
                <Skeleton className="h-3 w-40" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : orgs.length === 0 ? (
        <EmptyState
          icon={<Building2 className="size-5" />}
          title="No organizations yet"
          description="Once you create a repository, the parent organization appears here for membership management."
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
          {orgs.map((o) => (
            <Link
              key={o.org}
              to="/orgs/$org/members"
              params={{ org: o.org }}
              className="group block rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)] transition-colors hover:border-[var(--color-accent)]"
            >
              <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                Organization
              </CardDescription>
              <div className="mt-1 flex items-center justify-between gap-2">
                <h2 className="font-display text-xl font-medium tracking-tight">
                  {o.org}
                </h2>
                <ArrowRight className="size-4 -translate-x-1 text-[var(--color-fg-subtle)] transition-transform group-hover:translate-x-0 group-hover:text-[var(--color-accent)]" />
              </div>
              <div className="mt-4 flex items-center gap-4 text-xs text-[var(--color-fg-muted)]">
                <span className="inline-flex items-center gap-1.5">
                  <Building2 className="size-3" />
                  {o.repoCount} {o.repoCount === 1 ? "repository" : "repositories"}
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <Users className="size-3" />
                  Manage members
                </span>
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
