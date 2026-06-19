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
import { CopyButton } from "@/components/ui/copy-button";
import { RoleBadge } from "./role-badge";
import type { Member } from "@/lib/api/members";

interface MembersTableProps {
  members: Member[];
  loading?: boolean;
  onRemove: (member: Member) => void;
}

// Beacon — MembersTable. Reusable across the org-members page and the
// repo-detail Members tab. Shows user_id (a UUID — the only identifier the
// backend exposes today), role badge, who granted it, and a single
// per-row remove affordance.
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
                  <div className="flex items-center gap-3">
                    <Initial userId={m.user_id} />
                    <div className="min-w-0">
                      <div className="font-mono text-xs text-[var(--color-fg)]">
                        {m.user_id}
                      </div>
                      <div className="text-[10px] uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
                        user · {m.id.slice(0, 8)}
                      </div>
                    </div>
                    <CopyButton value={m.user_id} iconOnly />
                  </div>
                </TableCell>
                <TableCell>
                  <RoleBadge role={m.role} />
                </TableCell>
                <TableCell className="hidden font-mono text-xs text-[var(--color-fg-muted)] lg:table-cell">
                  {m.granted_by || "—"}
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

// Initial — derive a single character from the UUID for the avatar tile.
// Deterministic so the same user always renders the same letter.
function Initial({ userId }: { userId: string }): React.ReactElement {
  const ch = (userId.replace(/[^a-z0-9]/gi, "")[0] ?? "·").toUpperCase();
  return (
    <span
      className="grid size-8 shrink-0 place-items-center rounded-md bg-[var(--color-accent-subtle)] font-display text-sm font-semibold text-[var(--color-accent)]"
      aria-hidden
    >
      {ch}
    </span>
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
