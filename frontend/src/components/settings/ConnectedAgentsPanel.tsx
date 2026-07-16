import * as React from "react";
import { Bot } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import { formatRelativeDate } from "@/lib/format";
import {
  useServiceAccounts,
  useDeleteServiceAccount,
  type ServiceAccount,
} from "@/lib/api/service-accounts";

// Beacon — ConnectedAgentsPanel (FUT-088 #7). Settings › Connected Agents
// (MCP) surface. Lists the service accounts minted by the one-click MCP
// connect flow (Settings › Integrations) so an operator can see which AI
// agents hold a live key, when each was last used, and revoke one in a
// single click.
//
// Client-side filter (origin === "mcp-connect"): the backend has no
// origin filter on GET /service-accounts, and SA counts are small in a
// single-tenant deployment, so filtering the already-fetched list in the
// browser is cheaper than adding a server param + round-trip. The full
// list (includeDisabled) is fetched once and the SA table shares the same
// query cache key, so this reuses the warmed cache rather than double-fetching.
export function ConnectedAgentsPanel(): React.ReactElement {
  const { data, isLoading, isError } = useServiceAccounts({
    includeDisabled: true,
  });
  const del = useDeleteServiceAccount();

  // The SA pending revocation (drives the confirm dialog). null = closed.
  const [pending, setPending] = React.useState<ServiceAccount | null>(null);

  // Keep only MCP-minted accounts. See the client-side-filter note above.
  const agents = React.useMemo(
    () => (data ?? []).filter((sa) => sa.origin === "mcp-connect"),
    [data],
  );

  async function handleRevoke(): Promise<void> {
    if (!pending) return;
    await del.mutateAsync(pending.id);
    setPending(null);
  }

  // Error — inline, matching ServiceAccountsTable's posture.
  if (isError) {
    return (
      <div
        role="alert"
        className="flex flex-col items-start gap-2 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-5 py-4"
      >
        <div className="flex items-center gap-2 text-[var(--color-danger)]">
          <Bot className="size-4" aria-hidden />
          <span className="text-sm font-semibold">
            Couldn't load connected agents
          </span>
        </div>
        <p className="text-sm text-[var(--color-fg-muted)]">
          The service accounts endpoint didn't respond. Confirm the management
          BFF has AUTH_GRPC_ADDR wired, then reload.
        </p>
      </div>
    );
  }

  // Empty — no MCP-minted accounts (either none exist, or all are manual).
  if (!isLoading && agents.length === 0) {
    return (
      <EmptyState
        icon={<Bot className="size-5" />}
        title="No connected agents yet"
        description="Agents connected via the one-click MCP setup (Settings › Integrations) appear here. Generate a config there to connect Claude Desktop or Cursor to this registry."
      />
    );
  }

  return (
    <>
      <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-[36%]">Agent</TableHead>
              <TableHead className="w-[8%] text-center">Keys</TableHead>
              <TableHead className="hidden sm:table-cell">Last used</TableHead>
              <TableHead className="hidden md:table-cell">Created</TableHead>
              <TableHead className="text-right">Revoke</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(agents ?? []).map((sa) => (
              <TableRow key={sa.id}>
                {/* Agent name + MCP badge — mirrors ServiceAccountsTable so the
                    two surfaces read as siblings. */}
                <TableCell>
                  <div className="flex items-center gap-2">
                    <span
                      className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
                      aria-hidden
                    >
                      <Bot className="size-4" />
                    </span>
                    <span className="truncate text-sm font-medium text-[var(--color-fg)]">
                      {sa.name}
                    </span>
                    <Badge tone="accent">MCP</Badge>
                  </div>
                </TableCell>

                {/* Active key count. */}
                <TableCell className="text-center">
                  <Badge
                    tone={sa.active_key_count > 0 ? "accent" : "neutral"}
                    className="tabular-nums"
                  >
                    {sa.active_key_count}
                  </Badge>
                </TableCell>

                {/* Last used — "never" when the field is absent/null. */}
                <TableCell className="hidden text-[var(--color-fg-muted)] sm:table-cell">
                  {sa.last_used_at ? formatRelativeDate(sa.last_used_at) : "never"}
                </TableCell>

                {/* Created — em dash when absent. */}
                <TableCell className="hidden text-[var(--color-fg-muted)] md:table-cell">
                  {sa.created_at ? formatRelativeDate(sa.created_at) : "—"}
                </TableCell>

                {/* Revoke — deletes the SA (and its shadow user) after a
                    type-to-confirm. */}
                <TableCell className="text-right">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setPending(sa)}
                  >
                    Revoke
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      {/* Type-to-confirm (severity medium): the operator retypes the agent
          name before the key + shadow user are hard-deleted. */}
      <ConfirmDestructiveDialog
        open={pending !== null}
        onOpenChange={(next) => {
          if (!next) setPending(null);
        }}
        severity="medium"
        title="Revoke connected agent"
        description={
          <>
            This permanently deletes the service account and its API key. The
            connected agent will lose access to this registry immediately and
            the key cannot be restored.
          </>
        }
        resourceName={pending?.name}
        confirmLabel="Revoke"
        loading={del.isPending}
        onConfirm={handleRevoke}
      />
    </>
  );
}
