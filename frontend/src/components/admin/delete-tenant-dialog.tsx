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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import {
  useDeleteTenant,
  type AdminTenant,
} from "@/lib/api/admin-tenants";

interface DeleteTenantDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tenant: AdminTenant;
}

// Beacon — DeleteTenantDialog.
//
// This is the heaviest destructive flow in the app: deleting a tenant
// cascade-soft-deletes every repository in it. The backend handles GC of
// the blobs; the action itself is irreversible from the UI. Type-the-name
// gating is mandatory.
export function DeleteTenantDialog({
  open,
  onOpenChange,
  tenant,
}: DeleteTenantDialogProps): React.ReactElement {
  const [confirmText, setConfirmText] = React.useState("");
  const del = useDeleteTenant();
  const matches = confirmText.trim() === tenant.name;
  const submitting = del.isPending;

  React.useEffect(() => {
    if (!open) setConfirmText("");
  }, [open]);

  async function onConfirm(): Promise<void> {
    try {
      await del.mutateAsync(tenant.tenant_id);
      toast.success(`Tenant "${tenant.name}" deleted.`);
      onOpenChange(false);
    } catch (e) {
      const status = (e as { response?: { status?: number } })?.response?.status;
      const message =
        status === 403
          ? "Platform-admin role required."
          : status === 404
            ? "Tenant not found (or admin routes disabled on the BFF)."
            : "Delete failed. Check the BFF logs.";
      toast.error(message);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <div className="mb-2 flex items-center gap-2 text-[var(--color-danger)]">
            <AlertTriangle className="size-4" />
            <span className="text-xs font-medium uppercase tracking-[0.16em]">
              Destructive action
            </span>
          </div>
          <DialogTitle>Delete tenant</DialogTitle>
          <DialogDescription>
            This soft-deletes the tenant{" "}
            <span className="font-mono text-[var(--color-fg)]">
              {tenant.name}
            </span>{" "}
            and cascades to every repository under it. Blob storage is freed by
            garbage collection. This cannot be undone from the UI.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label htmlFor="confirm-tenant">
            Type{" "}
            <span className="font-mono normal-case text-[var(--color-fg)]">
              {tenant.name}
            </span>{" "}
            to confirm
          </Label>
          <Input
            id="confirm-tenant"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={tenant.name}
            className="font-mono"
          />
          <p className="font-mono text-[10px] text-[var(--color-fg-subtle)]">
            {tenant.tenant_id}
          </p>
        </div>

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
            onClick={() => void onConfirm()}
            disabled={!matches || submitting}
            loading={submitting}
          >
            {submitting ? "Deleting" : "Delete tenant"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
