import * as React from "react";
import { toast } from "sonner";
import { AxiosError } from "axios";
import { Shield } from "lucide-react";
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
import { useElevateToOrgAdmin } from "@/lib/api/tenant-users";
import type { TenantUser } from "@/lib/api/tenant-users";

interface ElevateToOrgAdminDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  user: TenantUser | null;
}

// FUT-012 Phase C — issues a (admin, org, <org>) grant for the target
// user. This is the "strict tenant-admin posture" escape hatch the user
// asked for: tenant-admin can self-grant org-admin one org at a time so
// the audit trail records each elevation.
//
// Org-name allowlist mirrors the platform's validateOrgName regex
// (^[a-z0-9-]{2,64}$) but we don't enforce client-side — the BFF
// rejects with 400 and the toast surfaces it. Less brittle than
// duplicating regex logic across the two layers.
export function ElevateToOrgAdminDialog({
  open,
  onOpenChange,
  user,
}: ElevateToOrgAdminDialogProps): React.ReactElement {
  const elevate = useElevateToOrgAdmin();
  const [org, setOrg] = React.useState("");

  React.useEffect(() => {
    if (!open) setOrg("");
  }, [open]);

  async function handleConfirm(): Promise<void> {
    if (!user) return;
    try {
      await elevate.mutateAsync({ user_id: user.user_id, org: org.trim() });
      toast.success(`Granted org-admin on ${org} to @${user.username}.`);
      onOpenChange(false);
    } catch (e) {
      const status = (e as AxiosError | undefined)?.response?.status;
      toast.error(
        status === 403
          ? "Tenant-admin role required."
          : status === 400
            ? "Invalid org name (lowercase letters, digits, hyphens; 2-64 chars)."
            : "Elevation failed. Retry, or check the BFF logs.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Shield className="size-4 text-[var(--color-accent)]" />
            Elevate @{user?.username} to org-admin
          </DialogTitle>
          <DialogDescription>
            Grants the <code>admin</code> role on one specific org. The
            grant is recorded in the audit trail (<code>rbac.role_granted</code>)
            so the elevation is reviewable later. Use the org-members
            page for finer-grained role grants.
          </DialogDescription>
        </DialogHeader>

        <div>
          <Label htmlFor="elevate-org">Org name</Label>
          <Input
            id="elevate-org"
            autoFocus
            autoComplete="off"
            value={org}
            onChange={(e) => setOrg(e.target.value)}
            placeholder="dev"
            className="font-mono"
          />
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
            Must match an existing org. Lowercase, digits, hyphens; 2-64 chars.
          </p>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={elevate.isPending}
          >
            Cancel
          </Button>
          <Button
            type="button"
            onClick={() => void handleConfirm()}
            loading={elevate.isPending}
            disabled={org.trim() === "" || elevate.isPending}
          >
            <Shield className="size-4" />
            {elevate.isPending ? "Granting" : "Grant org-admin"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
