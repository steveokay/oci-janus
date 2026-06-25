import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { AlertTriangle, MinusCircle } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { useSetUserDisabled } from "@/lib/api/tenant-users";
import type { TenantUser } from "@/lib/api/tenant-users";

interface DisableUserDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  user: TenantUser | null;
}

// FUT-012 Phase C — type-the-username gate, same shape as PR #109's
// bulk-tag-delete fix. Forces the operator's eye onto the username
// before they confirm a destructive action that revokes JWTs + disables
// API keys.
//
// The dialog body explains the blast radius BEFORE the type-to-confirm
// input so the operator sees what's about to happen.
export function DisableUserDialog({
  open,
  onOpenChange,
  user,
}: DisableUserDialogProps): React.ReactElement {
  const disable = useSetUserDisabled();
  const [typed, setTyped] = React.useState("");

  React.useEffect(() => {
    if (!open) setTyped("");
  }, [open]);

  const expected = user?.username ?? "";
  const canConfirm = typed === expected && expected !== "";

  async function handleConfirm(): Promise<void> {
    if (!user) return;
    try {
      const result = await disable.mutateAsync({
        user_id: user.user_id,
        disabled: true,
      });
      toast.success(
        `Disabled @${user.username}. Status is now ${result.status}.`,
      );
      onOpenChange(false);
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Tenant-admin role required."
          : status === 400
            ? "You can't disable yourself."
            : status === 412
              ? "User is in invited state — cancel the invite instead."
              : "Disable failed. Retry, or check the BFF logs.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-danger)]">
            <AlertTriangle className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Disable user
            </span>
          </div>
          <DialogTitle>Disable @{user?.username}?</DialogTitle>
          <DialogDescription>
            Disabling revokes every active JWT for this user (forces a
            re-login) and disables all of their API keys. Their role
            grants stay intact — re-enabling restores access without
            re-granting.
          </DialogDescription>
        </DialogHeader>

        <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-3 text-xs">
          <ul className="space-y-1 text-[var(--color-fg-muted)]">
            <li>• Active JWT sessions: revoked</li>
            <li>• API keys: flipped to <code>is_active=false</code></li>
            <li>• Role grants: preserved (re-enabling restores access)</li>
            <li>• Audit row written via <code>rbac.role_revoked</code> not fired (status flip uses its own trail)</li>
          </ul>
        </div>

        <div>
          <Label htmlFor="disable-confirm" className="mb-2 inline-block">
            Type{" "}
            <code className="font-mono text-[var(--color-danger)]">
              {expected}
            </code>{" "}
            to confirm
          </Label>
          <Input
            id="disable-confirm"
            autoFocus
            autoComplete="off"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            className="font-mono"
          />
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={disable.isPending}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            onClick={() => void handleConfirm()}
            loading={disable.isPending}
            disabled={!canConfirm || disable.isPending}
          >
            <MinusCircle className="size-4" />
            {disable.isPending ? "Disabling" : "Disable user"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
