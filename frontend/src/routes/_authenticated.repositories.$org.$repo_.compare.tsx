import * as React from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { CompareTagsView } from "@/components/repositories/compare-tags-view";
import { EmptyState } from "@/components/ui/empty-state";
import { GitCompare } from "lucide-react";

// Image-diff route (Tier 2 #3). Full-page (escapes the repo-tabs layout via
// the `$repo_` segment) so the four diff sections get room. The two tags come
// as `?from=&to=` search params — populated by the "Compare" action in the
// tags panel, but also deep-linkable / shareable.

interface CompareSearch {
  from?: string;
  to?: string;
}

export const Route = createFileRoute(
  "/_authenticated/repositories/$org/$repo_/compare",
)({
  component: CompareRoute,
  validateSearch: (raw: Record<string, unknown>): CompareSearch => {
    const from = typeof raw.from === "string" ? raw.from : undefined;
    const to = typeof raw.to === "string" ? raw.to : undefined;
    return { from, to };
  },
});

function CompareRoute(): React.ReactElement {
  const { org, repo } = Route.useParams();
  const { from, to } = Route.useSearch();

  return (
    <div className="mx-auto max-w-3xl space-y-5 py-2">
      <Link
        to="/repositories/$org/$repo"
        params={{ org, repo }}
        className="inline-flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
      >
        <ArrowLeft className="size-3.5" />
        Back to {org}/{repo}
      </Link>

      {from && to ? (
        <CompareTagsView org={org} repo={repo} from={from} to={to} />
      ) : (
        <EmptyState
          icon={<GitCompare className="size-5" />}
          title="Pick two tags to compare"
          description="Select exactly two tags in the repository's Tags panel and choose “Compare”, or add ?from=<tagA>&to=<tagB> to the URL."
        />
      )}
    </div>
  );
}
