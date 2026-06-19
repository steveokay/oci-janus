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
              <TableRow key={k.key_id}>
                <TableCell className="py-3">
                  <div className="text-sm font-medium text-[var(--color-fg)]">
                    {k.name}
                  </div>
                  {k.description ? (
                    <div className="mt-0.5 truncate text-xs text-[var(--color-fg-muted)]">
                      {k.description}
                    </div>
                  ) : null}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <code className="font-mono text-[11px] text-[var(--color-fg-muted)]">
                      {k.key_id.slice(0, 8)}…
                    </code>
                    <CopyButton value={k.key_id} iconOnly />
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
