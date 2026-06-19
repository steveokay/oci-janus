import * as React from "react";
import { HardDrive, Trash2 } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { CopyButton } from "@/components/ui/copy-button";
import { formatRelativeDate } from "@/lib/format";
import type { AdminTenant } from "@/lib/api/admin-tenants";

interface TenantsTableProps {
  tenants: AdminTenant[];
  loading?: boolean;
  onSetQuota: (tenant: AdminTenant) => void;
  onDelete: (tenant: AdminTenant) => void;
}

export function TenantsTable({
  tenants,
  loading,
  onSetQuota,
  onDelete,
}: TenantsTableProps): React.ReactElement {
  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Tenant</TableHead>
            <TableHead>Plan</TableHead>
            <TableHead className="hidden lg:table-cell">Created</TableHead>
            <TableHead className="w-[180px] text-right">
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading ? (
            <SkeletonRows />
          ) : (
            tenants.map((t) => (
              <TableRow key={t.tenant_id}>
                <TableCell className="py-3">
                  <div className="flex items-center gap-3">
                    <TenantInitial name={t.name} />
                    <div className="min-w-0">
                      <div className="text-sm font-medium text-[var(--color-fg)]">
                        {t.name}
                      </div>
                      <div className="mt-0.5 font-mono text-[10px] text-[var(--color-fg-subtle)]">
                        {t.tenant_id.slice(0, 8)}
                      </div>
                    </div>
                    {/* Copy moved out of the inner stack so it has its own
                        vertical centerline. Previously it shared a line with
                        the UUID; with the icon-button being h-9 wide, the
                        cell content exceeded h-12 and pushed the name into
                        the row's top border. */}
                    <CopyButton value={t.tenant_id} iconOnly />
                  </div>
                </TableCell>
                <TableCell>
                  <PlanBadge plan={t.plan} />
                </TableCell>
                <TableCell className="hidden text-xs text-[var(--color-fg-muted)] lg:table-cell">
                  {formatRelativeDate(t.created_at)}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex items-center justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onSetQuota(t)}
                      className="text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
                    >
                      <HardDrive className="size-3.5" />
                      Quota
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onDelete(t)}
                      className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
                    >
                      <Trash2 className="size-3.5" />
                      Delete
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  );
}

function TenantInitial({ name }: { name: string }): React.ReactElement {
  const ch = (name.replace(/[^a-z0-9]/gi, "")[0] ?? "·").toUpperCase();
  return (
    <span
      className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] font-display text-sm font-semibold text-[var(--color-accent)]"
      aria-hidden
    >
      {ch}
    </span>
  );
}

function PlanBadge({ plan }: { plan: string }): React.ReactElement {
  const lc = plan.toLowerCase();
  const tone =
    lc === "enterprise"
      ? "accent"
      : lc === "pro"
        ? "success"
        : "neutral";
  return <Badge tone={tone}>{plan || "—"}</Badge>;
}

function SkeletonRows(): React.ReactElement {
  return (
    <>
      {Array.from({ length: 4 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell>
            <div className="flex items-center gap-3">
              <Skeleton className="size-8 rounded-md" />
              <div className="space-y-1.5">
                <Skeleton className="h-3.5 w-36" />
                <Skeleton className="h-2.5 w-20" />
              </div>
            </div>
          </TableCell>
          <TableCell>
            <Skeleton className="h-5 w-20 rounded-full" />
          </TableCell>
          <TableCell className="hidden lg:table-cell">
            <Skeleton className="h-3 w-28" />
          </TableCell>
          <TableCell>
            <div className="flex justify-end gap-1">
              <Skeleton className="h-7 w-20 rounded-md" />
              <Skeleton className="h-7 w-20 rounded-md" />
            </div>
          </TableCell>
        </TableRow>
      ))}
    </>
  );
}
