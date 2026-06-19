import * as React from "react";
import { toast } from "sonner";
import { AlertTriangle } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { RoleBadge } from "./role-badge";
import type { Member } from "@/lib/api/members";

interface RemoveMemberDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  member: Member | null;
  scopeLabel: string;
  onRevoke: (assignmentId: string) => Promise<void>;
}

// Beacon — RemoveMemberDialog. Lighter-touch than the type-to-confirm
// pattern used for repo / tag deletes — revoking a role doesn't drop data,
// the operator can just re-grant. We do still gate behind a confirmation
// step so an errant click can't drop someone mid-call.
export function RemoveMemberDialog({
  open,
  onOpenChange,
  member,
  scopeLabel,
  onRevoke,
}: RemoveMemberDialogProps): React.ReactElement {
  const [submitting, setSubmitting] = React.useState(false);

  async function handleConfirm(): Promise<void> {
    if (!member) return;
    setSubmitting(true);
    try {
      await onRevoke(member.id);
      toast.success(`Removed ${member.user_id.slice(0, 8)}…`);
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const message =
        status === 403
          ? "You don't have permission to revoke roles on this scope."
          : "Couldn't revoke. Try again, or check the BFF logs.";
      toast.error(message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-danger)]">
            <AlertTriangle className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Revoke access
            </span>
          </div>
          <DialogTitle>Remove member</DialogTitle>
          <DialogDescription>
            This revokes the role from{" "}
            <span className="font-mono text-[var(--color-fg)]">
              {scopeLabel}
            </span>
            . The user keeps any other role assignments they have.
          </DialogDescription>
        </DialogHeader>

        {member ? (
          <div className="flex items-center gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2.5">
            <RoleBadge role={member.role} />
            <span className="truncate font-mono text-xs text-[var(--color-fg-muted)]">
              {member.user_id}
            </span>
          </div>
        ) : null}

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            onClick={() => void handleConfirm()}
            disabled={!member || submitting}
            loading={submitting}
          >
            {submitting ? "Removing" : "Remove member"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
