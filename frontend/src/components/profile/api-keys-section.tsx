import * as React from "react";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { CopyButton } from "@/components/ui/copy-button";
import { ExpiryBadge } from "@/components/ui/expiry-badge";
import { CreateApiKeyDialog } from "./create-api-key-dialog";
import { DeleteApiKeyDialog } from "./delete-api-key-dialog";
import { useApiKeys, type ApiKey } from "@/lib/api/api-keys";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";

// Beacon — ApiKeysSection. Sits below IdentityCard on the /profile route.
// Self-contained: owns the list query + create + delete flows so the route
// file stays declarative.
export function ApiKeysSection(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useApiKeys();
  const [createOpen, setCreateOpen] = React.useState(false);
  const [deleteTarget, setDeleteTarget] = React.useState<ApiKey | null>(null);

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              API keys
            </CardDescription>
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
              Long-lived credentials for CI / Terraform / scripts. Treat each
              key like a password.
            </p>
          </div>
          <Button onClick={() => setCreateOpen(true)} size="sm">
            <Plus className="size-3.5" />
            Issue key
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorState
            title="Couldn't load API keys"
            description="The auth service didn't answer. Retry, or check the logs."
            onRetry={() => void refetch()}
          />
        ) : !isLoading && (data?.length ?? 0) === 0 ? (
          <EmptyState
            icon={<KeyRound className="size-5" />}
            title="No API keys yet"
            description="Issue your first key to authenticate a robot account against the registry."
            action={
              <Button onClick={() => setCreateOpen(true)} size="sm">
                <Plus className="size-3.5" />
                Issue first key
              </Button>
            }
          />
        ) : (
          <KeysTable
            keys={data ?? []}
            loading={isLoading}
            onDelete={setDeleteTarget}
          />
        )}
      </CardContent>

      <CreateApiKeyDialog open={createOpen} onOpenChange={setCreateOpen} />
      {deleteTarget ? (
        <DeleteApiKeyDialog
          open
          onOpenChange={(o) => {
            if (!o) setDeleteTarget(null);
          }}
          apiKey={deleteTarget}
        />
      ) : null}
    </Card>
  );
}

interface KeysTableProps {
  keys: ApiKey[];
  loading?: boolean;
  onDelete: (k: ApiKey) => void;
}

function KeysTable({
  keys,
  loading,
  onDelete,
}: KeysTableProps): React.ReactElement {
  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)]">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[35%]">Name</TableHead>
            <TableHead>Key ID</TableHead>
            <TableHead>Last used</TableHead>
            {/* ApiKey carries expires_at (services/auth apiKeyResponse), so we */}
            {/* surface it with an urgency treatment (expired/soon/ok). */}
            <TableHead className="hidden md:table-cell">Expires</TableHead>
            <TableHead className="hidden lg:table-cell">Issued</TableHead>
            <TableHead className="w-[100px] text-right">
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading ? (
            <SkeletonRows />
          ) : (
            keys.map((k) => (
              // B2 fix: services/auth's apiKeyResponse uses `id`, `prefix`, no
              // `description`; the column shows the prefix (which is what a
              // human can recognise — first 12 chars of the raw key) rather
              // than the row's primary-key UUID.
              <TableRow key={k.id}>
                <TableCell className="py-3">
                  <div className="text-sm font-medium text-[var(--color-fg)]">
                    {k.name}
                  </div>
                  {k.scopes.length > 0 ? (
                    <div className="mt-0.5 flex flex-wrap gap-1 text-[10px] text-[var(--color-fg-muted)]">
                      {k.scopes.map((s) => (
                        <span
                          key={s}
                          className="rounded-sm bg-[var(--color-bg-muted)] px-1.5 py-0.5 font-mono"
                        >
                          {s}
                        </span>
                      ))}
                    </div>
                  ) : null}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <code className="font-mono text-[11px] text-[var(--color-fg-muted)]">
                      {k.prefix}…
                    </code>
                    <CopyButton value={k.prefix} iconOnly />
                  </div>
                </TableCell>
                <TableCell>
                  {k.last_used_at ? (
                    <span
                      className="text-xs text-[var(--color-fg)]"
                      title={formatAbsoluteDate(k.last_used_at)}
                    >
                      {formatRelativeDate(k.last_used_at)}
                    </span>
                  ) : (
                    <span className="text-xs text-[var(--color-fg-subtle)]">
                      Never used
                    </span>
                  )}
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  {/* Urgency-aware expiry: danger "Expired", warning countdown */}
                  {/* within 14 days, else plain muted relative time. */}
                  <ExpiryBadge expiresAt={k.expires_at} />
                </TableCell>
                <TableCell className="hidden lg:table-cell">
                  <span
                    className="text-xs text-[var(--color-fg-muted)]"
                    title={formatAbsoluteDate(k.created_at)}
                  >
                    {formatRelativeDate(k.created_at)}
                  </span>
                </TableCell>
                <TableCell className="text-right">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onDelete(k)}
                    className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
                  >
                    <Trash2 className="size-3.5" />
                    Revoke
                  </Button>
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  );
}

function SkeletonRows(): React.ReactElement {
  return (
    <>
      {Array.from({ length: 3 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell className="py-3">
            <Skeleton className="h-3 w-32" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-3 w-20" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-3 w-20" />
          </TableCell>
          {/* Expires column skeleton (md+). */}
          <TableCell className="hidden md:table-cell">
            <Skeleton className="h-3 w-16" />
          </TableCell>
          <TableCell className="hidden lg:table-cell">
            <Skeleton className="h-3 w-20" />
          </TableCell>
          <TableCell>
            <Skeleton className="ml-auto h-7 w-20 rounded-md" />
          </TableCell>
        </TableRow>
      ))}
    </>
  );
}
