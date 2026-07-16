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
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { EmptyState } from "@/components/ui/empty-state";
import { Button } from "@/components/ui/button";
import { formatRelativeDate } from "@/lib/format";
import { useServiceAccounts, type ServiceAccount } from "@/lib/api/service-accounts";

// Maximum number of scope chips to render inline before truncating with a
// "+N more" suffix so the cell doesn't overflow on narrow viewports.
const MAX_SCOPE_CHIPS = 3;

// Maximum description length before the text is clamped to 80 chars.
const MAX_DESC_LEN = 80;

interface ServiceAccountsTableProps {
  // onSelect is called with the service account's ID when the operator clicks
  // a row. The route handler sets ?id=<id> so T26's drawer can open.
  onSelect: (id: string) => void;
  // onAdd is surfaced to the empty-state CTA so the parent can open the
  // Create dialog without duplicating the trigger logic.
  onAdd: () => void;
}

// Beacon — ServiceAccountsTable. Admin-only list view for the /api-keys/
// service-accounts route (FE-API-048 T25). Reads from useServiceAccounts
// with includeDisabled:true so the operator can see disabled SAs and
// reenable them from the detail drawer (T26).
export function ServiceAccountsTable({
  onSelect,
  onAdd,
}: ServiceAccountsTableProps): React.ReactElement {
  const { data: accounts, isLoading, isError, refetch } = useServiceAccounts({
    includeDisabled: true,
  });

  // Error state — inline per Beacon design direction.
  if (isError) {
    return (
      <div
        role="alert"
        className="flex flex-col items-start gap-3 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-5 py-4"
      >
        <div className="flex items-center gap-2 text-[var(--color-danger)]">
          <Bot className="size-4" aria-hidden />
          <span className="text-sm font-semibold">
            Couldn't load service accounts
          </span>
        </div>
        <p className="text-sm text-[var(--color-fg-muted)]">
          The service accounts endpoint didn't respond. Confirm the management
          BFF has AUTH_GRPC_ADDR wired, then retry.
        </p>
        <Button variant="outline" size="sm" onClick={() => void refetch()}>
          Retry
        </Button>
      </div>
    );
  }

  // Empty state — shown when the list loads with no entries.
  if (!isLoading && accounts?.length === 0) {
    return (
      <EmptyState
        icon={<Bot className="size-5" />}
        title="No service accounts yet"
        description="Service accounts are machine identities for CI pipelines and Terraform modules. Create one to issue scoped API keys that can be rotated independently."
        action={
          <Button variant="accent" size="sm" onClick={onAdd}>
            Add one
          </Button>
        }
      />
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
      <Table>
        <TableHeader>
          <TableRow>
            {/* Name + description live in the first column so they share the
                wider real-estate; description is truncated to 80 chars. */}
            <TableHead className="w-[30%]">Account</TableHead>
            <TableHead className="w-[8%] text-center">Keys</TableHead>
            <TableHead className="hidden sm:table-cell">Last used</TableHead>
            <TableHead className="hidden md:table-cell">Scopes</TableHead>
            <TableHead>Status</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {isLoading
            ? Array.from({ length: 5 }).map((_, i) => <SkeletonRow key={i} />)
            : (accounts ?? []).map((sa) => (
                <SARow key={sa.id} sa={sa} onSelect={onSelect} />
              ))}
        </TableBody>
      </Table>
    </div>
  );
}

// SARow renders a single service account row. The whole row is clickable
// (convenience for mouse users); the account-name link is the true focusable
// target so keyboard users can still navigate without losing column fidelity.
function SARow({
  sa,
  onSelect,
}: {
  sa: ServiceAccount;
  onSelect: (id: string) => void;
}): React.ReactElement {
  // Truncate description to MAX_DESC_LEN chars with an ellipsis.
  const desc =
    sa.description && sa.description.length > MAX_DESC_LEN
      ? `${sa.description.slice(0, MAX_DESC_LEN)}…`
      : (sa.description ?? "");

  // Scope chips — show up to MAX_SCOPE_CHIPS then a "+N more" badge.
  const visibleScopes = sa.allowed_scopes.slice(0, MAX_SCOPE_CHIPS);
  const hiddenCount = sa.allowed_scopes.length - visibleScopes.length;

  const isDisabled = !!sa.disabled_at;

  return (
    <TableRow interactive onClick={() => onSelect(sa.id)}>
      {/* Account name + description */}
      <TableCell>
        {/* The MCP badge lives as a sibling of the name button (not nested)
            because PopoverTrigger renders a <button> and a button-in-button is
            invalid HTML that breaks focus + click handling. */}
        <div className="flex items-center gap-2">
          <button
            type="button"
            // The button is a semantic interactive element that stops the event
            // from bubbling to the row click (which would call onSelect twice).
            onClick={(e) => {
              e.stopPropagation();
              onSelect(sa.id);
            }}
            className="flex min-w-0 items-center gap-3 text-left"
          >
            {/* Robot avatar — Beacon-style round swatch matching
                RepositoriesTable's icon pattern */}
            <span
              className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] text-[var(--color-accent)]"
              aria-hidden
            >
              <Bot className="size-4" />
            </span>
            <div className="min-w-0">
              <div className="truncate text-sm font-medium text-[var(--color-fg)]">
                {sa.name}
              </div>
              {desc ? (
                <div className="mt-0.5 truncate text-[11px] text-[var(--color-fg-subtle)]">
                  {desc}
                </div>
              ) : null}
            </div>
          </button>

          {/* MCP-minted accounts get an informational badge whose popover
              explains the advisory nature of the *:read scopes. Only the
              one-click MCP connect flow stamps origin: "mcp-connect". */}
          {sa.origin === "mcp-connect" ? (
            <Popover>
              <PopoverTrigger asChild>
                {/* Wrap in a span so the badge's own styling is preserved and
                    the trigger click doesn't bubble to the row's onSelect. */}
                <span onClick={(e) => e.stopPropagation()}>
                  <Badge tone="accent">MCP</Badge>
                </span>
              </PopoverTrigger>
              <PopoverContent className="max-w-xs p-3 text-xs text-[var(--color-fg-muted)]">
                Minted by the MCP one-click connect (Settings › Integrations).
                The
                <span className="font-mono"> *:read </span>
                scopes are advisory — MCP read access is governed by the key's
                reader role, not by these labels.
              </PopoverContent>
            </Popover>
          ) : null}
        </div>
      </TableCell>

      {/* Active key count — badge centred so the column is easy to scan. */}
      <TableCell className="text-center">
        <Badge
          tone={sa.active_key_count > 0 ? "accent" : "neutral"}
          className="tabular-nums"
        >
          {sa.active_key_count}
        </Badge>
      </TableCell>

      {/* Last used — relative date; "never" when the field is absent/null. */}
      <TableCell className="hidden text-[var(--color-fg-muted)] sm:table-cell">
        {sa.last_used_at ? formatRelativeDate(sa.last_used_at) : "never"}
      </TableCell>

      {/* Allowed scopes — up to MAX_SCOPE_CHIPS chips then overflow badge. */}
      <TableCell className="hidden md:table-cell">
        <div className="flex flex-wrap gap-1">
          {visibleScopes.map((scope) => (
            <Badge key={scope} tone="neutral" className="font-mono text-[11px]">
              {scope}
            </Badge>
          ))}
          {hiddenCount > 0 ? (
            <Badge tone="neutral" className="text-[11px]">
              +{hiddenCount} more
            </Badge>
          ) : null}
          {sa.allowed_scopes.length === 0 ? (
            <span className="text-[11px] text-[var(--color-fg-subtle)]">
              none
            </span>
          ) : null}
        </div>
      </TableCell>

      {/* Status — Active / Disabled */}
      <TableCell>
        {isDisabled ? (
          <Badge tone="neutral" dot>
            Disabled
          </Badge>
        ) : (
          <Badge tone="success" dot>
            Active
          </Badge>
        )}
      </TableCell>
    </TableRow>
  );
}

// SkeletonRow matches the column layout of SARow so loading state doesn't
// cause a layout shift when the real data arrives.
function SkeletonRow(): React.ReactElement {
  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-3">
          <Skeleton className="size-8 rounded-md" />
          <div className="space-y-1.5">
            <Skeleton className="h-3.5 w-40" />
            <Skeleton className="h-2.5 w-64" />
          </div>
        </div>
      </TableCell>
      <TableCell className="text-center">
        <Skeleton className="mx-auto h-5 w-8 rounded-full" />
      </TableCell>
      <TableCell className="hidden sm:table-cell">
        <Skeleton className="h-3 w-24" />
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <div className="flex gap-1">
          <Skeleton className="h-5 w-12 rounded-full" />
          <Skeleton className="h-5 w-12 rounded-full" />
        </div>
      </TableCell>
      <TableCell>
        <Skeleton className="h-5 w-16 rounded-full" />
      </TableCell>
    </TableRow>
  );
}
