import * as React from "react";
import { ChevronDown, MinusCircle, Shield, UserPlus } from "lucide-react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
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
import { UserCell } from "@/components/users/user-cell";
import { formatRelativeDate } from "@/lib/format";
import type { TenantUser } from "@/lib/api/tenant-users";
import { RoleSummaryChips } from "./role-summary-chips";
import { StatusPill } from "./status-pill";

interface TenantUsersTableProps {
  users: TenantUser[];
  loading?: boolean;
  selfUserID?: string;
  onDisable: (u: TenantUser) => void;
  onEnable: (u: TenantUser) => void;
  onElevate: (u: TenantUser) => void;
}

// FUT-012 Phase C — TenantUsersTable.
//
// Columns: User (UserCell), kind chip, role summary chips, status pill,
// last login (relative), actions menu.
//
// The actions menu shows "Disable" / "Enable" + "Elevate to org-admin"
// depending on the current status. Self-disable is blocked at the BFF
// but we also hide the menu item for the caller themselves so the
// affordance never even renders — fewer 400s + clearer UX.
export function TenantUsersTable({
  users,
  loading,
  selfUserID,
  onDisable,
  onEnable,
  onElevate,
}: TenantUsersTableProps): React.ReactElement {
  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[26%]">User</TableHead>
            <TableHead className="w-[110px]">Kind</TableHead>
            <TableHead>Roles</TableHead>
            <TableHead className="w-[110px]">Status</TableHead>
            <TableHead className="hidden lg:table-cell w-[140px]">
              Last login
            </TableHead>
            <TableHead className="w-[110px] text-right">
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading ? (
            <SkeletonRows />
          ) : (
            users.map((u) => {
              const isSelf = u.user_id === selfUserID;
              const isDisabled = u.status === "disabled";
              const isInvited = u.status === "invited";
              return (
                <TableRow key={u.user_id}>
                  <TableCell>
                    <UserCell
                      userId={u.user_id}
                      username={u.username}
                      displayName={u.display_name}
                    />
                  </TableCell>
                  <TableCell>
                    <Badge tone={u.kind === "service_account" ? "neutral" : "accent"}>
                      {u.kind === "service_account" ? "Service" : "Human"}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <RoleSummaryChips roles={u.roles} />
                  </TableCell>
                  <TableCell>
                    <StatusPill status={u.status} />
                  </TableCell>
                  <TableCell className="hidden lg:table-cell text-xs text-[var(--color-fg-muted)]">
                    {u.last_login_at ? formatRelativeDate(u.last_login_at) : "Never"}
                  </TableCell>
                  <TableCell className="text-right">
                    <DropdownMenu.Root>
                      <DropdownMenu.Trigger asChild>
                        <Button variant="ghost" size="sm" disabled={isSelf}>
                          Actions <ChevronDown className="size-3.5" />
                        </Button>
                      </DropdownMenu.Trigger>
                      <DropdownMenu.Portal>
                        <DropdownMenu.Content
                          align="end"
                          className="z-50 min-w-[180px] rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-1 shadow-[var(--shadow-card)]"
                        >
                          {!isDisabled && !isInvited && !isSelf ? (
                            <MenuItem
                              icon={<Shield className="size-3.5" />}
                              label="Elevate to org-admin"
                              onSelect={() => onElevate(u)}
                            />
                          ) : null}
                          {!isDisabled && !isInvited && !isSelf ? (
                            <MenuItem
                              icon={<MinusCircle className="size-3.5" />}
                              label="Disable user"
                              danger
                              onSelect={() => onDisable(u)}
                            />
                          ) : null}
                          {isDisabled ? (
                            <MenuItem
                              icon={<UserPlus className="size-3.5" />}
                              label="Re-enable user"
                              onSelect={() => onEnable(u)}
                            />
                          ) : null}
                          {isInvited ? (
                            <DisabledMenuItem label="Pending invite — cancel via invite link" />
                          ) : null}
                          {isSelf ? (
                            <DisabledMenuItem label="No actions on self" />
                          ) : null}
                        </DropdownMenu.Content>
                      </DropdownMenu.Portal>
                    </DropdownMenu.Root>
                  </TableCell>
                </TableRow>
              );
            })
          )}
        </TableBody>
      </Table>
    </div>
  );
}

function MenuItem({
  icon,
  label,
  danger,
  onSelect,
}: {
  icon: React.ReactNode;
  label: string;
  danger?: boolean;
  onSelect: () => void;
}): React.ReactElement {
  return (
    <DropdownMenu.Item
      onSelect={onSelect}
      className={`flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm outline-none hover:bg-[var(--color-surface-sunken)] ${
        danger ? "text-[var(--color-danger)]" : "text-[var(--color-fg)]"
      }`}
    >
      {icon} {label}
    </DropdownMenu.Item>
  );
}

function DisabledMenuItem({ label }: { label: string }): React.ReactElement {
  return (
    <div className="px-2 py-1.5 text-xs text-[var(--color-fg-subtle)]">{label}</div>
  );
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
                <Skeleton className="h-3 w-44" />
                <Skeleton className="h-2.5 w-24" />
              </div>
            </div>
          </TableCell>
          <TableCell><Skeleton className="h-4 w-16 rounded-full" /></TableCell>
          <TableCell><Skeleton className="h-4 w-36" /></TableCell>
          <TableCell><Skeleton className="h-4 w-16 rounded-full" /></TableCell>
          <TableCell className="hidden lg:table-cell"><Skeleton className="h-3 w-20" /></TableCell>
          <TableCell><Skeleton className="ml-auto h-7 w-20 rounded" /></TableCell>
        </TableRow>
      ))}
    </>
  );
}
