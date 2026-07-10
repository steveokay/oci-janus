// FUT-023 Phase 1 — active PR-namespace inventory (read-only).
//
// A table of the currently-active ephemeral per-PR orgs (provider, source repo,
// PR #, org name, created). Provisioning + teardown are entirely webhook-driven
// in Phase 1, so there are no row actions here — this is a visibility surface
// so an admin can see what the GitHub receiver has spun up.
//
// Admin-only: renders nothing for non-admins (the backing route is
// global-admin-gated). Sits below the PRRegistryPanel on the Integrations tab.
import * as React from "react";
import { GitBranch } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/ui/error-state";
import { useIsGlobalAdmin } from "@/lib/api/abilities";
import { usePRNamespaces } from "@/lib/api/pr-registry";
import { formatRelativeDate } from "@/lib/format";

const CARD_CLASS =
  "rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]";

export function PRNamespacesList(): React.ReactElement | null {
  const isAdmin = useIsGlobalAdmin();
  // Admin-only surface — render nothing for everyone else.
  if (!isAdmin) return null;
  return <PRNamespacesListInner />;
}

function PRNamespacesListInner(): React.ReactElement {
  const { data, isLoading, isError, refetch } = usePRNamespaces("active");
  const rows = data?.namespaces ?? [];

  return (
    <section className={CARD_CLASS}>
      <div className="flex items-center gap-2">
        <GitBranch className="size-4 text-[var(--color-fg-muted)]" />
        <h2 className="font-display text-lg font-medium">
          Active PR namespaces
        </h2>
      </div>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
        Ephemeral orgs currently provisioned for open pull requests. These are
        created and torn down automatically as PRs open and close.
      </p>

      <div className="mt-4">
        {isError ? (
          <ErrorState
            title="Couldn't load PR namespaces"
            description="The management API didn't answer. Retry, or check the BFF logs."
            onRetry={() => void refetch()}
          />
        ) : (
          <div className="overflow-hidden rounded-md border border-[var(--color-border)]">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Provider</TableHead>
                  <TableHead>Source repo</TableHead>
                  <TableHead className="w-[70px] text-right">PR #</TableHead>
                  <TableHead>Namespace org</TableHead>
                  <TableHead className="w-[130px]">Created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {isLoading ? (
                  <SkeletonRows />
                ) : rows.length === 0 ? (
                  <TableRow>
                    <TableCell
                      colSpan={5}
                      className="py-8 text-center text-sm text-[var(--color-fg-muted)]"
                    >
                      No active PR namespaces. They appear here when GitHub opens
                      a pull request against a configured repo.
                    </TableCell>
                  </TableRow>
                ) : (
                  rows.map((ns) => (
                    <TableRow key={`${ns.provider}:${ns.source_repo}:${ns.pr_number}`}>
                      <TableCell className="capitalize">{ns.provider}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {ns.source_repo}
                      </TableCell>
                      <TableCell className="text-right font-mono">
                        #{ns.pr_number}
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {ns.org_name}
                      </TableCell>
                      <TableCell className="text-xs text-[var(--color-fg-muted)]">
                        {formatRelativeDate(ns.created_at)}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </section>
  );
}

function SkeletonRows(): React.ReactElement {
  return (
    <>
      {Array.from({ length: 3 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell><Skeleton className="h-4 w-16" /></TableCell>
          <TableCell><Skeleton className="h-4 w-40" /></TableCell>
          <TableCell className="text-right"><Skeleton className="ml-auto h-4 w-8" /></TableCell>
          <TableCell><Skeleton className="h-4 w-36" /></TableCell>
          <TableCell><Skeleton className="h-4 w-20" /></TableCell>
        </TableRow>
      ))}
    </>
  );
}
