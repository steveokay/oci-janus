import * as React from "react";
import { UserMinus } from "lucide-react";
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
import { UserCell } from "@/components/users/user-cell";
import { RoleBadge } from "./role-badge";
import type { Member } from "@/lib/api/members";

interface MembersTableProps {
  members: Member[];
  loading?: boolean;
  onRemove: (member: Member) => void;
}

// Beacon — MembersTable. Reusable across the org-members page and the
// repo-detail Members tab. REM-018 Phase B: principal + granted-by columns
// now render via the shared <UserCell> primitive so usernames + display
// names replace the raw UUIDs that used to sit here.
export function MembersTable({
  members,
  loading,
  onRemove,
}: MembersTableProps): React.ReactElement {
  return (
    <div className="overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-card)]">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[45%]">User</TableHead>
            <TableHead>Role</TableHead>
            <TableHead className="hidden lg:table-cell">Granted by</TableHead>
            <TableHead className="w-[120px] text-right">
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {loading ? (
            <SkeletonRows />
          ) : (
            members.map((m) => (
              <TableRow key={m.id}>
                <TableCell>
                  <UserCell
                    userId={m.user_id}
                    username={m.username}
                    displayName={m.display_name}
                    withCopy
                  />
                </TableCell>
                <TableCell>
                  <RoleBadge role={m.role} />
                </TableCell>
                <TableCell className="hidden lg:table-cell">
                  <UserCell
                    userId={m.granted_by}
                    username={m.granted_by_username}
                    displayName={m.granted_by_display_name}
                    variant="inline"
                  />
                </TableCell>
                <TableCell className="text-right">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onRemove(m)}
                    className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
                  >
                    <UserMinus className="size-4" />
                    Remove
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
      {Array.from({ length: 4 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell>
            <div className="flex items-center gap-3">
              <Skeleton className="size-8 rounded-md" />
              <div className="space-y-1.5">
                <Skeleton className="h-3 w-56" />
                <Skeleton className="h-2.5 w-20" />
              </div>
            </div>
          </TableCell>
          <TableCell>
            <Skeleton className="h-5 w-20 rounded-full" />
          </TableCell>
          <TableCell className="hidden lg:table-cell">
            <Skeleton className="h-3 w-32" />
          </TableCell>
          <TableCell>
            <Skeleton className="ml-auto h-7 w-20 rounded-md" />
          </TableCell>
        </TableRow>
      ))}
    </>
  );
}
